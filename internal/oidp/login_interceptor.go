package oidp

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

const (
	ModeDM    = "dm"
	ModeGroup = "group"
)

type ActiveAppReader interface {
	ListActiveBySession(ctx context.Context, sessionID string) ([]domain.OAuthClient, error)
}

type GroupMemberChecker interface {
	IsActiveGroupMember(ctx context.Context, sessionID, groupJID, senderLID string) (bool, error)
}

type BotFeedback interface {
	React(ctx context.Context, organizationID, sessionID, chatJID, senderJID, messageID, emoji string) error
	Reply(ctx context.Context, organizationID, sessionID, chatJID, messageID, text string) error
}

type LoginInterceptor struct {
	apps    ActiveAppReader
	pending *PendingStore
	members GroupMemberChecker
	bot     BotFeedback
	now     func() int64
	log     *slog.Logger

	mu    sync.RWMutex
	cache map[string]sessionApps
}

type sessionApps struct {
	apps    []domain.OAuthClient
	re      *regexp.Regexp
	byLower map[string][]domain.OAuthClient
}

func NewLoginInterceptor(apps ActiveAppReader, pending *PendingStore, members GroupMemberChecker, bot BotFeedback, log *slog.Logger) *LoginInterceptor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &LoginInterceptor{
		apps: apps, pending: pending, members: members, bot: bot, log: log,
		now:   func() int64 { return time.Now().UnixMilli() },
		cache: map[string]sessionApps{},
	}
}

func (l *LoginInterceptor) InvalidateSession(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if sessionID == "" {
		l.cache = map[string]sessionApps{}
		return
	}
	delete(l.cache, sessionID)
}

func (l *LoginInterceptor) HandleLogin(ctx context.Context, nm *inbound.NormalizedMessage) (bool, error) {
	if l == nil || l.apps == nil || l.pending == nil || nm == nil || nm.FromMe || nm.Kind != inbound.KindMessage {
		return false, nil
	}
	body := strings.TrimSpace(nm.Body)
	if body == "" {
		return false, nil
	}
	if strings.EqualFold(body, "STOP") && nm.IsDM {
		res, err := l.pending.DenyRecentForSender(ctx, nm.SenderLID)
		if err == nil && res.Status == ClaimStatusDenied && l.bot != nil {
			_ = l.bot.React(ctx, nm.OrganizationID, nm.SessionID, nm.ChatJID, nm.SenderJID, nm.WAMessageID, "🛑")
		}
		return res.Status == ClaimStatusDenied, err
	}

	apps, err := l.sessionApps(ctx, nm.SessionID)
	if err != nil || apps.re == nil {
		return false, err
	}
	m := apps.re.FindStringSubmatch(body)
	if m == nil {
		return false, nil
	}
	command := strings.ToLower(m[1])
	userCode := m[2]
	mode := ModeDM
	if nm.IsGroup {
		mode = ModeGroup
	} else if !nm.IsDM {
		_ = l.feedback(ctx, nm, ClaimResult{Status: ClaimStatusWrong}, nil)
		return true, nil
	}

	candidates := apps.byLower[command]
	if mode == ModeGroup {
		candidates = filterGroupCandidates(ctx, l.members, nm, candidates)
		if len(candidates) == 0 {
			_ = l.feedback(ctx, nm, ClaimResult{Status: ClaimStatusWrong}, nil)
			return true, nil
		}
	}

	in := ClaimInput{
		SessionID: nm.SessionID, UserCode: userCode, Mode: mode, LoginCommand: command,
		SenderLID: nm.SenderLID, PhoneJID: nm.SenderJID, PhoneNumber: nm.SenderPhone,
		PushName: nm.PushName, NowMs: l.now(),
	}
	if mode == ModeGroup {
		in.GroupJID = nm.ChatJID
	}
	res, err := l.pending.ClaimVerified(ctx, in)
	if err != nil {
		return true, err
	}
	if res.Status == ClaimStatusExpired || res.Status == ClaimStatusWrong {
		if counted, countErr := l.pending.ClaimWrongCode(ctx, nm.SessionID, nm.SenderLID, l.now()); countErr == nil && counted.Status == ClaimStatusRateLimited {
			res = counted
		}
	}
	var app *domain.OAuthClient
	for i := range candidates {
		if candidates[i].ClientID == res.ClientID || strings.EqualFold(candidates[i].LoginCommand, command) {
			app = &candidates[i]
			break
		}
	}
	if res.Status == ClaimStatusVerified && res.BrowserCode != "" {
		ttl := time.Duration(maxInt64(0, int64(10*time.Minute)))
		_ = l.pending.RememberStop(ctx, nm.SenderLID, res.BrowserCode, ttl)
	}
	_ = l.feedback(ctx, nm, res, app)
	return true, nil
}

func (l *LoginInterceptor) sessionApps(ctx context.Context, sessionID string) (sessionApps, error) {
	l.mu.RLock()
	cached, ok := l.cache[sessionID]
	l.mu.RUnlock()
	if ok {
		return cached, nil
	}
	clients, err := l.apps.ListActiveBySession(ctx, sessionID)
	if err != nil {
		return sessionApps{}, err
	}
	out := buildSessionApps(clients)
	l.mu.Lock()
	l.cache[sessionID] = out
	l.mu.Unlock()
	return out, nil
}

func buildSessionApps(clients []domain.OAuthClient) sessionApps {
	commands := map[string]struct{}{}
	by := map[string][]domain.OAuthClient{}
	for _, c := range clients {
		cmd := strings.ToLower(strings.TrimSpace(c.LoginCommand))
		if cmd == "" {
			continue
		}
		commands[cmd] = struct{}{}
		by[cmd] = append(by[cmd], c)
	}
	if len(commands) == 0 {
		return sessionApps{apps: clients, byLower: by}
	}
	parts := make([]string, 0, len(commands))
	for cmd := range commands {
		parts = append(parts, regexp.QuoteMeta(cmd))
	}
	sort.Strings(parts)
	return sessionApps{
		apps: clients, byLower: by,
		re: regexp.MustCompile(`(?i)^\s*(` + strings.Join(parts, "|") + `)\s+(\d{6})\s*$`),
	}
}

func filterGroupCandidates(ctx context.Context, members GroupMemberChecker, nm *inbound.NormalizedMessage, in []domain.OAuthClient) []domain.OAuthClient {
	out := make([]domain.OAuthClient, 0, len(in))
	for _, c := range in {
		if c.GroupJID == nil || *c.GroupJID != nm.ChatJID || !mentionedBot(nm, c) {
			continue
		}
		if members != nil {
			ok, err := members.IsActiveGroupMember(ctx, nm.SessionID, nm.ChatJID, nm.SenderLID)
			if err != nil || !ok {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func mentionedBot(nm *inbound.NormalizedMessage, _ domain.OAuthClient) bool {
	self := map[string]struct{}{}
	if nm.SelfJID != "" {
		self[nm.SelfJID] = struct{}{}
	}
	if nm.SelfLID != "" {
		self[nm.SelfLID] = struct{}{}
	}
	if len(self) == 0 {
		return false
	}
	for _, mention := range nm.Mentions {
		if _, ok := self[mention]; ok {
			return true
		}
	}
	return false
}

func (l *LoginInterceptor) feedback(ctx context.Context, nm *inbound.NormalizedMessage, res ClaimResult, app *domain.OAuthClient) error {
	if l.bot == nil {
		return nil
	}
	emoji, text := "", ""
	appName := res.AppName
	if app != nil && app.Name != "" {
		appName = app.Name
	}
	switch res.Status {
	case ClaimStatusVerified:
		emoji = "✅"
		if appName == "" {
			appName = "this app"
		}
		text = fmt.Sprintf("You're signed in to %s. Return to your browser. Warning: this signs you in to %s. If you didn't start this, reply STOP.", appName, appName)
	case ClaimStatusAlreadyUsed:
		emoji = "⌛"
	case ClaimStatusRateLimited:
		return nil
	default:
		emoji = "❌"
		text = "That sign-in code is invalid or expired."
	}
	if emoji != "" {
		_ = l.bot.React(ctx, nm.OrganizationID, nm.SessionID, nm.ChatJID, nm.SenderJID, nm.WAMessageID, emoji)
	}
	if text != "" && nm.IsDM {
		_ = l.bot.Reply(ctx, nm.OrganizationID, nm.SessionID, nm.ChatJID, nm.WAMessageID, text)
	}
	return nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

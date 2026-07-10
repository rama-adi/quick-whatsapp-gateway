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
	"go.mau.fi/whatsmeow/types"
)

const (
	ModeDM    = "dm"
	ModeGroup = "group"
)

// ActiveAppReader returns login-command definitions for active OAuth clients on
// one WhatsApp session.
type ActiveAppReader interface {
	ListActiveBySession(ctx context.Context, sessionID string) ([]domain.OAuthClient, error)
}

// GroupMemberChecker verifies that a group-mode claimant is still an active
// member of the configured group at message-processing time.
type GroupMemberChecker interface {
	IsActiveGroupMember(ctx context.Context, sessionID, groupJID, senderLID string) (bool, error)
}

// BotFeedback emits best-effort WhatsApp reactions and replies explaining a
// claim result. Feedback failure never changes the atomic PendingStore outcome.
type BotFeedback interface {
	React(ctx context.Context, organizationID, sessionID, chatJID, senderJID, messageID, emoji string) error
	Reply(ctx context.Context, organizationID, sessionID, chatJID, messageID, text string) error
}

// LoginInterceptor recognizes OAuth login commands before the normal inbound
// persistence/fan-out stages. Per-session command regexes are cached behind mu;
// control-bus invalidation removes the cache entry so changed app configuration
// is observed on the next message. PendingStore remains the authority for
// single-use claims, attempt caps, and concurrent winners.
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

// NewLoginInterceptor constructs a stateless message interceptor plus an empty
// per-session app cache. It starts no background work and is safe for concurrent
// HandleLogin calls.
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

// InvalidateSession evicts one session's compiled command index, or every entry
// when sessionID is empty. Concurrent readers keep their immutable snapshot;
// the next message reloads active clients from the repository.
func (l *LoginInterceptor) InvalidateSession(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if sessionID == "" {
		l.cache = map[string]sessionApps{}
		return
	}
	delete(l.cache, sessionID)
}

// mentionTokenRe matches the literal "@<jid-user>" tokens WhatsApp embeds in
// the message text for @-mentions (phone number or LID user, optionally with
// a ":device" suffix).
var mentionTokenRe = regexp.MustCompile(`(^|\s)@[0-9][0-9:.]*`)

// HandleLogin recognizes STOP and configured login commands before ordinary
// inbound persistence. It returns handled=true whenever this interceptor owns the
// message, including rejected claims, so credentials and attempts are never fanned
// out as chat content; PendingStore atomically chooses the winner under duplicates.
// Bot feedback is best effort and cannot change a successful or failed claim.
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
	if nm.IsGroup {
		// In groups the raw body carries the mention as literal "@<jid-user>"
		// text ("@628xx login 443811"), so drop mention tokens before matching.
		// Whether the BOT was mentioned is enforced via nm.Mentions in
		// filterGroupCandidates, not from the body text.
		body = strings.TrimSpace(mentionTokenRe.ReplaceAllString(body, " "))
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
		if c.GroupJID == nil || !sameJID(*c.GroupJID, nm.ChatJID) || !mentionedBot(nm, c) {
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
	var self []jidKey
	if nm.SelfJID != "" {
		self = append(self, canonicalJIDKeys(nm.SelfJID)...)
	}
	if nm.SelfLID != "" {
		self = append(self, canonicalJIDKeys(nm.SelfLID)...)
	}
	if len(self) == 0 {
		return false
	}
	for _, mention := range nm.Mentions {
		for _, mentionKey := range canonicalJIDKeys(mention) {
			for _, selfKey := range self {
				if sameJIDKey(mentionKey, selfKey) {
					return true
				}
			}
		}
	}
	return false
}

type jidKey struct {
	user   string
	server string
}

func sameJID(a, b string) bool {
	for _, ak := range canonicalJIDKeys(a) {
		for _, bk := range canonicalJIDKeys(b) {
			if sameJIDKey(ak, bk) {
				return true
			}
		}
	}
	return false
}

func sameJIDKey(a, b jidKey) bool {
	if a.user == "" || b.user == "" || a.user != b.user {
		return false
	}
	return a.server == "" || b.server == "" || a.server == b.server
}

func canonicalJIDKeys(raw string) []jidKey {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return nil
	}
	// types.ParseJID treats an @-less string as server-only, so only use it
	// when a server part is present; bare users fall through below.
	if strings.Contains(raw, "@") {
		if jid, err := types.ParseJID(raw); err == nil && !jid.IsEmpty() {
			jid = jid.ToNonAD()
			if jid.User == "" {
				return nil
			}
			return []jidKey{{user: jid.User, server: jid.Server}}
		}
	}

	user, server, ok := strings.Cut(raw, "@")
	if !ok {
		user = raw
	}
	if before, _, ok := strings.Cut(user, ":"); ok {
		user = before
	}
	if user == "" {
		return nil
	}
	return []jidKey{{user: user, server: server}}
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

package inbound

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Pipeline runs the ordered inbound stages from masterplan §7 for every
// whatsmeow event, tagged with its session/organization:
//
//  1. normalize        -> versioned envelope + NormalizedMessage
//  2. command intercept -> admin-session prefix commands are dropped
//  3. capture          -> identities, contacts, groups, group members
//  4. persist          -> chats, messages, poll votes, receipt status
//  5. auto-read        -> read receipt (+ optional "composing") BEFORE fan-out
//  6. fan-out          -> event sink + webhook enqueue + event_log append
//
// All collaborators are CONSUMER INTERFACES (see ports.go) injected via the
// constructor — no globals, no sibling-package imports. Pipeline configuration
// is immutable after construction, so one instance is safe to call concurrently
// for many sessions; per-event tenant and session identity enters only through
// Process and is overwritten onto the normalized working view defensively.
//
// Required capture, enrichment, persistence, and fan-out failures stop the call
// and are returned with their stage name. Intentional drops return nil, while
// OAuth lookup and automatic-read failures are logged and contained at their
// best-effort boundaries. Process does not retry: callers or durable downstream
// queues own retry policy, avoiding duplicate message mutations inside a pass.
type Pipeline struct {
	normalizer Normalizer
	commands   CommandRegistry
	login      LoginInterceptor
	repos      Repos
	sink       EventSink
	webhooks   WebhookEnqueuer
	wa         WAClient
	clock      Clock
	log        *slog.Logger

	// cmdPrefix is WHATSAPP_ADMIN_CMD_PREFIX (§6, default "am"); empty disables
	// the interceptor.
	cmdPrefix string

	// autoReadResolver reports whether a session has auto_read / presence_typing
	// enabled. It is a function rather than an interface so the manager can hand
	// the pipeline a closure over its live session config without inbound taking
	// a dependency on the session type.
	sessionConfig SessionConfigFunc
}

// SessionConfig is the per-session inbound behavior the pipeline needs.
type SessionConfig struct {
	AutoRead       bool
	PresenceTyping bool
}

// SessionConfigFunc resolves a session's inbound config by id. Returning ok=false
// (unknown session) disables auto-read for that event.
type SessionConfigFunc func(sessionID string) (SessionConfig, bool)

// Option configures optional Pipeline behavior.
type Option func(*Pipeline)

// WithCommandPrefix sets the admin command prefix (§6). Empty disables the
// interceptor entirely.
func WithCommandPrefix(prefix string) Option {
	return func(p *Pipeline) { p.cmdPrefix = prefix }
}

// WithSessionConfig injects the per-session auto_read/presence_typing resolver.
// Without it, auto-read is skipped (treated as disabled) for every session.
func WithSessionConfig(fn SessionConfigFunc) Option {
	return func(p *Pipeline) { p.sessionConfig = fn }
}

// WithLogger sets a structured logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(p *Pipeline) {
		if l != nil {
			p.log = l
		}
	}
}

// WithLoginInterceptor installs the OAuth login verifier at stage 2.
func WithLoginInterceptor(li LoginInterceptor) Option {
	return func(p *Pipeline) { p.login = li }
}

// NewPipeline constructs a Pipeline with constructor injection. The five core
// collaborators are required; behavioral knobs are Options.
func NewPipeline(
	normalizer Normalizer,
	commands CommandRegistry,
	repos Repos,
	sink EventSink,
	webhooks WebhookEnqueuer,
	wa WAClient,
	clock Clock,
	opts ...Option,
) *Pipeline {
	p := &Pipeline{
		normalizer: normalizer,
		commands:   commands,
		repos:      repos,
		sink:       sink,
		webhooks:   webhooks,
		wa:         wa,
		clock:      clock,
		cmdPrefix:  "am",
		log:        slog.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Process runs the full pipeline once for a raw whatsmeow event. It returns an
// error only for a required stage failure; filtered events, claimed commands,
// content-less system frames, and failures of explicitly best-effort automation
// return nil after their permitted side effects.
//
// Ordering guarantee: capture and persist happen before auto-read, and auto-read
// happens strictly before fan-out, so a reply consumer that reacts to the fanned
// event never races the read receipt (§7.5). The supplied context is propagated
// to every collaborator, so cancellation stops before the next stage rather than
// spawning detached work; already completed durable writes are not rolled back.
func (p *Pipeline) Process(ctx context.Context, sessionID, organizationID string, isAdminSession bool, evt any) error {
	// Stage 1: normalize.
	envelope, nm, ok := p.normalizer.Normalize(ctx, evt, sessionID, organizationID)
	if !ok {
		// Filtered/unrecognized event — nothing to do.
		return nil
	}
	// Defensive: keep the working view's tags authoritative.
	nm.SessionID = sessionID
	nm.OrganizationID = organizationID

	// Stage 2a: OAuth login interceptor. It owns matching login-command shapes
	// for active OAuth apps on this session, even when the code is invalid.
	if p.login != nil {
		handled, err := p.login.HandleLogin(ctx, nm)
		if err != nil {
			p.log.WarnContext(ctx, "oauth login interceptor error",
				slog.String("session", sessionID), slog.Any("err", err))
		}
		if handled {
			p.log.DebugContext(ctx, "inbound: dropped oauth login command",
				slog.String("session", sessionID))
			return nil
		}
	}

	// Stage 2b: command interceptor (admin session, prefixed text → drop).
	res, err := p.runInterceptor(ctx, isAdminSession, nm)
	if err != nil {
		// A registry error still drops the event, but we surface it for logging.
		p.log.WarnContext(ctx, "command registry error",
			slog.String("session", sessionID), slog.Any("err", err))
	}
	if res.drop {
		p.log.DebugContext(ctx, "inbound: dropped admin command",
			slog.String("session", sessionID))
		return nil
	}

	// Stage 3: identity / contacts / group capture.
	if err := p.capture(ctx, nm); err != nil {
		return fmt.Errorf("inbound capture: %w", err)
	}

	// Content-less system/control traffic (the classifier's unrecognized-content
	// fallthrough: E2E-encryption notices, ephemeral settings, sender-key
	// distribution, …) carries no displayable body. Capture has already refreshed
	// the sender's identity; drop the rest so these never land in `messages` or
	// fan out as empty "system" events. Real WhatsApp renders group notices from
	// typed group events, not from these.
	if nm.Kind == KindMessage && nm.MsgType == "system" {
		p.log.DebugContext(ctx, "inbound: dropped content-less system message",
			slog.String("session", sessionID))
		return nil
	}

	if err := p.enrichMentions(ctx, &envelope, nm); err != nil {
		return fmt.Errorf("inbound mention enrichment: %w", err)
	}

	if err := p.enrichQuote(ctx, &envelope, nm); err != nil {
		return fmt.Errorf("inbound quote enrichment: %w", err)
	}

	// Stage 4: persist (chats, messages, poll votes, receipt status).
	if err := p.persist(ctx, nm); err != nil {
		return fmt.Errorf("inbound persist: %w", err)
	}

	// Stage 5: auto-read (BEFORE fan-out, §7.5).
	p.autoRead(ctx, nm)

	// Stage 6: fan-out (sink + webhooks + event_log).
	if err := p.fanout(ctx, envelope); err != nil {
		return fmt.Errorf("inbound fanout: %w", err)
	}
	return nil
}

// now returns the pipeline's notion of current epoch-ms, via the injected Clock.
func (p *Pipeline) now() int64 { return p.clock.NowMs() }

// SystemClock is the production Clock backed by domain.NowMs.
type SystemClock struct{}

// NowMs returns the current epoch-ms time.
func (SystemClock) NowMs() int64 { return domain.NowMs() }

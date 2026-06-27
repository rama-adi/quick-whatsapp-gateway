package inbound

import (
	"context"
	"log/slog"
)

// presenceComposing is the chat-presence state sent when presence_typing is on.
const presenceComposing = "composing"

// autoRead is stage 5 (§7.5): if the session has auto_read enabled, send a read
// receipt for the inbound message BEFORE fan-out, so consumers that reply on the
// fanned event never leave the chat stuck-unread on WhatsApp. Optionally sends a
// "composing" presence when presence_typing is enabled.
//
// It is best-effort: WA-client errors are logged, not returned, because auto-read
// is a side-channel courtesy and must never block persistence or fan-out.
//
// Only real inbound, non-echo messages are read; receipts/poll-votes/edits/other
// events and our own echoes are skipped.
func (p *Pipeline) autoRead(ctx context.Context, nm *NormalizedMessage) {
	if !shouldAutoRead(nm) {
		return
	}
	cfg, ok := p.resolveSessionConfig(nm.SessionID)
	if !ok || !cfg.AutoRead {
		return
	}

	// WhatsApp routes a group read receipt by participant, so the sender JID is
	// required. For LID-only senders (the norm post-LID-migration) SenderJID is
	// empty, so fall back to the LID — sending with an empty sender makes WA drop
	// the receipt and the chat stays unread.
	sender := nm.SenderJID
	if sender == "" {
		sender = nm.SenderLID
	}

	if err := p.wa.SendReadReceipt(ctx, nm.SessionID, nm.ChatJID, sender, []string{nm.WAMessageID}); err != nil {
		p.log.WarnContext(ctx, "auto-read: send read receipt failed",
			slog.String("session", nm.SessionID),
			slog.String("chat", nm.ChatJID),
			slog.Any("err", err))
		return
	}

	// Optional "composing" presence — a human-mimicry hint before a reply.
	if cfg.PresenceTyping {
		if err := p.wa.SendPresence(ctx, nm.SessionID, nm.ChatJID, presenceComposing); err != nil {
			p.log.WarnContext(ctx, "auto-read: send presence failed",
				slog.String("session", nm.SessionID),
				slog.String("chat", nm.ChatJID),
				slog.Any("err", err))
		}
	}
}

// shouldAutoRead reports whether this event is a real inbound message worth a
// read receipt.
func shouldAutoRead(nm *NormalizedMessage) bool {
	if nm.Kind != KindMessage {
		return false
	}
	if nm.FromMe {
		return false
	}
	return nm.WAMessageID != "" && nm.ChatJID != ""
}

// resolveSessionConfig looks up per-session inbound behavior. When no resolver is
// injected, auto-read is treated as disabled (safe default).
func (p *Pipeline) resolveSessionConfig(sessionID string) (SessionConfig, bool) {
	if p.sessionConfig == nil {
		return SessionConfig{}, false
	}
	return p.sessionConfig(sessionID)
}

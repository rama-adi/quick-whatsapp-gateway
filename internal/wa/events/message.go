package events

import (
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// normalizeMessage handles *events.Message, the richest event. It detects the
// sub-type (reaction / edit / revoke / poll-vote / location / contact / poll /
// media / text), extracts every field the persistence + capture layers need into
// a NormalizedMessage, and maps the sub-type onto the right catalog event.
//
// e.Message is the unwrapped content (the lib already strips Ephemeral/ViewOnce/
// DeviceSent/Edited wrappers), so we read from it directly per recon §5.
func normalizeMessage(e *events.Message, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	info := e.Info
	// Canonicalize sender addresses to non-AD form (drop the ":device" / agent
	// part) so the same human maps to ONE identity key — the device suffix was a
	// primary source of duplicate identity rows and broken sender_lid joins.
	nm := &NormalizedMessage{
		WAMessageID: info.ID,
		ChatJID:     jidString(info.Chat),
		ChatClass:   ClassifyChat(info.Chat),
		SenderJID:   jidString(info.Sender.ToNonAD()),
		SenderLID:   jidString(info.SenderAlt.ToNonAD()), // LID alt-address when AddressingMode is PN, or vice-versa
		FromMe:      info.IsFromMe,
		PushName:    info.PushName,
		Timestamp:   msFromTime(info.Timestamp),
	}
	// SenderAlt may be the PN form (not the LID); only treat it as a LID when it
	// actually lives on the LID server, so SenderLID stays meaningful.
	if !info.SenderAlt.IsEmpty() && info.SenderAlt.Server != types.HiddenUserServer {
		nm.SenderLID = ""
	}

	msg := e.Message
	classify(e, msg, nm)

	eventType, kind := catalogForSubtype(nm.Subtype, nm.FromMe)
	payload := messagePayload(nm)
	ev := domain.NewEvent(eventType, sessionID, organizationID, payload)
	pr := PersistResult{
		Kind:    kind,
		Message: nm,
		ChatJID: nm.ChatJID,
	}
	return ev, pr, true
}

// classify inspects the unwrapped *waE2E.Message and fills the sub-type-specific
// fields of nm. Order matters: protocol (edit/revoke), reaction and poll-vote are
// checked before the content sub-types because they are control messages.
func classify(e *events.Message, msg *waE2E.Message, nm *NormalizedMessage) {
	switch {
	case msg.GetReactionMessage() != nil:
		fillReaction(msg.GetReactionMessage(), nm)
	case msg.GetProtocolMessage() != nil && isRevoke(msg.GetProtocolMessage()):
		fillRevoke(msg.GetProtocolMessage(), nm)
	case e.IsEdit || (msg.GetProtocolMessage() != nil && isEdit(msg.GetProtocolMessage())):
		fillEdit(e, msg, nm)
	case msg.GetPollUpdateMessage() != nil:
		fillPollVote(msg.GetPollUpdateMessage(), nm)
	case pollCreation(msg) != nil:
		fillPoll(pollCreation(msg), nm)
	case msg.GetLocationMessage() != nil:
		fillLocation(msg.GetLocationMessage(), nm)
	case msg.GetContactMessage() != nil:
		fillContact(msg.GetContactMessage(), nm)
	case hasMediaMessage(msg):
		fillMedia(msg, nm)
	case msg.GetConversation() != "" || msg.GetExtendedTextMessage() != nil:
		fillText(msg, nm)
	default:
		nm.Subtype = SubtypeUnknown
		nm.MessageType = "system"
	}
}

func isRevoke(p *waE2E.ProtocolMessage) bool {
	return p.GetType() == waE2E.ProtocolMessage_REVOKE
}

func isEdit(p *waE2E.ProtocolMessage) bool {
	return p.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT
}

func fillReaction(r *waE2E.ReactionMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypeReaction
	nm.MessageType = "reaction"
	nm.Reaction = r.GetText()
	nm.TargetMessageID = keyID(r.GetKey())
}

func fillRevoke(p *waE2E.ProtocolMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypeRevoke
	nm.MessageType = "revoke"
	nm.TargetMessageID = keyID(p.GetKey())
}

func fillEdit(e *events.Message, msg *waE2E.Message, nm *NormalizedMessage) {
	nm.Subtype = SubtypeEdit
	nm.MessageType = "text"
	// The edited target id is on the ProtocolMessage key; the new content is the
	// already-unwrapped message body (lib unwraps EditedMessage when IsEdit).
	if p := msg.GetProtocolMessage(); p != nil {
		nm.TargetMessageID = keyID(p.GetKey())
		if edited := p.GetEditedMessage(); edited != nil {
			nm.Body = textOf(edited)
			applyContext(contextOf(edited), nm)
			return
		}
	}
	nm.Body = textOf(msg)
	applyContext(contextOf(msg), nm)
}

func fillPollVote(pu *waE2E.PollUpdateMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypePollVote
	nm.MessageType = "poll_vote"
	// The vote payload is encrypted; expose the target poll id. Actual selected
	// options are decrypted later via cli.DecryptPollVote (§7 persist stage).
	nm.PollVoteTargetID = keyID(pu.GetPollCreationMessageKey())
}

// pollCreation returns the poll-creation content of a message regardless of which
// versioned field carries it. WhatsApp has revised the poll wire field several
// times: the original PollCreationMessage (field 49) is now legacy, and current
// clients send PollCreationMessageV2 (60) / V3 (64). Only checking the legacy
// field is why modern polls were classified as an unknown "system" message and
// dropped. (V4+ wrap the poll in a FutureProofMessage and are not handled yet.)
func pollCreation(msg *waE2E.Message) *waE2E.PollCreationMessage {
	switch {
	case msg.GetPollCreationMessage() != nil:
		return msg.GetPollCreationMessage()
	case msg.GetPollCreationMessageV2() != nil:
		return msg.GetPollCreationMessageV2()
	case msg.GetPollCreationMessageV3() != nil:
		return msg.GetPollCreationMessageV3()
	default:
		return nil
	}
}

func fillPoll(pc *waE2E.PollCreationMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypePoll
	nm.MessageType = "poll"
	opts := pc.GetOptions()
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		names = append(names, o.GetOptionName())
	}
	nm.Poll = &PollData{
		Name:            pc.GetName(),
		Options:         names,
		SelectableCount: int(pc.GetSelectableOptionsCount()),
	}
	nm.Body = pc.GetName()
	applyContext(pc.GetContextInfo(), nm)
}

func fillLocation(l *waE2E.LocationMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypeLocation
	nm.MessageType = "location"
	nm.Location = &LocationData{
		Latitude:  l.GetDegreesLatitude(),
		Longitude: l.GetDegreesLongitude(),
		Name:      l.GetName(),
		Address:   l.GetAddress(),
	}
	applyContext(l.GetContextInfo(), nm)
}

func fillContact(c *waE2E.ContactMessage, nm *NormalizedMessage) {
	nm.Subtype = SubtypeContact
	nm.MessageType = "contact"
	nm.Contact = &ContactData{
		DisplayName: c.GetDisplayName(),
		VCard:       c.GetVcard(),
	}
	nm.Body = c.GetDisplayName()
	applyContext(c.GetContextInfo(), nm)
}

func fillText(msg *waE2E.Message, nm *NormalizedMessage) {
	nm.Subtype = SubtypeText
	nm.MessageType = "text"
	nm.Body = textOf(msg)
	applyContext(contextOf(msg), nm)
}

// fillMedia extracts media METADATA only — never downloads (§9). MessageType is
// the specific media kind so the messages.type column reflects it.
func fillMedia(msg *waE2E.Message, nm *NormalizedMessage) {
	nm.Subtype = SubtypeMedia
	nm.HasMedia = true
	switch {
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		nm.MessageType = "image"
		nm.Body = m.GetCaption()
		nm.MediaInfo = &MediaMeta{Mimetype: m.GetMimetype(), Size: int64(m.GetFileLength())}
		applyContext(m.GetContextInfo(), nm)
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		nm.MessageType = "video"
		nm.Body = m.GetCaption()
		nm.MediaInfo = &MediaMeta{Mimetype: m.GetMimetype(), Size: int64(m.GetFileLength())}
		applyContext(m.GetContextInfo(), nm)
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		nm.MessageType = "audio"
		nm.MediaInfo = &MediaMeta{Mimetype: m.GetMimetype(), Size: int64(m.GetFileLength())}
		applyContext(m.GetContextInfo(), nm)
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		nm.MessageType = "document"
		nm.Body = m.GetCaption()
		nm.MediaInfo = &MediaMeta{Mimetype: m.GetMimetype(), Size: int64(m.GetFileLength()), Filename: m.GetFileName()}
		applyContext(m.GetContextInfo(), nm)
	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		nm.MessageType = "sticker"
		nm.MediaInfo = &MediaMeta{Mimetype: m.GetMimetype(), Size: int64(m.GetFileLength())}
		applyContext(m.GetContextInfo(), nm)
	}
}

func hasMediaMessage(msg *waE2E.Message) bool {
	return msg.GetImageMessage() != nil ||
		msg.GetVideoMessage() != nil ||
		msg.GetAudioMessage() != nil ||
		msg.GetDocumentMessage() != nil ||
		msg.GetStickerMessage() != nil
}

// textOf returns the plaintext of a message, preferring Conversation and falling
// back to ExtendedTextMessage (recon §5).
func textOf(msg *waE2E.Message) string {
	if t := msg.GetConversation(); t != "" {
		return t
	}
	return msg.GetExtendedTextMessage().GetText()
}

// contextOf returns the ContextInfo carried by a text message (quoted/mentions).
func contextOf(msg *waE2E.Message) *waE2E.ContextInfo {
	return msg.GetExtendedTextMessage().GetContextInfo()
}

// applyContext copies reply + mention metadata off a ContextInfo onto nm.
func applyContext(ctx *waE2E.ContextInfo, nm *NormalizedMessage) {
	if ctx == nil {
		return
	}
	if id := ctx.GetStanzaID(); id != "" {
		nm.QuotedMessageID = id
	}
	if m := ctx.GetMentionedJID(); len(m) > 0 {
		nm.Mentions = canonicalMentions(m)
	}
}

// canonicalMentions strips the ":device" suffix off each mentioned JID — the same
// non-AD canonicalization applied to sender_lid — so stored mentions are
// consistent and line up with the canonical identity key. WhatsApp may mention a
// person by LID (@lid) or phone JID (@s.whatsapp.net) depending on the chat's
// addressing mode; both forms are preserved (only the device part is dropped), so
// the read-time resolver still matches on lid OR phone_jid. Unparseable entries
// are kept verbatim.
func canonicalMentions(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		j, err := types.ParseJID(s)
		if err != nil {
			out = append(out, s)
			continue
		}
		out = append(out, jidString(j.ToNonAD()))
	}
	return out
}

func keyID(k *waCommon.MessageKey) string {
	return k.GetID()
}

// catalogForSubtype maps a message sub-type onto its catalog event type and the
// PersistKind the pipeline should run. Plain messages split into "message" vs
// "message.from_me" (§9) based on FromMe.
func catalogForSubtype(st MessageSubtype, fromMe bool) (string, PersistKind) {
	switch st {
	case SubtypeReaction:
		return domain.EventMessageReaction, PersistMessageReaction
	case SubtypeEdit:
		return domain.EventMessageEdited, PersistMessageEdit
	case SubtypeRevoke:
		return domain.EventMessageRevoked, PersistMessageRevoke
	case SubtypePollVote:
		return domain.EventPollVote, PersistPollVote
	default:
		// text/media/location/contact/poll/unknown are all "message" rows.
		if fromMe {
			return domain.EventMessageFromMe, PersistMessage
		}
		return domain.EventMessage, PersistMessage
	}
}

// messagePayload projects a NormalizedMessage into the wire payload. Media is
// kept null on the wire (§9) — HasMedia signals presence; the parsed MediaInfo
// stays on PersistResult.Message for the persistence layer's media_meta column.
func messagePayload(nm *NormalizedMessage) MessagePayload {
	return MessagePayload{
		WAMessageID:     nm.WAMessageID,
		ChatJID:         nm.ChatJID,
		SenderJID:       nm.SenderJID,
		SenderLID:       nm.SenderLID,
		FromMe:          nm.FromMe,
		Type:            nm.MessageType,
		Body:            nm.Body,
		QuotedMessageID: nm.QuotedMessageID,
		Mentions:        nm.Mentions,
		HasMedia:        nm.HasMedia,
		Media:           nil, // always null in v1
		Timestamp:       nm.Timestamp,
		PushName:        nm.PushName,
		Reaction:        nm.Reaction,
		TargetID:        targetID(nm),
		Location:        nm.Location,
		Contact:         nm.Contact,
		Poll:            nm.Poll,
		SelectedOptions: nm.SelectedOptions,
	}
}

// targetID returns the acted-on message id for reaction/edit/revoke/poll-vote.
func targetID(nm *NormalizedMessage) string {
	if nm.PollVoteTargetID != "" {
		return nm.PollVoteTargetID
	}
	return nm.TargetMessageID
}

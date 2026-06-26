package events

import (
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Normalize translates a raw whatsmeow event into (a) a versioned domain.Event
// envelope carrying a wire-safe, camelCase payload (§9 catalog) and (b) a
// PersistResult — the structured, parsed view the rest of the inbound pipeline
// (capture / persist / fan-out, §7) consumes. The third return is ok: false when
// the event type carries nothing the catalog represents (the caller drops it).
//
// Raw protobufs are NEVER placed in the payload: every protobuf is parsed into
// plain Go fields here so consumers and the wire format never see waE2E types.
//
// Media is always metadata-only in v1 — HasMedia is set with mimetype/size/
// filename, but the media body is never downloaded and Media stays null on the
// wire (§9).
func Normalize(evt any, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	switch e := evt.(type) {
	case *events.Message:
		return normalizeMessage(e, sessionID, organizationID)
	case *events.Receipt:
		return normalizeReceipt(e, sessionID, organizationID)
	case *events.Connected:
		return sessionStatus(domain.SessionWorking, sessionID, organizationID)
	case *events.Disconnected:
		// A plain disconnect is transient (the manager reconnects); report it as a
		// status change so dashboards can reflect it, but it's not terminal.
		return sessionStatus(domain.SessionStarting, sessionID, organizationID)
	case *events.LoggedOut:
		return sessionStatus(domain.SessionLoggedOut, sessionID, organizationID)
	case *events.StreamReplaced:
		return sessionStatus(domain.SessionFailed, sessionID, organizationID)
	case *events.QR:
		return normalizeQR(e, sessionID, organizationID)
	case *events.PairSuccess:
		return normalizePairSuccess(e, sessionID, organizationID)
	case *events.Presence:
		return normalizePresence(e, sessionID, organizationID)
	case *events.ChatPresence:
		return normalizeChatPresence(e, sessionID, organizationID)
	case *events.GroupInfo:
		return normalizeGroupInfo(e, sessionID, organizationID)
	case *events.JoinedGroup:
		return normalizeJoinedGroup(e, sessionID, organizationID)
	case *events.Picture:
		return normalizePicture(e, sessionID, organizationID)
	case *events.Contact:
		return normalizeContact(e, sessionID, organizationID)
	case *events.PushName:
		return normalizePushName(e, sessionID, organizationID)
	case *events.CallOffer:
		return normalizeCallOffer(e, sessionID, organizationID)
	case *events.NewsletterJoin:
		return normalizeNewsletter(e.ID, "join", sessionID, organizationID)
	case *events.NewsletterLeave:
		return normalizeNewsletter(e.ID, "leave", sessionID, organizationID)
	case *events.NewsletterMuteChange:
		return normalizeNewsletter(e.ID, "mute", sessionID, organizationID)
	default:
		return domain.Event{}, PersistResult{}, false
	}
}

// sessionStatus is the shared builder for the session.status events.
func sessionStatus(status domain.SessionStatus, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := SessionStatusPayload{Status: string(status)}
	ev := domain.NewEvent(domain.EventSessionStatus, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistSessionStatus, SessionStatus: status}, true
}

func normalizeQR(e *events.QR, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	var code string
	if len(e.Codes) > 0 {
		code = e.Codes[0]
	}
	payload := AuthQRPayload{Code: code}
	ev := domain.NewEvent(domain.EventAuthQR, sessionID, organizationID, payload)
	// QR/pair are transient auth signals, not persisted rows.
	return ev, PersistResult{Kind: PersistNone}, true
}

func normalizePairSuccess(e *events.PairSuccess, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := AuthCodePayload{
		JID:          jidString(e.ID),
		LID:          jidString(e.LID),
		BusinessName: e.BusinessName,
		Platform:     e.Platform,
	}
	ev := domain.NewEvent(domain.EventAuthCode, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistNone}, true
}

func normalizeReceipt(e *events.Receipt, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	status, ok := receiptStatus(e.Type)
	if !ok {
		// Non-status receipts (sender/retry/server-error/etc.) don't update the
		// message lifecycle we expose; drop them.
		return domain.Event{}, PersistResult{}, false
	}
	payload := MessageStatusPayload{
		ChatJID:    jidString(e.Chat),
		SenderJID:  jidString(e.Sender),
		MessageIDs: append([]string(nil), e.MessageIDs...),
		Status:     string(status),
		Timestamp:  msFromTime(e.Timestamp),
	}
	ev := domain.NewEvent(domain.EventMessageStatus, sessionID, organizationID, payload)
	pr := PersistResult{
		Kind:          PersistMessageStatus,
		ChatJID:       payload.ChatJID,
		MessageStatus: status,
		MessageIDs:    payload.MessageIDs,
	}
	return ev, pr, true
}

// receiptStatus maps whatsmeow receipt types onto the domain message status we
// persist. Returns ok=false for receipt types that don't correspond to a
// lifecycle status update.
func receiptStatus(t types.ReceiptType) (domain.MessageStatus, bool) {
	switch t {
	case types.ReceiptTypeDelivered:
		return domain.MessageDelivered, true
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		return domain.MessageRead, true
	case types.ReceiptTypePlayed, types.ReceiptTypePlayedSelf:
		return domain.MessagePlayed, true
	default:
		return "", false
	}
}

func normalizePresence(e *events.Presence, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	state := "available"
	if e.Unavailable {
		state = "unavailable"
	}
	payload := PresencePayload{
		From:        jidString(e.From),
		State:       state,
		Unavailable: e.Unavailable,
		LastSeen:    msFromTime(e.LastSeen),
	}
	ev := domain.NewEvent(domain.EventPresenceUpdate, sessionID, organizationID, payload)
	// Presence is ephemeral; not a persisted row.
	return ev, PersistResult{Kind: PersistNone}, true
}

func normalizeChatPresence(e *events.ChatPresence, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := PresencePayload{
		ChatJID: jidString(e.Chat),
		From:    jidString(e.Sender),
		State:   string(e.State),
		Media:   string(e.Media),
	}
	ev := domain.NewEvent(domain.EventPresenceUpdate, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistNone}, true
}

func normalizeGroupInfo(e *events.GroupInfo, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := groupPayloadFromInfo(e)
	// A participant delta (join/leave/promote/demote) is a group.participant
	// event; pure metadata changes are group.update.
	if len(e.Join)+len(e.Leave)+len(e.Promote)+len(e.Demote) > 0 {
		ev := domain.NewEvent(domain.EventGroupParticipant, sessionID, organizationID, payload)
		return ev, PersistResult{Kind: PersistGroupParticipant, ChatJID: payload.GroupJID}, true
	}
	ev := domain.NewEvent(domain.EventGroupUpdate, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistGroupUpdate, ChatJID: payload.GroupJID}, true
}

func normalizeJoinedGroup(e *events.JoinedGroup, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	// JoinedGroup embeds types.GroupInfo (not the events.GroupInfo struct), so
	// build the payload from the embedded metadata.
	payload := GroupPayload{
		GroupJID: jidString(e.JID),
		Reason:   e.Reason,
	}
	if e.Name != "" {
		payload.Subject = e.Name
	}
	ev := domain.NewEvent(domain.EventGroupUpdate, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistGroupUpdate, ChatJID: payload.GroupJID}, true
}

func normalizePicture(e *events.Picture, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := ChatUpdatePayload{
		ChatJID:   jidString(e.JID),
		Change:    "picture",
		PictureID: e.PictureID,
		Removed:   e.Remove,
	}
	ev := domain.NewEvent(domain.EventChatUpdate, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistNone, ChatJID: payload.ChatJID}, true
}

func normalizeContact(e *events.Contact, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := ContactUpdatePayload{JID: jidString(e.JID)}
	if a := e.Action; a != nil {
		payload.FullName = a.GetFullName()
		payload.FirstName = a.GetFirstName()
	}
	ev := domain.NewEvent(domain.EventContactUpdate, sessionID, organizationID, payload)
	pr := PersistResult{Kind: PersistContactUpdate, ContactJID: payload.JID, ContactName: payload.FullName}
	return ev, pr, true
}

func normalizePushName(e *events.PushName, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := ContactUpdatePayload{
		JID:      jidString(e.JID),
		PushName: e.NewPushName,
	}
	ev := domain.NewEvent(domain.EventContactUpdate, sessionID, organizationID, payload)
	pr := PersistResult{Kind: PersistContactUpdate, ContactJID: payload.JID, PushName: payload.PushName}
	return ev, pr, true
}

func normalizeCallOffer(e *events.CallOffer, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := CallPayload{
		CallID:    e.CallID,
		From:      jidString(e.From),
		Timestamp: msFromTime(e.Timestamp),
		IsGroup:   !e.GroupJID.IsEmpty(),
		GroupJID:  jidString(e.GroupJID),
	}
	ev := domain.NewEvent(domain.EventCallIncoming, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistNone}, true
}

func normalizeNewsletter(id types.JID, action, sessionID, organizationID string) (domain.Event, PersistResult, bool) {
	payload := NewsletterPayload{JID: jidString(id), Action: action}
	ev := domain.NewEvent(domain.EventNewsletterUpdate, sessionID, organizationID, payload)
	return ev, PersistResult{Kind: PersistNone, ChatJID: payload.JID}, true
}

// groupPayloadFromInfo flattens an events.GroupInfo into the wire payload,
// converting every nested protobuf wrapper into plain fields.
func groupPayloadFromInfo(e *events.GroupInfo) GroupPayload {
	p := GroupPayload{
		GroupJID:  jidString(e.JID),
		Timestamp: msFromTime(e.Timestamp),
		Join:      jidStrings(e.Join),
		Leave:     jidStrings(e.Leave),
		Promote:   jidStrings(e.Promote),
		Demote:    jidStrings(e.Demote),
	}
	if e.Sender != nil {
		p.Sender = jidString(*e.Sender)
	}
	if e.Name != nil {
		p.Subject = e.Name.Name
	}
	if e.Topic != nil {
		p.Description = e.Topic.Topic
	}
	if e.Announce != nil {
		p.IsAnnounce = &e.Announce.IsAnnounce
	}
	if e.Locked != nil {
		p.IsLocked = &e.Locked.IsLocked
	}
	if e.NewInviteLink != nil {
		p.NewInviteLink = *e.NewInviteLink
	}
	return p
}

// --- shared helpers ---

// jidString renders a JID, returning "" for the empty JID rather than the bare
// "@server" form ParseJID/String would otherwise produce.
func jidString(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.String()
}

func jidStrings(js []types.JID) []string {
	if len(js) == 0 {
		return nil
	}
	out := make([]string, 0, len(js))
	for _, j := range js {
		out = append(out, jidString(j))
	}
	return out
}

// msFromTime converts a time.Time to epoch-ms, returning 0 for the zero time so
// "unknown" round-trips as 0 instead of a nonsense large negative value.
func msFromTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

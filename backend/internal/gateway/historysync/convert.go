// Package historysync turns WhatsApp history-sync notifications into
// history.batch and history.sync events. Convert is the pure part: it maps
// one downloaded history blob to deterministic, size-bounded batches. The
// worker (worker.go) owns download, pacing, journaling and run lifecycle.
package historysync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/events"
)

// Batch limits. A batch carries at most MaxBatchMessages messages and its
// payload JSON stays within MaxBatchBytes, which leaves room for the event
// envelope under the 96 KiB webhook body budget. A single message larger than
// that travels alone; MaxMessageBytes is the hard cap for one message (its
// body and quoted body are truncated beyond it), chosen so the batch still
// fits the gateway journal's 1 MiB control batch after protobuf encoding.
var (
	MaxBatchMessages = 200
	MaxBatchBytes    = 96*1024 - 2*1024
	MaxMessageBytes  = 768 * 1024
)

// Parser parses one history message into the live event shape. Production
// uses whatsmeow's Client.ParseWebMessage, which needs the account's own JID.
type Parser func(chat types.JID, msg *waWeb.WebMessageInfo) (*waevents.Message, error)

// Input is one downloaded history notification.
type Input struct {
	SessionID      string
	NotificationID string    // stanza id of the history-sync protocol message
	NotifiedAt     time.Time // when the phone sent the notification; seeds event ids
	RunID          string
	AccountJID     string // phone JID of the linked account (own messages' sender)
	CutoffMs       int64  // messages older than this are skipped; 0 keeps all
	Blob           *waHistorySync.HistorySync
	Parse          Parser
}

// Batch is one history.batch event: its deterministic id and payload.
type Batch struct {
	EventID string
	Payload apitypes.HistoryBatchPayload
}

// Result is the conversion of one notification.
type Result struct {
	SyncType      string
	ChunkOrder    int
	Progress      *int
	Batches       []Batch
	Conversations int
	Messages      int
	Skipped       int
}

// SyncTypeName maps a whatsmeow history sync type to the catalog name.
func SyncTypeName(t waHistorySync.HistorySync_HistorySyncType) string {
	return strings.ToLower(t.String())
}

// EventID returns a deterministic evt_ ULID: the time component is t and the
// entropy is derived from parts, so re-processing the same notification after
// a crash yields the same ids and the journal deduplicates them.
func EventID(t time.Time, parts ...string) string {
	return domain.PrefixEvent + deterministicULID(t, parts...)
}

// RunID returns the deterministic id of a run that starts with the given
// notification.
func RunID(t time.Time, sessionID, notificationID string) string {
	return "hsr_" + deterministicULID(t, sessionID, notificationID, "run")
}

func deterministicULID(t time.Time, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	if t.IsZero() || t.Before(time.Unix(0, 0)) {
		t = time.Unix(0, 0)
	}
	return ulid.MustNew(ulid.Timestamp(t), bytes.NewReader(sum[:])).String()
}

// Convert maps a history blob to batches. It is deterministic: the same input
// always yields the same batches, parts and event ids.
func Convert(in Input) Result {
	blob := in.Blob
	res := Result{
		SyncType:   SyncTypeName(blob.GetSyncType()),
		ChunkOrder: int(blob.GetChunkOrder()),
	}
	if blob.Progress != nil {
		p := int(min(blob.GetProgress(), 100))
		res.Progress = &p
	}
	ids := newIdentityIndex(blob)
	contacts := buildContacts(blob, ids)
	// Status updates are not chat history.
	res.Skipped += len(blob.GetStatusV3Messages())

	var convs []convItem
	for _, conv := range blob.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || chat.IsEmpty() || chat.User == "" {
			res.Skipped += len(conv.GetMessages())
			continue
		}
		chat = chat.ToNonAD()
		class := events.ClassifyChat(chat)
		if class == events.ChatClassStatus {
			res.Skipped += len(conv.GetMessages())
			continue
		}
		item := convItem{meta: conversationMeta(conv, chat, class, ids)}
		var skipped int
		item.messages, skipped = convertMessages(in, chat, conv.GetMessages(), ids)
		res.Skipped += skipped
		res.Messages += len(item.messages)
		convs = append(convs, item)
	}
	res.Conversations = len(convs)

	payloads := pack(contacts, convs)
	snapshot := in.NotifiedAt.UnixMilli()
	for i := range payloads {
		p := &payloads[i]
		p.RunID = in.RunID
		p.AccountJID = in.AccountJID
		p.SyncType = res.SyncType
		p.ChunkOrder = res.ChunkOrder
		p.Part = i + 1
		p.Parts = len(payloads)
		p.SnapshotAt = snapshot
		res.Batches = append(res.Batches, Batch{
			EventID: EventID(in.NotifiedAt, in.SessionID, in.NotificationID, strconv.Itoa(i)),
			Payload: *p,
		})
	}
	return res
}

type convItem struct {
	meta     apitypes.HistoryConversation
	messages []apitypes.HistoryMessage
}

// --- identities ---

// identityIndex maps phone JIDs and LIDs of the same person, learned from the
// blob itself (WhatsApp's mapping list, inline contacts and conversation
// metadata).
type identityIndex struct {
	pnToLID map[string]string
	lidToPN map[string]string
}

func newIdentityIndex(blob *waHistorySync.HistorySync) identityIndex {
	idx := identityIndex{pnToLID: map[string]string{}, lidToPN: map[string]string{}}
	for _, m := range blob.GetPhoneNumberToLidMappings() {
		idx.add(m.GetPnJID(), m.GetLidJID())
	}
	for _, c := range blob.GetInlineContacts() {
		idx.add(c.GetPnJID(), c.GetLidJID())
	}
	for _, conv := range blob.GetConversations() {
		id := canonicalJID(conv.GetID())
		switch {
		case strings.HasSuffix(id, "@lid"):
			idx.add(conv.GetPnJID(), id)
		case strings.HasSuffix(id, "@"+types.DefaultUserServer):
			idx.add(id, conv.GetLidJID())
		}
	}
	return idx
}

func (x identityIndex) add(pn, lid string) {
	pn, lid = canonicalJID(pn), canonicalJID(lid)
	if !strings.HasSuffix(pn, "@"+types.DefaultUserServer) || !strings.HasSuffix(lid, "@lid") {
		return
	}
	if _, ok := x.pnToLID[pn]; !ok {
		x.pnToLID[pn] = lid
	}
	if _, ok := x.lidToPN[lid]; !ok {
		x.lidToPN[lid] = pn
	}
}

// split returns (lid, phoneJID) for a JID of either kind, filling the other
// side from the index.
func (x identityIndex) split(jid string) (lid, pn string) {
	jid = canonicalJID(jid)
	switch {
	case strings.HasSuffix(jid, "@lid"):
		return jid, x.lidToPN[jid]
	case strings.HasSuffix(jid, "@"+types.DefaultUserServer):
		return x.pnToLID[jid], jid
	}
	return "", ""
}

func canonicalJID(s string) string {
	if s == "" {
		return ""
	}
	j, err := types.ParseJID(s)
	if err != nil || j.User == "" {
		return ""
	}
	return j.ToNonAD().String()
}

func buildContacts(blob *waHistorySync.HistorySync, ids identityIndex) []apitypes.HistoryContact {
	byKey := map[string]*apitypes.HistoryContact{}
	get := func(jid string) *apitypes.HistoryContact {
		lid, pn := ids.split(jid)
		if lid == "" && pn == "" {
			return nil
		}
		key := lid
		if key == "" {
			key = pn
		}
		c := byKey[key]
		if c == nil {
			c = &apitypes.HistoryContact{LID: lid, PhoneJID: pn}
			byKey[key] = c
		}
		return c
	}
	for pn := range ids.pnToLID {
		get(pn)
	}
	for lid := range ids.lidToPN {
		get(lid)
	}
	for _, p := range blob.GetPushnames() {
		name := strings.TrimSpace(p.GetPushname())
		if name == "" {
			continue
		}
		if c := get(p.GetID()); c != nil && c.PushName == "" {
			c.PushName = name
		}
	}
	out := make([]apitypes.HistoryContact, 0, len(byKey))
	for _, c := range byKey {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LID != out[j].LID {
			return out[i].LID < out[j].LID
		}
		return out[i].PhoneJID < out[j].PhoneJID
	})
	return out
}

// --- conversations ---

func conversationMeta(conv *waHistorySync.Conversation, chat types.JID, class events.ChatClass,
	ids identityIndex) apitypes.HistoryConversation {
	meta := apitypes.HistoryConversation{
		ChatJID:     chat.String(),
		Type:        string(chatType(class)),
		Name:        strings.TrimSpace(conv.GetName()),
		Description: strings.TrimSpace(conv.GetDescription()),
		Archived:    conv.GetArchived(),
		Pinned:      conv.GetPinned() > 0,
		Messages:    []apitypes.HistoryMessage{},
	}
	if meta.Name == "" {
		meta.Name = strings.TrimSpace(conv.GetDisplayName())
	}
	if class == events.ChatClassDM {
		meta.LID, meta.PhoneJID = ids.split(meta.ChatJID)
	}
	if conv.UnreadCount != nil {
		n := int(conv.GetUnreadCount())
		meta.UnreadCount = &n
	}
	if v := conv.GetMuteEndTime(); v > 0 {
		meta.MutedUntil = epochMs(v)
	}
	if v := conv.GetLastMsgTimestamp(); v > 0 {
		meta.LastMessageAt = epochMs(v)
	} else if v := conv.GetConversationTimestamp(); v > 0 {
		meta.LastMessageAt = epochMs(v)
	}
	for _, p := range conv.GetParticipant() {
		lid, pn := ids.split(p.GetUserJID())
		if lid == "" && pn == "" {
			continue
		}
		meta.Participants = append(meta.Participants, apitypes.HistoryParticipant{
			LID: lid, PhoneJID: pn, Role: participantRole(p.GetRank()),
		})
	}
	return meta
}

// mutedForever is the epoch-ms stand-in for "muted indefinitely" (end of
// year 9999), used when the phone sends a sentinel instead of a time.
const mutedForever int64 = 253402300799000

// epochMs normalizes a WhatsApp timestamp that may be in seconds or
// milliseconds. Sentinels far in the future map to mutedForever.
func epochMs(v uint64) int64 {
	switch {
	case v >= uint64(mutedForever):
		return mutedForever
	case v < 1e12:
		return int64(v) * 1000
	default:
		return int64(v)
	}
}

func chatType(class events.ChatClass) domain.ChatType {
	switch class {
	case events.ChatClassGroup:
		return domain.ChatGroup
	case events.ChatClassNewsletter:
		return domain.ChatNewsletter
	case events.ChatClassBroadcast:
		return domain.ChatBroadcast
	case events.ChatClassStatus:
		return domain.ChatStatus
	default:
		return domain.ChatDM
	}
}

func participantRole(r waHistorySync.GroupParticipant_Rank) string {
	switch r {
	case waHistorySync.GroupParticipant_ADMIN:
		return string(domain.RoleAdmin)
	case waHistorySync.GroupParticipant_SUPERADMIN:
		return string(domain.RoleSuperAdmin)
	default:
		return string(domain.RoleMember)
	}
}

// --- messages ---

type orderedMessage struct {
	ts    int64
	order uint64
	index int
	msg   apitypes.HistoryMessage
}

func convertMessages(in Input, chat types.JID, msgs []*waHistorySync.HistorySyncMsg,
	ids identityIndex) ([]apitypes.HistoryMessage, int) {
	var out []orderedMessage
	skipped := 0
	for i, hsm := range msgs {
		converted, ok := convertMessage(in, chat, hsm.GetMessage(), ids)
		if !ok {
			skipped++
			continue
		}
		for j, m := range converted {
			out = append(out, orderedMessage{ts: m.Timestamp, order: hsm.GetMsgOrderID(), index: i<<8 | min(j, 255), msg: m})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].ts != out[b].ts {
			return out[a].ts < out[b].ts
		}
		if out[a].order != out[b].order {
			return out[a].order < out[b].order
		}
		return out[a].index < out[b].index
	})
	result := make([]apitypes.HistoryMessage, 0, len(out))
	for _, o := range out {
		if !fitMessage(&o.msg, MaxMessageBytes) {
			skipped++
			continue
		}
		result = append(result, o.msg)
	}
	return result, skipped
}

// convertMessage maps one history message to the live payload shape, plus
// the reactions and poll votes WhatsApp folds into it. ok=false means the
// message is skipped (system notice, stub, undecodable, or before the cutoff).
func convertMessage(in Input, chat types.JID, wm *waWeb.WebMessageInfo,
	ids identityIndex) ([]apitypes.HistoryMessage, bool) {
	key := wm.GetKey()
	if wm == nil || key.GetID() == "" {
		return nil, false
	}
	ts := int64(wm.GetMessageTimestamp()) * 1000
	if in.CutoffMs > 0 && ts < in.CutoffMs {
		return nil, false
	}
	stub := wm.GetMessageStubType()
	if stub != 0 && stub != waWeb.WebMessageInfo_REVOKE {
		return nil, false
	}
	if stub == 0 && wm.GetMessage() == nil {
		return nil, false
	}
	evt, err := in.Parse(chat, wm)
	if err != nil || evt == nil {
		return nil, false
	}
	if stub == waWeb.WebMessageInfo_REVOKE {
		// The phone replaced a deleted-for-everyone message with a stub that
		// keeps the original id.
		m := apitypes.HistoryMessage{Kind: domain.EventMessageRevoked}
		m.WAMessageID, m.TargetID = key.GetID(), key.GetID()
		m.ChatJID, m.FromMe, m.Type, m.Timestamp = chat.String(), key.GetFromMe(), "revoke", ts
		m.SenderJID, m.SenderLID = senderIDs(evt.Info.Sender, ids)
		return []apitypes.HistoryMessage{m}, true
	}
	restoreEdit(evt, wm)
	ev, pr, ok := events.Normalize(evt, in.SessionID, "")
	if !ok || pr.Message == nil || pr.Message.MessageType == "system" {
		return nil, false
	}
	payload, ok := ev.Payload.(apitypes.MessagePayload)
	if !ok {
		return nil, false
	}
	if payload.SenderJID == "" && payload.SenderLID == "" {
		payload.SenderJID, payload.SenderLID = senderIDs(evt.Info.Sender, ids)
	} else {
		payload.SenderJID, payload.SenderLID = fillPair(payload.SenderJID, payload.SenderLID, ids)
	}
	if nm := pr.Message; nm.HasMedia && nm.MediaInfo != nil {
		payload.Media = &domain.MediaMeta{Mimetype: nm.MediaInfo.Mimetype, Size: nm.MediaInfo.Size, Filename: nm.MediaInfo.Filename}
	}
	if len(pr.Message.Mentions) > 0 {
		payload.Mentions = make(map[string]apitypes.MentionData, len(pr.Message.Mentions))
		for _, jid := range pr.Message.Mentions {
			payload.Mentions[jid] = apitypes.MentionData{}
		}
	}
	main := apitypes.HistoryMessage{MessagePayload: payload, Kind: ev.Type}
	if payload.FromMe && createsRow(ev.Type) {
		main.Status = messageStatus(wm)
	}
	out := []apitypes.HistoryMessage{main}
	out = append(out, reactions(in, chat, wm, ids)...)
	if payload.Poll != nil {
		out = append(out, pollVotes(in, chat, wm, payload.Poll.Options, ids)...)
	}
	return out, true
}

// restoreEdit undoes ParseWebMessage's edit rewrite (it replaces the edit
// with the edited content under the original id), so the normalizer sees the
// MESSAGE_EDIT protocol message and maps it to message.edited with targetId.
func restoreEdit(evt *waevents.Message, wm *waWeb.WebMessageInfo) {
	raw := (&waevents.Message{RawMessage: wm.GetMessage()}).UnwrapRaw()
	if pm := raw.Message.GetProtocolMessage(); pm.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT {
		evt.Message = raw.Message
		evt.Info.ID = wm.GetKey().GetID()
	}
}

func createsRow(kind string) bool {
	return kind == domain.EventMessage || kind == domain.EventMessageFromMe || kind == domain.EventMessageInteractiveReply
}

func messageStatus(wm *waWeb.WebMessageInfo) string {
	if wm.Status == nil {
		return ""
	}
	switch wm.GetStatus() {
	case waWeb.WebMessageInfo_PENDING:
		return string(domain.MessagePending)
	case waWeb.WebMessageInfo_SERVER_ACK:
		return string(domain.MessageSent)
	case waWeb.WebMessageInfo_DELIVERY_ACK:
		return string(domain.MessageDelivered)
	case waWeb.WebMessageInfo_READ:
		return string(domain.MessageRead)
	case waWeb.WebMessageInfo_PLAYED:
		return string(domain.MessagePlayed)
	default:
		return ""
	}
}

// senderIDs splits a sender JID into (phone JID, LID) and fills the missing
// side from the blob's identity index.
func senderIDs(sender types.JID, ids identityIndex) (jid, lid string) {
	if sender.IsEmpty() {
		return "", ""
	}
	s := sender.ToNonAD().String()
	if sender.Server == types.HiddenUserServer {
		return fillPair("", s, ids)
	}
	return fillPair(s, "", ids)
}

func fillPair(jid, lid string, ids identityIndex) (string, string) {
	if lid == "" && jid != "" {
		lid = ids.pnToLID[jid]
	}
	if jid == "" && lid != "" {
		jid = ids.lidToPN[lid]
	}
	return jid, lid
}

// keySender resolves who sent a reaction or vote from its message key.
func keySender(in Input, chat types.JID, key *waCommon.MessageKey, ids identityIndex) (jid, lid string, fromMe bool) {
	if key.GetFromMe() {
		j, l := fillPair(canonicalJID(in.AccountJID), "", ids)
		return j, l, true
	}
	if p := key.GetParticipant(); p != "" {
		if parsed, err := types.ParseJID(p); err == nil {
			j, l := senderIDs(parsed, ids)
			return j, l, false
		}
	}
	if chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer {
		j, l := senderIDs(chat, ids)
		return j, l, false
	}
	return "", "", false
}

func reactions(in Input, chat types.JID, wm *waWeb.WebMessageInfo, ids identityIndex) []apitypes.HistoryMessage {
	var out []apitypes.HistoryMessage
	for _, r := range wm.GetReactions() {
		if r.GetText() == "" || r.GetKey().GetID() == "" {
			continue
		}
		ts := r.GetSenderTimestampMS()
		if ts <= 0 {
			ts = int64(wm.GetMessageTimestamp()) * 1000
		}
		if in.CutoffMs > 0 && ts < in.CutoffMs {
			continue
		}
		m := apitypes.HistoryMessage{Kind: domain.EventMessageReaction}
		m.WAMessageID, m.TargetID, m.Reaction = r.GetKey().GetID(), wm.GetKey().GetID(), r.GetText()
		m.ChatJID, m.Type, m.Timestamp = chat.String(), "reaction", ts
		m.SenderJID, m.SenderLID, m.FromMe = keySender(in, chat, r.GetKey(), ids)
		out = append(out, m)
	}
	return out
}

// pollVotes maps the decrypted votes WhatsApp folds into a poll message. The
// vote carries SHA-256 hashes of the selected option names.
func pollVotes(in Input, chat types.JID, wm *waWeb.WebMessageInfo, options []string,
	ids identityIndex) []apitypes.HistoryMessage {
	byHash := make(map[string]string, len(options))
	for _, opt := range options {
		sum := sha256.Sum256([]byte(opt))
		byHash[hex.EncodeToString(sum[:])] = opt
	}
	var out []apitypes.HistoryMessage
	for _, pu := range wm.GetPollUpdates() {
		key := pu.GetPollUpdateMessageKey()
		if key.GetID() == "" {
			continue
		}
		var selected []string
		for _, h := range pu.GetVote().GetSelectedOptions() {
			hash := hex.EncodeToString(h)
			if name, ok := byHash[hash]; ok {
				selected = append(selected, name)
			} else {
				selected = append(selected, hash)
			}
		}
		if len(selected) == 0 {
			continue
		}
		ts := pu.GetSenderTimestampMS()
		if ts <= 0 {
			ts = pu.GetServerTimestampMS()
		}
		if in.CutoffMs > 0 && ts < in.CutoffMs {
			continue
		}
		m := apitypes.HistoryMessage{Kind: domain.EventPollVote}
		m.WAMessageID, m.TargetID, m.SelectedOptions = key.GetID(), wm.GetKey().GetID(), selected
		m.ChatJID, m.Type, m.Timestamp = chat.String(), "poll_vote", ts
		m.SenderJID, m.SenderLID, m.FromMe = keySender(in, chat, key, ids)
		out = append(out, m)
	}
	return out
}

// fitMessage truncates a message's free-text fields until its JSON fits
// limit, flagging it as truncated. It returns false when even that is not
// enough (the message is then skipped).
func fitMessage(m *apitypes.HistoryMessage, limit int) bool {
	size := jsonLen(m)
	if size <= limit {
		return true
	}
	fields := []*string{&m.Body, &m.QuotedBody}
	if m.Contact != nil {
		contact := *m.Contact
		m.Contact = &contact
		fields = append(fields, &m.Contact.VCard)
	}
	for _, f := range fields {
		for size > limit && len(*f) > 0 {
			keep := len(*f) - (size - limit) - 16
			*f = truncateUTF8(*f, max(keep, 0))
			m.Truncated = true
			size = jsonLen(m)
		}
	}
	return size <= limit
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func jsonLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 1 << 30
	}
	return len(b)
}

// --- packing ---

// batchOverhead is the JSON of a batch payload without contacts or
// conversations, with generous room for ids and counters.
var batchOverhead = jsonLen(apitypes.HistoryBatchPayload{
	RunID: "hsr_01J9ZX3K8Q2W5V7B4N6M1C0D9E", AccountJID: "000000000000000@s.whatsapp.net",
	SyncType: "initial_bootstrap", ChunkOrder: 1 << 30, Part: 1 << 30, Parts: 1 << 30, SnapshotAt: 1 << 62,
}) + 64

type packer struct {
	out      []apitypes.HistoryBatchPayload
	cur      apitypes.HistoryBatchPayload
	size     int
	messages int
	convAt   map[string]int
}

func (p *packer) empty() bool { return len(p.cur.Contacts) == 0 && len(p.cur.Conversations) == 0 }

func (p *packer) flush() {
	if p.empty() {
		return
	}
	p.out = append(p.out, p.cur)
	p.cur = apitypes.HistoryBatchPayload{}
	p.size, p.messages = batchOverhead, 0
	p.convAt = map[string]int{}
}

// room flushes the current batch when adding cost bytes (and messages) would
// exceed the limits. An item that is too large on its own still goes into an
// empty batch.
func (p *packer) room(cost, messages int) {
	if !p.empty() && (p.size+cost > MaxBatchBytes || p.messages+messages > MaxBatchMessages) {
		p.flush()
	}
}

func pack(contacts []apitypes.HistoryContact, convs []convItem) []apitypes.HistoryBatchPayload {
	p := &packer{size: batchOverhead, convAt: map[string]int{}}
	for _, c := range contacts {
		cost := jsonLen(c) + 1
		p.room(cost, 0)
		p.cur.Contacts = append(p.cur.Contacts, c)
		p.size += cost
	}
	for _, conv := range convs {
		metaCost := jsonLen(conv.meta) + 1
		ensureMeta := func() int {
			if i, ok := p.convAt[conv.meta.ChatJID]; ok {
				return i
			}
			meta := conv.meta
			meta.Messages = []apitypes.HistoryMessage{}
			p.cur.Conversations = append(p.cur.Conversations, meta)
			p.size += metaCost
			i := len(p.cur.Conversations) - 1
			p.convAt[conv.meta.ChatJID] = i
			return i
		}
		if len(conv.messages) == 0 {
			p.room(metaCost, 0)
			ensureMeta()
			continue
		}
		for _, m := range conv.messages {
			cost := jsonLen(m) + 1
			need := cost
			if _, ok := p.convAt[conv.meta.ChatJID]; !ok {
				need += metaCost
			}
			p.room(need, 1)
			i := ensureMeta()
			p.cur.Conversations[i].Messages = append(p.cur.Conversations[i].Messages, m)
			p.size += cost
			p.messages++
		}
	}
	p.flush()
	return p.out
}

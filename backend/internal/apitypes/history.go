package apitypes

// History sync events. When a device is linked (or relinked), the phone sends
// the chat history it still has in a series of WhatsApp "history sync"
// notifications. The gateway turns each notification into:
//
//   - `history.sync` lifecycle events (started, progress, completed, failed)
//     with running counters. These are small and are delivered like any other
//     event, including to `*` subscriptions and the realtime stream.
//   - `history.batch` data events, each carrying up to 200 messages (and at
//     most about 96 KiB of JSON) plus the conversations and contacts they
//     belong to. These are bulk events: they are delivered only to webhooks
//     that list `history.batch` explicitly. `*` does not include them, they are
//     never sent over the realtime stream, and event replay (`since`) skips
//     them.
//
// Imported messages never produce `message` / `message.from_me` events. The
// API stores them like live messages, marked with origin `history`.

// HistorySyncPayload is the payload of a `history.sync` event: the lifecycle
// and running totals of one history import run.
type HistorySyncPayload struct {
	RunID         string `json:"runId" doc:"Id of the import run. A run starts when the phone begins sending history after a device link and covers every notification that follows for the same WhatsApp account. history.batch events of the run carry the same id." example:"hsr_01J9ZX3K8Q2W5V7B4N6M1C0D9E"`
	Phase         string `json:"phase" doc:"Where the run is. started: the first history notification arrived. progress: another notification was imported. completed: the phone reported all history sent (or the run went quiet) and every batch was emitted. failed: the run stopped; see error." enum:"started,progress,completed,failed" example:"progress"`
	AccountJID    string `json:"accountJid,omitempty" doc:"Phone JID of the WhatsApp account whose history this is. Compare it with the session's account to detect a relink to a different number." example:"6281234567890@s.whatsapp.net"`
	SyncType      string `json:"syncType,omitempty" doc:"Kind of the WhatsApp history notification that caused this update. initial_bootstrap and recent carry recent chats and messages; full carries older history (only when the operator enables full sync); push_name carries contact display names; the other kinds carry status updates and settings." enum:"initial_bootstrap,recent,full,push_name,initial_status_v3,non_blocking_data,on_demand" example:"recent"`
	ChunkOrder    int    `json:"chunkOrder,omitempty" doc:"Position of that notification within its sync type, as numbered by WhatsApp." example:"2"`
	Progress      *int   `json:"progress,omitempty" doc:"Percent of the history the phone reports as sent so far (0-100). Omitted when the phone did not report progress." minimum:"0" maximum:"100" example:"45"`
	CutoffAt      int64  `json:"cutoffAt,omitempty" doc:"Messages older than this time (epoch milliseconds) are not imported, per the session's historySyncDays setting. Omitted when there is no cutoff." example:"1717000000000"`
	Chunks        int    `json:"chunks" doc:"Number of WhatsApp history notifications processed so far in this run." example:"3"`
	Batches       int    `json:"batches" doc:"Number of history.batch events emitted so far in this run." example:"12"`
	Conversations int    `json:"conversations" doc:"Number of conversations included in batches so far. A conversation split over several batches counts once per notification." example:"40"`
	Messages      int    `json:"messages" doc:"Number of messages included in batches so far. Messages already stored (for example received live) are still counted here; the API skips them on import." example:"1830"`
	Skipped       int    `json:"skipped" doc:"Number of messages left out so far: older than cutoffAt, system notices, or messages that could not be decoded." example:"25"`
	Error         string `json:"error,omitempty" doc:"Why the run failed. Only set when phase is failed." example:"history blob expired on WhatsApp servers"`
}

// HistoryBatchPayload is the payload of a `history.batch` event: one slice of
// imported history. A large notification is split into several parts; each
// part repeats the metadata of the conversations it carries messages for.
type HistoryBatchPayload struct {
	RunID         string                `json:"runId" doc:"Id of the import run this batch belongs to (see history.sync)." example:"hsr_01J9ZX3K8Q2W5V7B4N6M1C0D9E"`
	AccountJID    string                `json:"accountJid" doc:"Phone JID of the WhatsApp account whose history this is." example:"6281234567890@s.whatsapp.net"`
	SyncType      string                `json:"syncType" doc:"Kind of the WhatsApp history notification this batch came from (see history.sync)." enum:"initial_bootstrap,recent,full,push_name,initial_status_v3,non_blocking_data,on_demand" example:"recent"`
	ChunkOrder    int                   `json:"chunkOrder" doc:"Position of the source notification within its sync type, as numbered by WhatsApp." example:"2"`
	Part          int                   `json:"part" doc:"1-based index of this batch within the source notification." minimum:"1" example:"1"`
	Parts         int                   `json:"parts" doc:"Total number of batches the source notification was split into." minimum:"1" example:"3"`
	SnapshotAt    int64                 `json:"snapshotAt" doc:"When the phone produced this snapshot, in epoch milliseconds. Chat state (unread counts, names) in the batch is as of this time; a receiver should not apply it over newer state it already has." example:"1719400000000"`
	Conversations []HistoryConversation `json:"conversations" doc:"Conversations in this batch with their messages. A conversation can appear in several parts of the same notification; its metadata is repeated in each."`
	Contacts      []HistoryContact      `json:"contacts,omitempty" doc:"Contact names and phone-number-to-LID mappings from the notification. A large list is split across parts like messages."`
}

// HistoryConversation is one chat in a history.batch: its state on the phone
// and the messages of this batch, oldest first.
type HistoryConversation struct {
	ChatJID       string               `json:"chatJid" doc:"JID of the chat (a user LID or phone JID, group, newsletter, or broadcast list)." example:"6281234567890@s.whatsapp.net"`
	Type          string               `json:"type" doc:"Chat kind, derived from the JID." enum:"dm,group,newsletter,broadcast,status" example:"dm"`
	Name          string               `json:"name,omitempty" doc:"Chat display name on the phone: the group subject, or the contact name for a direct chat. Only fills a missing name; never overwrites a known one." example:"Project team"`
	Description   string               `json:"description,omitempty" doc:"Group description, when the phone included it." example:"Weekly planning"`
	LID           string               `json:"lid,omitempty" doc:"For a direct chat: the contact's LID, when known." example:"123456789012345@lid"`
	PhoneJID      string               `json:"phoneJid,omitempty" doc:"For a direct chat: the contact's phone JID, when known." example:"6281234567890@s.whatsapp.net"`
	UnreadCount   *int                 `json:"unreadCount,omitempty" doc:"Unread messages in this chat on the phone at snapshotAt. Omitted when the phone did not say." minimum:"0" example:"2"`
	Archived      bool                 `json:"archived,omitempty" doc:"True when the chat is archived on the phone. Applied only when the chat is first created locally." example:"false"`
	Pinned        bool                 `json:"pinned,omitempty" doc:"True when the chat is pinned on the phone. Applied only when the chat is first created locally." example:"false"`
	MutedUntil    int64                `json:"mutedUntil,omitempty" doc:"Mute end time in epoch milliseconds, when the chat is muted on the phone. Applied only when the chat is first created locally." example:"1719500000000"`
	LastMessageAt int64                `json:"lastMessageAt,omitempty" doc:"Time of the chat's latest message on the phone, in epoch milliseconds. Only moves a stored value forward." example:"1719399000000"`
	Participants  []HistoryParticipant `json:"participants,omitempty" doc:"For a group: members as listed by the phone."`
	Messages      []HistoryMessage     `json:"messages" doc:"Messages of this chat in this batch, oldest first. Can be empty when the batch only carries chat metadata."`
}

// HistoryParticipant is one group member listed in a history conversation.
type HistoryParticipant struct {
	LID      string `json:"lid,omitempty" doc:"The member's LID. Members without a known LID are not stored." example:"123456789012345@lid"`
	PhoneJID string `json:"phoneJid,omitempty" doc:"The member's phone JID, when known." example:"6281234567890@s.whatsapp.net"`
	Role     string `json:"role" doc:"The member's role in the group." enum:"member,admin,superadmin" example:"member"`
}

// HistoryContact is a contact name or address mapping from a history
// notification.
type HistoryContact struct {
	LID      string `json:"lid,omitempty" doc:"The contact's LID, when known." example:"123456789012345@lid"`
	PhoneJID string `json:"phoneJid,omitempty" doc:"The contact's phone JID, when known. When both lid and phoneJid are set they are the same person." example:"6281234567890@s.whatsapp.net"`
	PushName string `json:"pushName,omitempty" doc:"The contact's WhatsApp display (push) name. Only fills a missing name." example:"Alice"`
}

// HistoryMessage is one imported message: the same fields as the live
// `message` payload, plus import-only fields.
type HistoryMessage struct {
	MessagePayload
	Status    string `json:"status,omitempty" doc:"For messages this account sent: the delivery status WhatsApp recorded. Applied only when the message is first stored." enum:"pending,sent,delivered,read,played,failed" example:"read"`
	Truncated bool   `json:"truncated,omitempty" doc:"True when body or quotedBody was shortened to keep the batch under its size limit." example:"false"`
}

// HistorySyncEvent is the envelope for a history import lifecycle event.
type HistorySyncEvent struct {
	EventMeta
	Event   string             `json:"event" doc:"The event type." enum:"history.sync" example:"history.sync"`
	Payload HistorySyncPayload `json:"payload"`
}

// HistoryBatchEvent is the envelope for a bulk history data event. Delivered
// only to webhooks that subscribe to history.batch by name.
type HistoryBatchEvent struct {
	EventMeta
	Event   string              `json:"event" doc:"The event type. Opt-in only: a webhook subscribed to * does not receive it." enum:"history.batch" example:"history.batch"`
	Payload HistoryBatchPayload `json:"payload"`
}

package domain

// This file holds the value types exchanged across the "live ops" boundary —
// the operations the resource services delegate to a connected whatsmeow client
// (group management, on-WhatsApp checks, profile picture/about, presence). They
// live in domain so both the service-layer port interfaces and the manager-backed
// adapter (internal/wa) can reference the exact same types without an import
// cycle (service imports wa; wa imports domain).

// GroupParticipantAction enumerates the participant mutations (§11 Groups).
type GroupParticipantAction string

const (
	GroupActionAdd     GroupParticipantAction = "add"
	GroupActionRemove  GroupParticipantAction = "remove"
	GroupActionPromote GroupParticipantAction = "promote"
	GroupActionDemote  GroupParticipantAction = "demote"
)

// GroupInfo is the live view of a group returned by create/get/update flows.
type GroupInfo struct {
	GroupJID     string `json:"groupJid"`
	Subject      string `json:"subject,omitempty"`
	Description  string `json:"description,omitempty"`
	OwnerJID     string `json:"ownerJid,omitempty"`
	Participants int    `json:"participants,omitempty"`
	IsAnnounce   bool   `json:"isAnnounce,omitempty"`
	IsLocked     bool   `json:"isLocked,omitempty"`
}

// GroupSettings is the PATCH /groups/{gid} mutable surface. Nil fields are left
// unchanged.
type GroupSettings struct {
	Subject     *string
	Description *string
	Announce    *bool
	Locked      *bool
}

// OnWhatsApp is the §11 contacts/check result for one queried phone.
type OnWhatsApp struct {
	Query string `json:"query"`
	JID   string `json:"jid,omitempty"`
	IsIn  bool   `json:"isOnWhatsApp"`
}

// ProfilePicture is the §11 contacts/{jid}/picture result.
type ProfilePicture struct {
	URL string `json:"url,omitempty"`
	ID  string `json:"id,omitempty"`
}

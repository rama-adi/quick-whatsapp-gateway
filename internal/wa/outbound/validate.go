package outbound

import (
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// MaxMediaBytes caps the decoded size of an inline (base64) media send. It bounds
// memory use and the size of the JSON payload stored on the outbox row for async
// sends; it sits under WhatsApp's own media limits.
const MaxMediaBytes = 16 * 1024 * 1024 // 16 MiB

// validate checks a SendRequest's type and the per-type required fields,
// returning a *domain.APIError (validation_error) on failure. It is the single
// gate before any whatsmeow call.
func validate(req domain.SendRequest) error {
	if req.Type == "" {
		return domain.ErrValidation("send type is required")
	}
	if req.To == "" {
		return domain.ErrValidation("recipient 'to' is required")
	}

	switch req.Type {
	case domain.SendTypeText:
		if req.Text == "" {
			return domain.ErrValidation("text is required for type 'text'")
		}
	case domain.SendTypePoll:
		if req.Name == "" {
			return domain.ErrValidation("name is required for type 'poll'")
		}
		if len(req.Options) < 2 {
			return domain.ErrValidation("poll requires at least 2 options")
		}
		if req.SelectableCount < 0 || req.SelectableCount > len(req.Options) {
			return domain.ErrValidation("selectableCount must be between 0 and the number of options")
		}
		if req.PollEndTime < 0 {
			return domain.ErrValidation("pollEndTime must be a positive epoch millisecond timestamp")
		}
	case domain.SendTypeLocation:
		if req.Latitude < -90 || req.Latitude > 90 {
			return domain.ErrValidation("latitude must be between -90 and 90")
		}
		if req.Longitude < -180 || req.Longitude > 180 {
			return domain.ErrValidation("longitude must be between -180 and 180")
		}
	case domain.SendTypeContact:
		if req.Contact == nil {
			return domain.ErrValidation("contact is required for type 'contact'")
		}
		if req.Contact.VCard == "" && (req.Contact.Name == "" || req.Contact.Phone == "") {
			return domain.ErrValidation("contact requires either a vcard or both name and phone")
		}
	case domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker:
		if req.Media == nil || req.Media.Data == "" {
			return domain.ErrValidation(fmt.Sprintf("media.data (base64) is required for type '%s'", req.Type))
		}
		// Reject oversized payloads up front, from the base64 length, without
		// decoding the whole thing (decoded bytes ≈ 3/4 of the encoded length).
		if approxDecodedLen(req.Media.Data) > MaxMediaBytes {
			return domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
		}
	default:
		return domain.ErrValidation(fmt.Sprintf("unknown send type %q", req.Type))
	}
	return nil
}

// approxDecodedLen estimates the byte length a base64 string decodes to (3 bytes
// per 4 chars), ignoring padding nuances — close enough for an upper-bound check.
func approxDecodedLen(b64 string) int {
	return len(b64) / 4 * 3
}

// MessageOp identifies a message sub-resource operation (§11) routed through
// SendOp rather than the typed body send.
type MessageOp string

const (
	OpReaction MessageOp = "reaction"
	OpEdit     MessageOp = "edit"
	OpRevoke   MessageOp = "revoke"
	OpVote     MessageOp = "vote"
	OpForward  MessageOp = "forward"
)

// OpRequest is the input for a message operation (reaction/edit/revoke/vote/
// forward). The target message is identified by its chat + (original) sender +
// wa_message_id, as the §11 sub-resource routes provide.
type OpRequest struct {
	Op MessageOp
	// Chat is the chat JID the target message lives in.
	Chat string
	// Sender is the original message sender JID ("" for your own outgoing
	// message, where whatsmeow expects an empty JID).
	Sender string
	// MsgID is the target wa_message_id.
	MsgID string

	// Emoji for OpReaction ("" removes the reaction).
	Emoji string
	// NewText for OpEdit.
	NewText string
	// Options for OpVote (selected option names).
	Options []string
	// To for OpForward (destination chat JID).
	To string
}

// validateOp checks an OpRequest's required fields per operation.
func validateOp(req OpRequest) error {
	if req.MsgID == "" {
		return domain.ErrValidation("target message id is required")
	}
	if req.Chat == "" {
		return domain.ErrValidation("chat is required")
	}
	switch req.Op {
	case OpReaction:
		// Emoji may be empty (removal); nothing else required.
	case OpEdit:
		if req.NewText == "" {
			return domain.ErrValidation("newText is required for edit")
		}
	case OpRevoke:
		// no extra fields
	case OpVote:
		if len(req.Options) == 0 {
			return domain.ErrValidation("at least one option is required for vote")
		}
	case OpForward:
		if req.To == "" {
			return domain.ErrValidation("destination 'to' is required for forward")
		}
	default:
		return domain.ErrValidation(fmt.Sprintf("unknown message op %q", req.Op))
	}
	return nil
}

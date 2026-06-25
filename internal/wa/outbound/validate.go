package outbound

import (
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// mediaTypes are the send types that parse but are not implemented in v1 (§8:
// media sends return 501). Kept as a set so validate can give a precise error.
var mediaTypes = map[string]struct{}{
	domain.SendTypeImage:    {},
	domain.SendTypeVideo:    {},
	domain.SendTypeAudio:    {},
	domain.SendTypeDocument: {},
	domain.SendTypeSticker:  {},
}

// validate checks a SendRequest's type and the per-type required fields,
// returning a *domain.APIError (validation_error / not_implemented) on failure.
// It is the single gate before any whatsmeow call: media types are rejected with
// not_implemented (501) here so they never reach dispatch.
func validate(req domain.SendRequest) error {
	if req.Type == "" {
		return domain.ErrValidation("send type is required")
	}
	if _, isMedia := mediaTypes[req.Type]; isMedia {
		return domain.ErrNotImplemented(fmt.Sprintf("media send type %q is not implemented in v1", req.Type)).
			WithDetails(map[string]any{"type": req.Type})
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
	default:
		return domain.ErrValidation(fmt.Sprintf("unknown send type %q", req.Type))
	}
	return nil
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

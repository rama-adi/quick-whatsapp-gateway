package outbound

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func buildInteractive(req domain.SendRequest, info *waE2E.ContextInfo) (*waE2E.Message, error) {
	if err := validateInteractive(req); err != nil {
		return nil, err
	}
	buttons := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(req.Buttons))
	add := func(name string, params any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		buttons = append(buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name: proto.String(name), ButtonParamsJSON: proto.String(string(data)),
		})
		return nil
	}
	if req.Type == domain.SendTypeButtons {
		for _, button := range req.Buttons {
			if err := add("quick_reply", struct {
				ID          string `json:"id"`
				DisplayText string `json:"display_text"`
			}{ID: button.ID, DisplayText: button.Title}); err != nil {
				return nil, err
			}
		}
	} else {
		if err := add("single_select", req.List); err != nil {
			return nil, err
		}
	}
	return &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(req.Text)},
		Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(req.Footer)},
		ContextInfo: info,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: buttons, MessageVersion: proto.Int32(3)},
		},
	}}, nil
}

func (a *whatsmeowAdapter) SendInteractive(
	ctx context.Context,
	req domain.SendRequest,
	quote QuoteInfo,
) (string, int64, error) {
	to, err := parseJID(req.To)
	if err != nil {
		return "", 0, err
	}
	info := buildContextInfo(a.fillOwnQuote(to, quote), req.Mentions)
	msg, err := buildInteractive(req, info)
	if err != nil {
		return "", 0, err
	}
	// The pinned whatsmeow version does not add native-flow metadata for outgoing
	// InteractiveMessage. Use the metadata from the working quick-reply report:
	// https://github.com/tulir/whatsmeow/discussions/1145
	nodes := []waBinary.Node{{Tag: "biz", Content: []waBinary.Node{{
		Tag: "interactive", Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
		Content: []waBinary.Node{{Tag: "native_flow", Attrs: waBinary.Attrs{"v": "9", "name": "mixed"}}},
	}}}}
	resp, err := a.cli.SendMessage(
		ctx,
		to,
		msg,
		whatsmeow.SendRequestExtra{AdditionalNodes: &nodes},
	)
	if err != nil {
		return "", 0, fmt.Errorf("whatsmeow send interactive: %w", err)
	}
	return resp.ID, resp.Timestamp.UnixMilli(), nil
}

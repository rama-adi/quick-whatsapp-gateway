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
	if req.Type == domain.SendTypeList {
		// Native-flow single_select can be delivered while remaining invisible
		// to a regular linked-device recipient. ListMessage gets a dedicated
		// <biz><list> node from whatsmeow and preserves row IDs in list replies.
		sections := make([]*waE2E.ListMessage_Section, 0, len(req.List.Sections))
		for _, section := range req.List.Sections {
			rows := make([]*waE2E.ListMessage_Row, 0, len(section.Rows))
			for _, row := range section.Rows {
				rows = append(rows, &waE2E.ListMessage_Row{
					RowID: proto.String(row.ID), Title: proto.String(row.Title),
					Description: proto.String(row.Description),
				})
			}
			sections = append(sections, &waE2E.ListMessage_Section{
				Title: proto.String(section.Title), Rows: rows,
			})
		}
		return &waE2E.Message{ListMessage: &waE2E.ListMessage{
			Title: proto.String(req.List.Title), Description: proto.String(req.Text),
			ButtonText: proto.String(req.List.Title), FooterText: proto.String(req.Footer),
			ListType: waE2E.ListMessage_SINGLE_SELECT.Enum(), Sections: sections,
			ContextInfo: info,
		}}, nil
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
	for _, button := range req.Buttons {
		if err := add("quick_reply", struct {
			ID          string `json:"id"`
			DisplayText string `json:"display_text"`
		}{ID: button.ID, DisplayText: button.Title}); err != nil {
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
	var extra whatsmeow.SendRequestExtra
	if req.Type == domain.SendTypeButtons {
		// The pinned whatsmeow version does not add native-flow metadata for
		// outgoing InteractiveMessage. Legacy ListMessage gets its own biz node.
		nodes := []waBinary.Node{{Tag: "biz", Content: []waBinary.Node{{
			Tag: "interactive", Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{Tag: "native_flow", Attrs: waBinary.Attrs{"v": "9", "name": "mixed"}}},
		}}}}
		extra.AdditionalNodes = &nodes
	}
	resp, err := a.transport.SendMessage(ctx, to, msg, extra)
	if err != nil {
		return "", 0, fmt.Errorf("whatsmeow send interactive: %w", err)
	}
	return resp.ID, resp.Timestamp.UnixMilli(), nil
}

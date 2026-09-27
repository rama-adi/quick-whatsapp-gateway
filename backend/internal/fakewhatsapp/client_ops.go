package fakewhatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// call executes a live operation against the independent fake peer. WhatsApp
// state and response values are authoritative on the server across gateway restarts.
func (c *Client) call(ctx context.Context, kind string, payload, result any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response, err := c.operation(ctx, OperationRequest{Kind: kind, Payload: raw})
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if len(response.Result) == 0 {
		return errors.New("fake WhatsApp operation result missing")
	}
	return json.Unmarshal(response.Result, result)
}

func (c *Client) SendPresence(ctx context.Context, state types.Presence) error {
	return c.call(ctx, "presence", state, nil)
}
func (c *Client) SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	return c.call(ctx, "chat-presence", map[string]any{"jid": jid.String(), "state": state, "media": media}, nil)
}
func (c *Client) SubscribePresence(ctx context.Context, jid types.JID) error {
	return c.call(ctx, "subscribe-presence", jid.String(), nil)
}
func (c *Client) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, extra ...types.ReceiptType) error {
	return c.call(ctx, "mark-read", map[string]any{"ids": ids, "timestamp": timestamp, "chat": chat.String(), "sender": sender.String(), "types": extra}, nil)
}
func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	var result []types.IsOnWhatsAppResponse
	err := c.call(ctx, "lookup", phones, &result)
	return result, err
}
func (c *Client) GetProfilePictureInfo(ctx context.Context, jid types.JID, _ *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
	var result types.ProfilePictureInfo
	if err := c.call(ctx, "picture", jid.String(), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (c *Client) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	var rows []struct {
		JID  string         `json:"jid"`
		Info types.UserInfo `json:"info"`
	}
	if err := c.call(ctx, "user-info", jids, &rows); err != nil {
		return nil, err
	}
	result := make(map[types.JID]types.UserInfo, len(rows))
	for _, row := range rows {
		jid, err := types.ParseJID(row.JID)
		if err != nil {
			return nil, err
		}
		result[jid] = row.Info
	}
	return result, nil
}
func (c *Client) UpdateBlocklist(ctx context.Context, jid types.JID, action events.BlocklistChangeAction) (*types.Blocklist, error) {
	var result types.Blocklist
	if err := c.call(ctx, "blocklist", map[string]any{"jid": jid.String(), "action": action}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (c *Client) GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error) {
	var result []*types.GroupInfo
	err := c.call(ctx, "joined-groups", nil, &result)
	return result, err
}
func (c *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	var result types.GroupInfo
	if err := c.call(ctx, "group-info", jid.String(), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (c *Client) CreateGroup(ctx context.Context, request whatsmeow.ReqCreateGroup) (*types.GroupInfo, error) {
	var result types.GroupInfo
	if err := c.call(ctx, "create-group", request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (c *Client) UpdateGroupParticipants(ctx context.Context, jid types.JID, members []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error) {
	var result []types.GroupParticipant
	err := c.call(ctx, "group-participants", map[string]any{"jid": jid.String(), "members": members, "action": action}, &result)
	return result, err
}
func (c *Client) SetGroupName(ctx context.Context, jid types.JID, name string) error {
	return c.call(ctx, "group-name", map[string]any{"jid": jid.String(), "value": name}, nil)
}
func (c *Client) SetGroupTopic(ctx context.Context, jid types.JID, previous, next, topic string) error {
	return c.call(ctx, "group-topic", map[string]any{"jid": jid.String(), "value": topic}, nil)
}
func (c *Client) SetGroupAnnounce(ctx context.Context, jid types.JID, value bool) error {
	return c.call(ctx, "group-announce", map[string]any{"jid": jid.String(), "value": value}, nil)
}
func (c *Client) SetGroupLocked(ctx context.Context, jid types.JID, value bool) error {
	return c.call(ctx, "group-locked", map[string]any{"jid": jid.String(), "value": value}, nil)
}
func (c *Client) GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error) {
	var result string
	err := c.call(ctx, "group-invite", map[string]any{"jid": jid.String(), "reset": reset}, &result)
	return result, err
}
func (c *Client) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error) {
	var result string
	if err := c.call(ctx, "group-join", code, &result); err != nil {
		return types.EmptyJID, err
	}
	return types.ParseJID(result)
}
func (c *Client) LeaveGroup(ctx context.Context, jid types.JID) error {
	return c.call(ctx, "group-leave", jid.String(), nil)
}
func (c *Client) DownloadAny(ctx context.Context, message *waE2E.Message) ([]byte, error) {
	var directPath string
	switch {
	case message.GetImageMessage() != nil:
		directPath = message.GetImageMessage().GetDirectPath()
	case message.GetVideoMessage() != nil:
		directPath = message.GetVideoMessage().GetDirectPath()
	case message.GetAudioMessage() != nil:
		directPath = message.GetAudioMessage().GetDirectPath()
	case message.GetDocumentMessage() != nil:
		directPath = message.GetDocumentMessage().GetDirectPath()
	case message.GetStickerMessage() != nil:
		directPath = message.GetStickerMessage().GetDirectPath()
	}
	var encoded string
	if err := c.call(ctx, "download", directPath, &encoded); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(encoded)
}

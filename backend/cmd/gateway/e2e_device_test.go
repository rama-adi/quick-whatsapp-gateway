package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// e2eDeviceClient simulates the remote WhatsApp service, not gateway services.
// Manager, LiveOps JID conversions, gRPC, command fencing, and persistence remain
// production code. The embedded client supplies local protocol helpers only.
type e2eDeviceClient struct {
	*whatsmeow.Client
	transport *e2eWhatsApp
	mu        sync.Mutex
	connected bool
	handler   whatsmeow.EventHandler
	groups    map[types.JID]*types.GroupInfo
	blocked   map[types.JID]bool
}

func (c *e2eDeviceClient) Connect() error {
	c.mu.Lock()
	c.connected = true
	handler := c.handler
	paired := c.Store.ID != nil
	c.mu.Unlock()
	if paired && handler != nil {
		handler(&events.Connected{})
	}
	return nil
}
func (c *e2eDeviceClient) Disconnect()                  { c.mu.Lock(); c.connected = false; c.mu.Unlock() }
func (c *e2eDeviceClient) IsConnected() bool            { c.mu.Lock(); defer c.mu.Unlock(); return c.connected }
func (c *e2eDeviceClient) IsLoggedIn() bool             { return c.Store.ID != nil && c.IsConnected() }
func (c *e2eDeviceClient) Logout(context.Context) error { c.Disconnect(); return nil }
func (c *e2eDeviceClient) AddEventHandler(handler whatsmeow.EventHandler) uint32 {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
	return 1
}
func (c *e2eDeviceClient) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	result := make(chan whatsmeow.QRChannelItem)
	go func() {
		defer close(result)
		select {
		// whatsmeow qrchan.go gives the first QR code a 60-second lifetime.
		case result <- whatsmeow.QRChannelItem{Event: "code", Code: "isolated-e2e-qr", Timeout: 60 * time.Second}:
		case <-ctx.Done():
		}
	}()
	return result, nil
}
func (c *e2eDeviceClient) PairPhone(context.Context, string, bool, whatsmeow.PairClientType, string) (string, error) {
	return "E2E-CODE", nil
}
func (c *e2eDeviceClient) record(operation string, payload any) error {
	c.transport.mu.Lock()
	defer c.transport.mu.Unlock()
	c.transport.operations = append(c.transport.operations, map[string]any{"operation": operation, "payload": payload})
	if c.transport.mode == "send_error" {
		return errors.New("simulated external WhatsApp operation failure")
	}
	return nil
}
func (c *e2eDeviceClient) SendPresence(_ context.Context, state types.Presence) error {
	return c.record("presence", state)
}
func (c *e2eDeviceClient) SendChatPresence(_ context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	return c.record("chat-presence", map[string]any{"jid": jid.String(), "state": state, "media": media})
}
func (c *e2eDeviceClient) SubscribePresence(_ context.Context, jid types.JID) error {
	return c.record("subscribe-presence", jid.String())
}
func (c *e2eDeviceClient) MarkRead(_ context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, extra ...types.ReceiptType) error {
	return c.record("mark-read", map[string]any{"ids": ids, "timestamp": timestamp, "chat": chat.String(), "sender": sender.String(), "types": extra})
}
func (c *e2eDeviceClient) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	if err := c.record("lookup", phones); err != nil {
		return nil, err
	}
	result := make([]types.IsOnWhatsAppResponse, 0, len(phones))
	for _, phone := range phones {
		result = append(result, types.IsOnWhatsAppResponse{Query: phone, JID: types.NewJID(phone, types.DefaultUserServer), IsIn: true})
	}
	return result, nil
}
func (c *e2eDeviceClient) GetProfilePictureInfo(_ context.Context, jid types.JID, _ *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
	if err := c.record("picture", jid.String()); err != nil {
		return nil, err
	}
	return &types.ProfilePictureInfo{URL: "https://isolated.invalid/profile", ID: "e2e-picture"}, nil
}
func (c *e2eDeviceClient) GetUserInfo(_ context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	if err := c.record("user-info", jids); err != nil {
		return nil, err
	}
	result := map[types.JID]types.UserInfo{}
	for _, jid := range jids {
		result[jid] = types.UserInfo{Status: "Isolated profile"}
	}
	return result, nil
}
func (c *e2eDeviceClient) UpdateBlocklist(_ context.Context, jid types.JID, action events.BlocklistChangeAction) (*types.Blocklist, error) {
	if err := c.record("blocklist", map[string]any{"jid": jid.String(), "action": action}); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[jid] = action == events.BlocklistChangeActionBlock
	return &types.Blocklist{}, nil
}
func (c *e2eDeviceClient) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]*types.GroupInfo, 0, len(c.groups))
	for _, group := range c.groups {
		copy := *group
		copy.Participants = append([]types.GroupParticipant{}, group.Participants...)
		result = append(result, &copy)
	}
	return result, nil
}
func (c *e2eDeviceClient) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.groups[jid]
	if group == nil {
		return nil, errors.New("simulated group not found")
	}
	copy := *group
	copy.Participants = append([]types.GroupParticipant{}, group.Participants...)
	return &copy, nil
}
func (c *e2eDeviceClient) CreateGroup(_ context.Context, request whatsmeow.ReqCreateGroup) (*types.GroupInfo, error) {
	if err := c.record("create-group", request); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	jid := types.NewJID(fmt.Sprintf("120363%d", len(c.groups)+1), types.GroupServer)
	group := &types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: request.Name}, Participants: []types.GroupParticipant{}}
	for _, member := range request.Participants {
		group.Participants = append(group.Participants, types.GroupParticipant{JID: member, PhoneNumber: member})
	}
	c.groups[jid] = group
	copy := *group
	return &copy, nil
}
func (c *e2eDeviceClient) UpdateGroupParticipants(_ context.Context, jid types.JID, members []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error) {
	if err := c.record("group-participants", map[string]any{"jid": jid.String(), "members": members, "action": action}); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.groups[jid]
	if group == nil {
		return nil, errors.New("simulated group not found")
	}
	for _, member := range members {
		found := -1
		for i, p := range group.Participants {
			if p.JID == member {
				found = i
				break
			}
		}
		switch action {
		case whatsmeow.ParticipantChangeAdd:
			if found < 0 {
				group.Participants = append(group.Participants, types.GroupParticipant{JID: member, PhoneNumber: member})
			}
		case whatsmeow.ParticipantChangeRemove:
			if found >= 0 {
				group.Participants = append(group.Participants[:found], group.Participants[found+1:]...)
			}
		case whatsmeow.ParticipantChangePromote:
			if found >= 0 {
				group.Participants[found].IsAdmin = true
			}
		case whatsmeow.ParticipantChangeDemote:
			if found >= 0 {
				group.Participants[found].IsAdmin = false
			}
		}
	}
	return append([]types.GroupParticipant{}, group.Participants...), nil
}
func (c *e2eDeviceClient) changeGroup(jid types.JID, operation string, value any, change func(*types.GroupInfo)) error {
	if err := c.record(operation, map[string]any{"jid": jid.String(), "value": value}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.groups[jid]
	if group == nil {
		return errors.New("simulated group not found")
	}
	change(group)
	return nil
}
func (c *e2eDeviceClient) SetGroupName(_ context.Context, jid types.JID, name string) error {
	return c.changeGroup(jid, "group-name", name, func(g *types.GroupInfo) { g.Name = name })
}
func (c *e2eDeviceClient) SetGroupTopic(_ context.Context, jid types.JID, previous, next, topic string) error {
	return c.changeGroup(jid, "group-topic", topic, func(g *types.GroupInfo) { g.Topic = topic })
}
func (c *e2eDeviceClient) SetGroupAnnounce(_ context.Context, jid types.JID, value bool) error {
	return c.changeGroup(jid, "group-announce", value, func(g *types.GroupInfo) { g.IsAnnounce = value })
}
func (c *e2eDeviceClient) SetGroupLocked(_ context.Context, jid types.JID, value bool) error {
	return c.changeGroup(jid, "group-locked", value, func(g *types.GroupInfo) { g.IsLocked = value })
}
func (c *e2eDeviceClient) GetGroupInviteLink(_ context.Context, jid types.JID, reset bool) (string, error) {
	if err := c.record("group-invite", map[string]any{"jid": jid.String(), "reset": reset}); err != nil {
		return "", err
	}
	return "https://chat.whatsapp.com/isolated-invite", nil
}
func (c *e2eDeviceClient) JoinGroupWithLink(_ context.Context, code string) (types.JID, error) {
	if err := c.record("group-join", code); err != nil {
		return types.EmptyJID, err
	}
	return types.NewJID("120363999", types.GroupServer), nil
}
func (c *e2eDeviceClient) LeaveGroup(_ context.Context, jid types.JID) error {
	if err := c.record("group-leave", jid.String()); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.groups, jid)
	c.mu.Unlock()
	return nil
}
func (c *e2eDeviceClient) DownloadAny(_ context.Context, _ *waE2E.Message) ([]byte, error) {
	if err := c.record("download", nil); err != nil {
		return nil, err
	}
	return []byte("isolated-media"), nil
}

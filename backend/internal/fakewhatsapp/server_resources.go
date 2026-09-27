package fakewhatsapp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func asResult(value any) OperationResponse {
	raw, err := json.Marshal(value)
	if err != nil {
		return OperationResponse{Error: err.Error()}
	}
	return OperationResponse{Result: raw}
}

func (s *Server) performResource(se *session, req OperationRequest) (OperationResponse, bool) {
	switch req.Kind {
	case "presence", "chat-presence", "subscribe-presence", "mark-read":
		return OperationResponse{}, true
	case "lookup":
		var phones []string
		if err := json.Unmarshal(req.Payload, &phones); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		result := make([]types.IsOnWhatsAppResponse, 0, len(phones))
		for _, phone := range phones {
			result = append(result, types.IsOnWhatsAppResponse{Query: phone, JID: types.NewJID(phone, types.DefaultUserServer), IsIn: true})
		}
		return asResult(result), true
	case "picture":
		return asResult(types.ProfilePictureInfo{URL: "https://fake.whatsapp.invalid/profile", ID: "fake-picture"}), true
	case "user-info":
		var jids []types.JID
		if err := json.Unmarshal(req.Payload, &jids); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		result := make([]map[string]any, 0, len(jids))
		for _, jid := range jids {
			result = append(result, map[string]any{"jid": jid.String(), "info": types.UserInfo{Status: "Fake profile"}})
		}
		return asResult(result), true
	case "blocklist":
		var input struct {
			JID    string `json:"jid"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		jid, err := types.ParseJID(input.JID)
		if err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		if s.blocklists[se.number] == nil {
			s.blocklists[se.number] = map[string]bool{}
		}
		switch input.Action {
		case "block":
			s.blocklists[se.number][jid.String()] = true
		case "unblock":
			delete(s.blocklists[se.number], jid.String())
		default:
			return OperationResponse{Error: "unsupported blocklist action"}, true
		}
		result := types.Blocklist{JIDs: []types.JID{}}
		for value := range s.blocklists[se.number] {
			parsed, _ := types.ParseJID(value)
			result.JIDs = append(result.JIDs, parsed)
		}
		return asResult(result), true
	case "joined-groups":
		groups := make([]*types.GroupInfo, 0, len(s.groups))
		for key, group := range s.groups {
			if s.groupAccessible(key, se.number) {
				groups = append(groups, copyGroup(group))
			}
		}
		return asResult(groups), true
	case "group-info":
		jid, err := types.ParseJID(operationPayloadString(req.Payload))
		if err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		group := s.groups[jid.String()]
		if group == nil || !s.groupAccessible(jid.String(), se.number) {
			return OperationResponse{Error: "fake group not found"}, true
		}
		return asResult(copyGroup(group)), true
	case "create-group":
		var input whatsmeow.ReqCreateGroup
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		s.groupSequence++
		jid := types.NewJID(fmt.Sprintf("120363%d", s.groupSequence), types.GroupServer)
		owner := fakeParticipant(se.number)
		owner.IsSuperAdmin = true
		group := &types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: input.Name}, Participants: []types.GroupParticipant{owner}}
		for _, member := range input.Participants {
			if member.User == se.number {
				continue
			}
			group.Participants = append(group.Participants, fakeParticipant(member.User))
		}
		s.groups[jid.String()] = group
		s.groupOwners[jid.String()] = se.number
		s.grantGroup(jid.String(), se.number)
		return asResult(copyGroup(group)), true
	case "group-participants":
		var input struct {
			JID     string                      `json:"jid"`
			Members []types.JID                 `json:"members"`
			Action  whatsmeow.ParticipantChange `json:"action"`
		}
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		group := s.groups[input.JID]
		if group == nil || !s.groupAccessible(input.JID, se.number) {
			return OperationResponse{Error: "fake group not found"}, true
		}
		for _, member := range input.Members {
			found := groupParticipantIndex(group.Participants, member)
			switch input.Action {
			case whatsmeow.ParticipantChangeAdd:
				if found < 0 {
					group.Participants = append(group.Participants, fakeParticipant(member.User))
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
			default:
				return OperationResponse{Error: "unsupported participant action"}, true
			}
		}
		return asResult(group.Participants), true
	case "group-name", "group-topic", "group-announce", "group-locked":
		var input struct {
			JID   string          `json:"jid"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		group := s.groups[input.JID]
		if group == nil || !s.groupAccessible(input.JID, se.number) {
			return OperationResponse{Error: "fake group not found"}, true
		}
		switch req.Kind {
		case "group-name":
			if err := json.Unmarshal(input.Value, &group.Name); err != nil {
				return OperationResponse{Error: err.Error()}, true
			}
		case "group-topic":
			if err := json.Unmarshal(input.Value, &group.Topic); err != nil {
				return OperationResponse{Error: err.Error()}, true
			}
		case "group-announce":
			if err := json.Unmarshal(input.Value, &group.IsAnnounce); err != nil {
				return OperationResponse{Error: err.Error()}, true
			}
		case "group-locked":
			if err := json.Unmarshal(input.Value, &group.IsLocked); err != nil {
				return OperationResponse{Error: err.Error()}, true
			}
		}
		return OperationResponse{}, true
	case "group-invite":
		var input struct {
			JID   string `json:"jid"`
			Reset bool   `json:"reset"`
		}
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			return OperationResponse{Error: err.Error()}, true
		}
		if !s.groupAccessible(input.JID, se.number) {
			return OperationResponse{Error: "fake group not found"}, true
		}
		code := ""
		for existing, groupJID := range s.invites {
			if groupJID == input.JID {
				code = existing
				if input.Reset {
					delete(s.invites, existing)
					code = ""
				}
				break
			}
		}
		if code == "" {
			s.inviteSequence++
			code = fmt.Sprintf("fake-invite-%d", s.inviteSequence)
			s.invites[code] = input.JID
		}
		return asResult("https://chat.whatsapp.com/" + code), true
	case "group-join":
		code := operationPayloadString(req.Payload)
		code = strings.TrimPrefix(code, "https://chat.whatsapp.com/")
		groupJID := s.invites[code]
		if groupJID == "" {
			return OperationResponse{Error: "fake group invite not found"}, true
		}
		s.grantGroup(groupJID, se.number)
		return asResult(groupJID), true
	case "group-leave":
		jid := strings.Trim(operationPayloadString(req.Payload), `"`)
		if _, ok := s.groups[jid]; !ok || !s.groupAccessible(jid, se.number) {
			return OperationResponse{Error: "fake group not found"}, true
		}
		delete(s.groupAccess[jid], se.number)
		return OperationResponse{}, true
	case "download":
		path := operationPayloadString(req.Payload)
		data, ok := s.media[path]
		if !ok {
			return OperationResponse{Error: "fake media not found"}, true
		}
		return asResult(base64.StdEncoding.EncodeToString(data)), true
	default:
		return OperationResponse{}, false
	}
}

func copyGroup(group *types.GroupInfo) *types.GroupInfo {
	copy := *group
	copy.Participants = append([]types.GroupParticipant{}, group.Participants...)
	return &copy
}

func (s *Server) groupAccessible(jid, number string) bool {
	return s.groupAccess[jid][number]
}
func (s *Server) grantGroup(jid, number string) {
	if s.groupAccess[jid] == nil {
		s.groupAccess[jid] = map[string]bool{}
	}
	s.groupAccess[jid][number] = true
}

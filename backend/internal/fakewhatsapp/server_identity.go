package fakewhatsapp

import "go.mau.fi/whatsmeow/types"

// fakeParticipant mirrors the LID+phone pair WhatsApp exposes after group
// creation. The same deterministic LID is used by the fake paired device.
func fakeParticipant(phone string) types.GroupParticipant {
	pn := types.NewJID(phone, types.DefaultUserServer)
	lid := types.NewJID(phone, types.HiddenUserServer)
	return types.GroupParticipant{JID: lid, PhoneNumber: pn, LID: lid}
}

func groupParticipantIndex(participants []types.GroupParticipant, member types.JID) int {
	for index, participant := range participants {
		if participant.JID == member || participant.PhoneNumber == member || participant.LID == member {
			return index
		}
	}
	return -1
}

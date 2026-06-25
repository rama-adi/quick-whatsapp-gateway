package events

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestNormalizeSessionStatusEvents(t *testing.T) {
	tests := []struct {
		name       string
		evt        any
		wantType   string
		wantStatus domain.SessionStatus
	}{
		{"connected", &events.Connected{}, domain.EventSessionStatus, domain.SessionWorking},
		{"disconnected", &events.Disconnected{}, domain.EventSessionStatus, domain.SessionStarting},
		{"logged out", &events.LoggedOut{}, domain.EventSessionStatus, domain.SessionLoggedOut},
		{"stream replaced", &events.StreamReplaced{}, domain.EventSessionStatus, domain.SessionFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, pr, ok := Normalize(tt.evt, testSession, testTenant)
			if !ok {
				t.Fatalf("ok=false")
			}
			if ev.Type != tt.wantType {
				t.Errorf("type = %q", ev.Type)
			}
			if pr.Kind != PersistSessionStatus {
				t.Errorf("kind = %d", pr.Kind)
			}
			if pr.SessionStatus != tt.wantStatus {
				t.Errorf("status = %q, want %q", pr.SessionStatus, tt.wantStatus)
			}
			p, ok := ev.Payload.(SessionStatusPayload)
			if !ok || p.Status != string(tt.wantStatus) {
				t.Errorf("payload = %+v", ev.Payload)
			}
		})
	}
}

func TestNormalizeReceipt(t *testing.T) {
	tests := []struct {
		name       string
		rtype      types.ReceiptType
		wantOK     bool
		wantStatus domain.MessageStatus
	}{
		{"delivered", types.ReceiptTypeDelivered, true, domain.MessageDelivered},
		{"read", types.ReceiptTypeRead, true, domain.MessageRead},
		{"read-self", types.ReceiptTypeReadSelf, true, domain.MessageRead},
		{"played", types.ReceiptTypePlayed, true, domain.MessagePlayed},
		{"sender ignored", types.ReceiptTypeSender, false, ""},
		{"retry ignored", types.ReceiptTypeRetry, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &events.Receipt{
				MessageSource: types.MessageSource{
					Chat:   mustJID(t, "628111@s.whatsapp.net"),
					Sender: mustJID(t, "628222@s.whatsapp.net"),
				},
				MessageIDs: []types.MessageID{"m1", "m2"},
				Type:       tt.rtype,
				Timestamp:  time.UnixMilli(123),
			}
			ev, pr, ok := Normalize(e, testSession, testTenant)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if ev.Type != domain.EventMessageStatus {
				t.Errorf("type = %q", ev.Type)
			}
			if pr.Kind != PersistMessageStatus {
				t.Errorf("kind = %d", pr.Kind)
			}
			if pr.MessageStatus != tt.wantStatus {
				t.Errorf("status = %q, want %q", pr.MessageStatus, tt.wantStatus)
			}
			if len(pr.MessageIDs) != 2 {
				t.Errorf("messageIDs = %v", pr.MessageIDs)
			}
			p := ev.Payload.(MessageStatusPayload)
			if p.Status != string(tt.wantStatus) || len(p.MessageIDs) != 2 {
				t.Errorf("payload = %+v", p)
			}
		})
	}
}

func TestNormalizeQRAndPair(t *testing.T) {
	qr := &events.QR{Codes: []string{"qr-code-1", "qr-code-2"}}
	ev, pr, ok := Normalize(qr, testSession, testTenant)
	if !ok || ev.Type != domain.EventAuthQR {
		t.Fatalf("qr event wrong: %v %q", ok, ev.Type)
	}
	if ev.Payload.(AuthQRPayload).Code != "qr-code-1" {
		t.Errorf("qr code = %q", ev.Payload.(AuthQRPayload).Code)
	}
	if pr.Kind != PersistNone {
		t.Errorf("qr persist kind = %d", pr.Kind)
	}

	pair := &events.PairSuccess{
		ID:           mustJID(t, "628111@s.whatsapp.net"),
		LID:          mustJID(t, "777@lid"),
		BusinessName: "Biz",
		Platform:     "android",
	}
	ev, _, ok = Normalize(pair, testSession, testTenant)
	if !ok || ev.Type != domain.EventAuthCode {
		t.Fatalf("pair event wrong")
	}
	p := ev.Payload.(AuthCodePayload)
	if p.JID != "628111@s.whatsapp.net" || p.LID != "777@lid" || p.BusinessName != "Biz" {
		t.Errorf("pair payload = %+v", p)
	}
}

func TestNormalizePresence(t *testing.T) {
	pres := &events.Presence{
		From:        mustJID(t, "628111@s.whatsapp.net"),
		Unavailable: true,
		LastSeen:    time.UnixMilli(5000),
	}
	ev, pr, ok := Normalize(pres, testSession, testTenant)
	if !ok || ev.Type != domain.EventPresenceUpdate {
		t.Fatalf("presence wrong")
	}
	p := ev.Payload.(PresencePayload)
	if p.State != "unavailable" || !p.Unavailable || p.LastSeen != 5000 {
		t.Errorf("presence payload = %+v", p)
	}
	if pr.Kind != PersistNone {
		t.Errorf("presence kind = %d", pr.Kind)
	}

	chatPres := &events.ChatPresence{
		MessageSource: types.MessageSource{
			Chat:   mustJID(t, "12036@g.us"),
			Sender: mustJID(t, "628111@s.whatsapp.net"),
		},
		State: types.ChatPresenceComposing,
	}
	ev, _, ok = Normalize(chatPres, testSession, testTenant)
	if !ok {
		t.Fatalf("chat presence not ok")
	}
	cp := ev.Payload.(PresencePayload)
	if cp.State != string(types.ChatPresenceComposing) || cp.ChatJID != "12036@g.us" {
		t.Errorf("chat presence payload = %+v", cp)
	}
}

func TestNormalizeGroupInfo(t *testing.T) {
	// Metadata-only change -> group.update.
	gi := &events.GroupInfo{
		JID:  mustJID(t, "12036@g.us"),
		Name: &types.GroupName{Name: "New Name"},
	}
	ev, pr, ok := Normalize(gi, testSession, testTenant)
	if !ok || ev.Type != domain.EventGroupUpdate {
		t.Fatalf("group update wrong: %q", ev.Type)
	}
	if pr.Kind != PersistGroupUpdate {
		t.Errorf("kind = %d", pr.Kind)
	}
	if ev.Payload.(GroupPayload).Subject != "New Name" {
		t.Errorf("subject = %q", ev.Payload.(GroupPayload).Subject)
	}

	// Participant change -> group.participant.
	gi2 := &events.GroupInfo{
		JID:  mustJID(t, "12036@g.us"),
		Join: []types.JID{mustJID(t, "628999@s.whatsapp.net")},
	}
	ev2, pr2, _ := Normalize(gi2, testSession, testTenant)
	if ev2.Type != domain.EventGroupParticipant {
		t.Errorf("participant type = %q", ev2.Type)
	}
	if pr2.Kind != PersistGroupParticipant {
		t.Errorf("participant kind = %d", pr2.Kind)
	}
	gp := ev2.Payload.(GroupPayload)
	if len(gp.Join) != 1 || gp.Join[0] != "628999@s.whatsapp.net" {
		t.Errorf("join = %v", gp.Join)
	}
}

func TestNormalizeContactAndPushName(t *testing.T) {
	pn := &events.PushName{
		JID:         mustJID(t, "628111@s.whatsapp.net"),
		NewPushName: "Charlie",
	}
	ev, pr, ok := Normalize(pn, testSession, testTenant)
	if !ok || ev.Type != domain.EventContactUpdate {
		t.Fatalf("pushname wrong")
	}
	if pr.Kind != PersistContactUpdate || pr.PushName != "Charlie" {
		t.Errorf("pushname pr = %+v", pr)
	}
	if ev.Payload.(ContactUpdatePayload).PushName != "Charlie" {
		t.Errorf("pushname payload wrong")
	}

	c := &events.Contact{JID: mustJID(t, "628111@s.whatsapp.net")}
	ev2, pr2, ok := Normalize(c, testSession, testTenant)
	if !ok || ev2.Type != domain.EventContactUpdate {
		t.Fatalf("contact wrong")
	}
	if pr2.Kind != PersistContactUpdate {
		t.Errorf("contact kind = %d", pr2.Kind)
	}
}

func TestNormalizeCallOffer(t *testing.T) {
	co := &events.CallOffer{}
	co.CallID = "call-123"
	co.From = mustJID(t, "628111@s.whatsapp.net")
	co.Timestamp = time.UnixMilli(9999)
	ev, pr, ok := Normalize(co, testSession, testTenant)
	if !ok || ev.Type != domain.EventCallIncoming {
		t.Fatalf("call wrong")
	}
	p := ev.Payload.(CallPayload)
	if p.CallID != "call-123" || p.From != "628111@s.whatsapp.net" || p.Timestamp != 9999 {
		t.Errorf("call payload = %+v", p)
	}
	if pr.Kind != PersistNone {
		t.Errorf("call kind = %d", pr.Kind)
	}
}

func TestNormalizeNewsletter(t *testing.T) {
	nj := &events.NewsletterJoin{}
	nj.ID = mustJID(t, "12345@newsletter")
	ev, _, ok := Normalize(nj, testSession, testTenant)
	if !ok || ev.Type != domain.EventNewsletterUpdate {
		t.Fatalf("newsletter join wrong")
	}
	p := ev.Payload.(NewsletterPayload)
	if p.JID != "12345@newsletter" || p.Action != "join" {
		t.Errorf("newsletter payload = %+v", p)
	}

	nl := &events.NewsletterLeave{ID: mustJID(t, "12345@newsletter")}
	ev2, _, _ := Normalize(nl, testSession, testTenant)
	if ev2.Payload.(NewsletterPayload).Action != "leave" {
		t.Errorf("leave action wrong")
	}
}

func TestNormalizeUnknownEvent(t *testing.T) {
	_, _, ok := Normalize(&events.KeepAliveTimeout{}, testSession, testTenant)
	if ok {
		t.Errorf("expected ok=false for unhandled event")
	}
	_, _, ok = Normalize("not an event", testSession, testTenant)
	if ok {
		t.Errorf("expected ok=false for non-event")
	}
}

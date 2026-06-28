package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	waevents "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/events"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

func optionHash(opt string) string {
	sum := sha256.Sum256([]byte(opt))
	return hex.EncodeToString(sum[:])
}

func TestResolveSelectedOptions(t *testing.T) {
	options := []string{"Pizza", "Sushi", "Tacos"}

	got := resolveSelectedOptions(options, []string{optionHash("Sushi")})
	if len(got) != 1 || got[0] != "Sushi" {
		t.Fatalf("resolved = %v, want [Sushi]", got)
	}

	// An unknown hash is kept verbatim rather than dropped.
	got = resolveSelectedOptions(options, []string{optionHash("Pizza"), "deadbeef"})
	if len(got) != 2 || got[0] != "Pizza" || got[1] != "deadbeef" {
		t.Fatalf("resolved = %v, want [Pizza deadbeef]", got)
	}

	// No options stored: every hash falls back to itself.
	got = resolveSelectedOptions(nil, []string{"abc"})
	if len(got) != 1 || got[0] != "abc" {
		t.Fatalf("resolved = %v, want [abc]", got)
	}
}

type fakePollDecryptor struct {
	hashes []string
	err    error
}

func (f fakePollDecryptor) DecryptPollVote(ctx context.Context, sessionID string, evt any) ([]string, error) {
	return f.hashes, f.err
}

type fakePollOptions struct {
	options []string
}

func (f fakePollOptions) GetOptions(ctx context.Context, sessionID, pollMessageID string) ([]string, error) {
	return f.options, nil
}

func TestInboundNormalizer_PollVoteResolvesOptions(t *testing.T) {
	n := NewInboundNormalizer(
		fakePollDecryptor{hashes: []string{optionHash("Sushi")}},
		fakePollOptions{options: []string{"Pizza", "Sushi"}},
	)
	evt := &events.Message{
		Info: types.MessageInfo{
			ID: "VOTE1",
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("123-456", types.GroupServer),
				Sender: types.NewJID("789", types.HiddenUserServer),
			},
		},
		Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("POLL1")},
		}},
	}

	ev, nm, ok := n.Normalize(context.Background(), evt, "sess1", "org1")
	if !ok || nm == nil {
		t.Fatalf("normalize ok=%v nm=%v", ok, nm)
	}
	if nm.Kind != inbound.KindPollVote || nm.PollVote == nil {
		t.Fatalf("kind=%v pollVote=%v", nm.Kind, nm.PollVote)
	}
	if got := string(nm.PollVote.SelectedOptions); got != `["Sushi"]` {
		t.Fatalf("nm selected = %s, want [\"Sushi\"]", got)
	}
	mp, ok := ev.Payload.(apitypes.MessagePayload)
	if !ok {
		t.Fatalf("payload type = %T", ev.Payload)
	}
	if len(mp.SelectedOptions) != 1 || mp.SelectedOptions[0] != "Sushi" {
		t.Fatalf("payload selected = %v, want [Sushi]", mp.SelectedOptions)
	}
}

func TestInboundMessageFromEventsMessage_LIDSenderAndGroupAccounting(t *testing.T) {
	payload := waevents.MessagePayload{
		WAMessageID:     "MSG_SYNTHETIC_GROUP_TEXT",
		ChatJID:         "group-test@g.us",
		SenderJID:       "sender-test@lid",
		FromMe:          false,
		Type:            "text",
		Body:            "synthetic group text",
		QuotedMessageID: "MSG_SYNTHETIC_QUOTED",
		HasMedia:        false,
		Timestamp:       1782554804000,
		PushName:        "Synthetic Sender",
	}
	ev := domain.NewEvent(domain.EventMessage, "sess_1", "org_1", payload)

	nm := inboundMessageFromEventsMessage(&waevents.NormalizedMessage{
		WAMessageID:     payload.WAMessageID,
		ChatJID:         payload.ChatJID,
		ChatClass:       waevents.ChatClassGroup,
		SenderJID:       payload.SenderJID,
		FromMe:          payload.FromMe,
		PushName:        payload.PushName,
		Timestamp:       payload.Timestamp,
		Subtype:         waevents.SubtypeText,
		MessageType:     payload.Type,
		Body:            payload.Body,
		QuotedMessageID: payload.QuotedMessageID,
	}, inbound.KindMessage, ev, "sess_1", "org_1")

	if nm.SenderLID != "sender-test@lid" {
		t.Fatalf("SenderLID = %q", nm.SenderLID)
	}
	if nm.SenderJID != "" {
		t.Fatalf("SenderJID = %q, want empty when sender is LID-only", nm.SenderJID)
	}
	if !nm.IsGroup || nm.Group == nil || nm.Group.GroupJID != payload.ChatJID {
		t.Fatalf("group capture missing: isGroup=%v group=%+v", nm.IsGroup, nm.Group)
	}
	// A message records membership only; the push name belongs to the identity, so
	// the per-group tag stays empty (filled by backfill / group-info).
	if len(nm.Members) != 1 || nm.Members[0].LID != nm.SenderLID || nm.Members[0].Tag != "" {
		t.Fatalf("members = %+v", nm.Members)
	}
	if nm.Body != "synthetic group text" || nm.QuotedMessageID != "MSG_SYNTHETIC_QUOTED" {
		t.Fatalf("message fields body=%q quoted=%q", nm.Body, nm.QuotedMessageID)
	}
	var raw waevents.MessagePayload
	if err := json.Unmarshal(nm.RawJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.SenderJID != payload.SenderJID || raw.Body != payload.Body {
		t.Fatalf("raw payload = %+v", raw)
	}
}

func TestSplitSenderIDs_PhoneAndLID(t *testing.T) {
	lid, phoneJID := splitSenderIDs("777@lid", "628222@s.whatsapp.net")
	if lid != "777@lid" || phoneJID != "628222@s.whatsapp.net" {
		t.Fatalf("split alt lid = %q %q", lid, phoneJID)
	}

	lid, phoneJID = splitSenderIDs("", "sender-test@lid")
	if lid != "sender-test@lid" || phoneJID != "" {
		t.Fatalf("split primary lid = %q %q", lid, phoneJID)
	}
}

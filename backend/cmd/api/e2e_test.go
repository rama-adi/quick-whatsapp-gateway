package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/config"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/localmysql"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	e2eOrgA          = "e2e-org-a"
	e2eOrgB          = "e2e-org-b"
	e2eSessionID     = "e2e-session-a"
	e2eDeviceJID     = "6281111111111@s.whatsapp.net"
	e2eDeviceLID     = "111111111111@lid"
	e2eGroupJID      = "120363000000000001@g.us"
	e2eSenderLID     = "222222222222@lid"
	e2eQuoteWAID     = "e2e-quoted-image"
	e2eTextQuoteWAID = "e2e-quoted-text"
	e2eOrgAKey       = "e2e-key-organization-a"
	e2eOrgBKey       = "e2e-key-organization-b"
)

func TestOutboundE2E(t *testing.T) {
	infra := e2eStartInfra(t)
	t.Log("disposable MySQL and Redis ready")
	adminToken := setupE2EJWT(t, infra)
	external := setupE2EExternalServices(t, infra)
	infra.startAPI(t)
	t.Log("API ready")
	seedE2EAuth(t, infra.db)
	t.Log("API keys seeded")
	gateway := infra.startGateway(t)
	t.Log("gateway ready")
	t.Run("reply to group image carries quoted author and image context", func(t *testing.T) {
		ws := infra.realtimeSession(t, e2eOrgAKey)
		defer ws.CloseNow()
		status, result := infra.send(t, e2eOrgAKey, "reply-image-1", domain.SendRequest{
			Type:    domain.SendTypeText,
			To:      e2eGroupJID,
			Text:    "answer to image",
			ReplyTo: e2eQuoteWAID,
		})
		if status != http.StatusOK || result.WAMessageID == "" || result.Replayed {
			t.Fatalf("reply result: status=%d result=%+v", status, result)
		}
		captures := gateway.waitCaptureCount(t, 1)
		message := &waE2E.Message{}
		if err := protojson.Unmarshal(captures[0].Message, message); err != nil {
			t.Fatal(err)
		}
		context := message.GetExtendedTextMessage().GetContextInfo()
		if context.GetStanzaID() != e2eQuoteWAID || context.GetParticipant() != e2eSenderLID {
			t.Fatalf("quoted context = %+v", context)
		}
		if context.GetQuotedMessage().GetImageMessage().GetCaption() != "quoted image caption" {
			t.Fatalf("quoted image context = %+v", context.GetQuotedMessage())
		}
		if captures[0].To != e2eGroupJID || captures[0].ID != result.WAMessageID {
			t.Fatalf("WA capture/result mismatch: %+v %+v", captures[0], result)
		}
		event := e2eReadEvent(t, ws, domain.EventMessageFromMe)
		payload, ok := event.Payload.(map[string]any)
		if !ok || event.Organization != e2eOrgA || event.Session != e2eSessionID ||
			payload["waMessageId"] != result.WAMessageID || payload["quotedMessageId"] != e2eQuoteWAID {
			t.Fatalf("realtime sent event = %+v", event)
		}
		readStatus, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
		if readStatus != http.StatusOK {
			t.Fatalf("history status = %d", readStatus)
		}
		var recorded bool
		for _, message := range messages {
			if message.WAMessageID == result.WAMessageID {
				recorded = message.FromMe && message.Direction == domain.DirectionOut &&
					message.QuotedMessageID != nil && *message.QuotedMessageID == e2eQuoteWAID
			}
		}
		if !recorded {
			t.Fatalf("successful reply missing from HTTP history: %+v", messages)
		}
		var storedState, storedWAID string
		var storedUpdatedAt int64
		if err := infra.db.QueryRow(`SELECT status, wa_message_id, updated_at FROM outbox
			WHERE organization_id=? AND idempotency_key=?`, e2eOrgA, "reply-image-1").
			Scan(&storedState, &storedWAID, &storedUpdatedAt); err != nil {
			t.Fatal(err)
		}
		if storedState != "sent" || storedWAID != result.WAMessageID || storedUpdatedAt <= 0 {
			t.Fatalf("sent outbox state=%q waID=%q updatedAt=%d", storedState, storedWAID, storedUpdatedAt)
		}
		status, replay := infra.send(t, e2eOrgAKey, "reply-image-1", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "answer to image", ReplyTo: e2eQuoteWAID,
		})
		if status != http.StatusOK || !replay.Replayed || replay.WAMessageID != result.WAMessageID {
			t.Fatalf("idempotent replay: status=%d result=%+v", status, replay)
		}
		if captures := gateway.getCaptures(t); len(captures) != 1 {
			t.Fatalf("idempotent replay dispatched %d WA sends", len(captures))
		}
	})
	runE2EQuoteVariants(t, infra, gateway)
	t.Run("cross organization send and history are hidden", func(t *testing.T) {
		before := len(gateway.getCaptures(t))
		status, _ := infra.send(t, e2eOrgBKey, "forged-tenant", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "forged", ReplyTo: e2eQuoteWAID,
		})
		if status != http.StatusNotFound {
			t.Fatalf("cross-org send status = %d", status)
		}
		readStatus, messages := infra.messages(t, e2eOrgBKey, e2eGroupJID)
		if readStatus != http.StatusNotFound || len(messages) != 0 {
			t.Fatalf("cross-org history = status %d messages %+v", readStatus, messages)
		}
		if captures := gateway.getCaptures(t); len(captures) != before {
			t.Fatalf("cross-org send reached WhatsApp: %+v", captures)
		}
	})
	runE2ESendTypes(t, infra, gateway)
	runE2EAsyncSend(t, infra, gateway)
	t.Run("message operations", func(t *testing.T) {
		runE2EMessageOperations(t, infra, gateway)
	})
	runE2EOwnEcho(t, infra, gateway)
	runE2EEchoBeforeProjection(t, infra, gateway)
	runE2EDelayedReceipts(t, infra, gateway)
	runE2ESendFaults(t, infra, gateway)
	runE2EConcurrentReplay(t, infra, gateway)
	t.Run("projection failure after acknowledgement recovers without another WhatsApp send", func(t *testing.T) {
		if _, err := infra.db.Exec(`CREATE TRIGGER e2e_reject_sent_projection
			BEFORE INSERT ON messages FOR EACH ROW
			SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='e2e projection fault'`); err != nil {
			t.Fatal(err)
		}
		before := len(gateway.getCaptures(t))
		status, _ := infra.send(t, e2eOrgAKey, "projection-fault-1", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "projection recovery",
		})
		captures := gateway.waitCaptureCount(t, before+1)
		waID := captures[len(captures)-1].ID
		var prematureHistory, prematureEvent int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM messages
			WHERE session_id=? AND wa_message_id=?`, e2eSessionID, waID).Scan(&prematureHistory); err != nil {
			t.Fatal(err)
		}
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE organization_id=?
			AND session_id=? AND type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.waMessageId'))=?`,
			e2eOrgA, e2eSessionID, domain.EventMessageFromMe, waID).Scan(&prematureEvent); err != nil {
			t.Fatal(err)
		}
		if prematureHistory != 0 || prematureEvent != 0 {
			t.Fatalf("projection fault leaked %d history rows and %d sent events", prematureHistory, prematureEvent)
		}
		if _, err := infra.db.Exec(`DROP TRIGGER e2e_reject_sent_projection`); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "recover sent projection after MySQL fault", func() bool {
			var outboxStatus string
			var historyCount int
			if err := infra.db.QueryRowContext(ctx,
				`SELECT status FROM outbox WHERE organization_id=? AND idempotency_key=?`,
				e2eOrgA, "projection-fault-1").Scan(&outboxStatus); err != nil {
				return false
			}
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, waID).Scan(&historyCount); err != nil {
				return false
			}
			if outboxStatus == "sent" && historyCount == 0 {
				t.Fatalf("terminal sent outbox has no message after projection fault; status=%d", status)
			}
			return outboxStatus == "sent" && historyCount == 1
		})
		if captures := gateway.getCaptures(t); len(captures) != before+1 {
			t.Fatalf("projection recovery redispatched WhatsApp: %+v", captures)
		}
		if status == http.StatusOK {
			// A synchronous acknowledgement is legal only if the projection also
			// recovered before the response. The SQL trigger made that impossible.
			t.Fatal("API reported success while the sent projection was rejected")
		}
	})
	runE2EAPIRestartRecovery(t, infra, gateway)
	runE2ELostGatewayResponse(t, infra, gateway)
	runE2ELostEventAck(t, infra, gateway)
	runE2EResourceScenarios(t, infra, gateway, adminToken)
	runE2EStreamScenarios(t, infra)
	runE2EStickers(t, infra, gateway)
	runE2EExternalScenarios(t, infra, gateway, external)
	runE2EOIDCScenarios(t, infra, gateway, adminToken)
	runE2EPublicGRPCScenarios(t, infra, gateway)
	runE2EAdminGateways(t, infra, gateway, adminToken)
}

type e2eGatewayEnrollment struct {
	id     string
	token  string
	caPath string
}

func createE2EGatewayEnrollment(t *testing.T, db *sql.DB) e2eGatewayEnrollment {
	t.Helper()
	getenv := func(name string) string {
		switch name {
		case "PKI_ENCRYPTION_KEY":
			return "BAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQ="
		case "PKI_ENCRYPTION_KEY_ID":
			return "e2e"
		default:
			return ""
		}
	}
	cfg, err := config.LoadPKIWith(getenv)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.Policy()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := localmysql.New(db, localmysql.Config{
		KEK:             cfg.EncryptionKey,
		KeyID:           cfg.EncryptionKeyID,
		RootTTL:         cfg.RootTTL,
		IntermediateTTL: cfg.IntermediateTTL,
		RenewBefore:     cfg.IntermediateRenewBefore,
		Policy:          policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.EnsureHierarchy(context.Background()); err != nil {
		t.Fatal(err)
	}
	issuedBy, err := service.NewEnrollmentService(db, signer, service.DefaultEnrollmentConfig())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := issuedBy.CreateGateway(context.Background(), service.CreateGatewayInput{
		CreatedByUserID: "e2e-operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := signer.TrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bundle, []byte("BEGIN CERTIFICATE")) {
		t.Fatal("invalid bootstrap CA")
	}
	caPath := filepath.Join(t.TempDir(), "bootstrap-ca.pem")
	if err := os.WriteFile(caPath, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	return e2eGatewayEnrollment{id: issued.GatewayID, token: issued.Token, caPath: caPath}
}

func seedE2EAuth(t *testing.T, db *sql.DB) {
	t.Helper()
	// The WA migration intentionally excludes frontend-owned Better Auth tables.
	// This fixture mirrors the columns the API reads from Better Auth's apikey.
	if _, err := db.Exec(`CREATE TABLE apikey (
		id VARCHAR(36) PRIMARY KEY,
		name TEXT NULL,
		reference_id VARCHAR(255) NOT NULL,
		` + "`key`" + ` VARCHAR(255) NOT NULL,
		enabled BOOLEAN DEFAULT true,
		expires_at TIMESTAMP(3) NULL,
		permissions TEXT NULL,
		last_request TIMESTAMP(3) NULL,
		created_at TIMESTAMP(3) NOT NULL,
		updated_at TIMESTAMP(3) NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []struct{ id, org, raw string }{
		{id: "e2e-key-a", org: e2eOrgA, raw: e2eOrgAKey},
		{id: "e2e-key-b", org: e2eOrgB, raw: e2eOrgBKey},
	} {
		_, err := db.Exec(`INSERT INTO apikey
			(id, reference_id, `+"`key`"+`, enabled, permissions, created_at, updated_at)
			VALUES (?, ?, ?, true, ?, NOW(3), NOW(3))`,
			key.id, key.org, authz.DefaultHasher().Hash(key.raw),
			`{"gateway":["read","send","manage","events"]}`,
		)
		if err != nil {
			t.Fatalf("seed %s: %v", key.id, err)
		}
	}
}

func seedE2ESession(t *testing.T, db *sql.DB, gatewayID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	// The rate limiter defines zero as unlimited. This suite exercises send
	// routing and replay; rate-budget behavior is tested separately.
	_, err := db.ExecContext(ctx,
		`INSERT INTO wa_sessions
		 (id, organization_id, gateway_id, status, wa_jid, wa_lid, rate_per_min,
		 rate_per_hour, created_at, updated_at)
		 VALUES (?, ?, ?, 'working', ?, ?, 0, 0, ?, ?)`,
		e2eSessionID, e2eOrgA, gatewayID, e2eDeviceJID, e2eDeviceLID, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO gateway_session_assignments
		 (session_id, gateway_id, assignment_epoch, created_at, updated_at)
		 VALUES (?, ?, 1, ?, ?)`,
		e2eSessionID, gatewayID, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE gateways SET desired_revision=desired_revision+1 WHERE id=?`, gatewayID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chats (session_id, chat_jid, type, last_message_at)
		 VALUES (?, ?, 'group', ?)`, e2eSessionID, e2eGroupJID, now,
	); err != nil {
		t.Fatal(err)
	}
	quoted := domain.Message{
		ID:          domain.NewMessageID(),
		SessionID:   e2eSessionID,
		WAMessageID: e2eQuoteWAID,
		ChatJID:     e2eGroupJID,
		SenderLID:   ptr(e2eSenderLID),
		FromMe:      false,
		Direction:   domain.DirectionIn,
		Type:        domain.SendTypeImage,
		Body:        ptr("quoted image caption"),
		HasMedia:    true,
		Timestamp:   now,
		CreatedAt:   now,
	}
	if err := store.NewMessageRepo(db).Upsert(ctx, quoted); err != nil {
		t.Fatal(fmt.Errorf("seed quoted image: %w", err))
	}
	quotedText := quoted
	quotedText.ID = domain.NewMessageID()
	quotedText.WAMessageID = e2eTextQuoteWAID
	quotedText.Type = domain.SendTypeText
	quotedText.Body = ptr("text trigger")
	quotedText.HasMedia = false
	if err := store.NewMessageRepo(db).Upsert(ctx, quotedText); err != nil {
		t.Fatal(fmt.Errorf("seed quoted text: %w", err))
	}
}

func ptr[T any](value T) *T { return &value }

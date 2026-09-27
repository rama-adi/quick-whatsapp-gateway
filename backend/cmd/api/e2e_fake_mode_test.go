package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// Unlike the private fault harness, this journey launches the shipped gateway
// command and selects its fake transport entirely through environment variables.
func runE2EFakeMode(t *testing.T, infra *e2eInfra, adminToken string) {
	t.Run("standalone fake mode pairing media groups restart and pressure", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		const number = "555999000001"
		const sender = "555999000002@s.whatsapp.net"
		peer := httptest.NewServer(fakewhatsapp.NewServer())
		defer peer.Close()
		defer func() {
			var evidence fakewhatsapp.State
			fakeModeRequest(t, peer.URL+"/v1/state", nil, &evidence)
			raw, _ := json.Marshal(evidence)
			t.Logf("fake-mode peer evidence: %s", raw)
		}()
		root := t.TempDir()
		binary := filepath.Join(root, "gateway")
		build := exec.CommandContext(ctx, "go", "-C", filepath.Join(infra.root, "backend"),
			"build", "-o", binary, "./cmd/gateway")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build real gateway: %v\n%s", err, output)
		}
		enrollment := createE2EGatewayEnrollment(t, infra.db)
		health, engine := e2eFreeAddr(t), e2eFreeAddr(t)
		logPath := filepath.Join(root, "gateway.log")
		log, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		var command *exec.Cmd
		var done chan error
		stop := func() {
			if command == nil {
				return
			}
			_ = command.Process.Signal(os.Interrupt)
			<-done
			command = nil
		}
		defer stop()
		defer func() {
			if t.Failed() {
				data, _ := os.ReadFile(logPath)
				t.Logf("real fake-mode gateway: %s", data)
			}
		}()
		start := func() {
			command = exec.CommandContext(ctx, binary)
			command.Dir = root // Do not load a developer's deploy/.env.
			command.Env = append(os.Environ(),
				"GATEWAY_ID="+enrollment.id,
				"GATEWAY_CONTROL_PLANE_ADDR="+infra.apiPrivate,
				"GATEWAY_BOOTSTRAP_CA_FILE="+enrollment.caPath,
				"GATEWAY_ENROLLMENT_TOKEN="+enrollment.token,
				"GATEWAY_CREDENTIAL_DIR="+filepath.Join(root, "credentials"),
				"GATEWAY_CERTIFICATE_RENEW_BEFORE=6h",
				"GATEWAY_HTTP_ADDR="+health,
				"GATEWAY_ENGINE_GRPC_ADDR="+engine,
				"GATEWAY_ENGINE_GRPC_ADVERTISE_ADDR="+engine,
				"GATEWAY_JOURNAL_PATH="+filepath.Join(root, "journal.db"),
				"WHATSMEOW_STORE_DSN=file:"+filepath.Join(root, "real-unused.db"),
				"WHATSAPP_FAKE_SERVER=true",
				"WHATSAPP_FAKE_SERVER_URL="+peer.URL,
				"WHATSAPP_FAKE_STORE_DSN=file:"+filepath.Join(root, "fake.db")+"?_pragma=foreign_keys(on)",
			)
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done = make(chan error, 1)
			go func(cmd *exec.Cmd, result chan<- error) { result <- cmd.Wait() }(command, done)
			e2eEventually(t, ctx, "real fake-mode gateway ready", func() bool {
				select {
				case err := <-done:
					command = nil
					t.Fatalf("real gateway exited: %v", err)
				default:
				}
				response, err := http.Get("http://" + health + "/readyz")
				if err != nil {
					return false
				}
				defer response.Body.Close()
				return response.StatusCode == http.StatusOK
			})
		}
		start()
		t.Log("real gateway ready with empty session inventory")
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number, fakewhatsapp.Config{Scenario: "healthy"}, nil)
		var created domain.WASession
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/sessions", e2eOrgAKey,
			map[string]any{"label": "Standalone fake phone", "start": false}, &created, nil), http.StatusCreated)
		if created.GatewayID != enrollment.id {
			t.Fatalf("session assigned to %s instead of real fake-mode gateway %s", created.GatewayID, enrollment.id)
		}
		base := "/api/v1/sessions/" + created.ID
		var code struct {
			Code string `json:"code"`
		}
		e2eRequireStatus(t, infra.request(t, "POST", base+"/pairing-code", e2eOrgAKey,
			map[string]string{"phone": number}, &code, nil), http.StatusOK)
		if code.Code == "" {
			t.Fatal("pairing code absent")
		}
		var state fakewhatsapp.State
		fakeModeRequest(t, peer.URL+"/v1/state", nil, &state)
		var key string
		for _, session := range state.Sessions {
			if session.Number == number && session.Pending {
				key = session.Key
			}
		}
		if key == "" {
			t.Fatal("pairing request did not reach independent peer")
		}
		fakeModeRequest(t, peer.URL+"/v1/sessions/"+key+"/confirm", fakewhatsapp.ConfirmRequest{Number: number}, nil)
		working := func() {
			e2eEventually(t, ctx, "public session working with fake identity", func() bool {
				select {
				case err := <-done:
					command = nil
					t.Fatalf("real gateway exited before paired session became working: %v", err)
				default:
				}
				var current domain.WASession
				status := infra.request(t, "GET", base, e2eOrgAKey, nil, &current, nil)
				target, err := store.NewGatewayRepo(infra.db).ResolveSessionEngineTarget(ctx, e2eOrgA, created.ID)
				return status == http.StatusOK && current.Status == domain.SessionWorking &&
					current.WAJID != nil && *current.WAJID == number+"@s.whatsapp.net" &&
					err == nil && target.GatewayID == enrollment.id && target.GRPCEndpoint == engine
			})
		}
		working()
		t.Log("pairing approved and persisted through public session state")
		send := func(id, text string) {
			var result outbound.SendResult
			status := infra.request(t, "POST", base+"/messages", e2eOrgAKey,
				domain.SendRequest{Type: domain.SendTypeText, To: sender, Text: text}, &result,
				map[string]string{"Idempotency-Key": id})
			if status != http.StatusOK {
				t.Logf("API diagnostics: %s", infra.apiOutput.String())
			}
			e2eRequireStatus(t, status, http.StatusOK)
			fakeModeRequest(t, peer.URL+"/v1/state", nil, &state)
			found := 0
			for _, capture := range state.Captures {
				if capture.ID == result.WAMessageID && capture.To == sender {
					found++
				}
			}
			if found != 1 {
				t.Fatalf("public send did not produce one peer commit: %s count=%d", result.WAMessageID, found)
			}
		}
		send("fake-mode-control", "real gateway to fake recipient")
		t.Log("public send committed at the independent peer")
		var group struct {
			JID string `json:"jid"`
		}
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number+"/groups",
			map[string]any{"name": "Fake group", "participants": []string{number, "555999000002"}}, &group)
		// Group metadata follows the gateway's existing operator backfill flow;
		// group.update events are delivered but do not create stored group rows.
		adminHeaders := map[string]string{"Authorization": "Bearer " + adminToken}
		var backfill domain.BackfillJob
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/admin/sessions/"+created.ID+":backfill", "",
			nil, &backfill, adminHeaders), http.StatusAccepted)
		e2eEventually(t, ctx, "fake group backfill completed", func() bool {
			status := infra.request(t, "GET", "/api/v1/admin/sessions/"+created.ID+"/backfill", "",
				nil, &backfill, adminHeaders)
			if backfill.Status == "failed" {
				t.Fatalf("public fake-group backfill failed: %+v", backfill)
			}
			return status == http.StatusOK && backfill.Status == "succeeded"
		})
		e2eEventually(t, ctx, "fake group imported through real gateway", func() bool {
			var groups struct {
				Data []domain.Group `json:"data"`
			}
			if infra.request(t, "GET", base+"/groups", e2eOrgAKey, nil, &groups, nil) != http.StatusOK {
				return false
			}
			for _, row := range groups.Data {
				if row.GroupJID == group.JID && row.Subject != nil && *row.Subject == "Fake group" {
					return true
				}
			}
			return false
		})
		t.Log("fake group metadata and membership imported through public backfill")
		var imageBytes bytes.Buffer
		if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
			t.Fatal(err)
		}
		var incoming struct {
			ID string `json:"id"`
		}
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number+"/send", map[string]any{
			"chat": group.JID, "from": sender, "kind": "image", "text": "A fake photo",
			"mime_type": "image/png", "filename": "photo.png", "data_base64": base64.StdEncoding.EncodeToString(imageBytes.Bytes()),
		}, &incoming)
		messagePath := base + "/chats/" + url.PathEscape(group.JID) + "/messages"
		e2eEventually(t, ctx, "image from friendly composer in public history", func() bool {
			var page struct {
				Data []domain.Message `json:"data"`
			}
			infra.request(t, "GET", messagePath, e2eOrgAKey, nil, &page, nil)
			for _, message := range page.Data {
				if message.WAMessageID == incoming.ID && message.Type == domain.SendTypeImage && message.HasMedia {
					return true
				}
			}
			return false
		})
		t.Log("friendly group and image projected to public history")
		stickerBase64 := "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number+"/send", map[string]any{
			"chat": group.JID, "from": sender, "kind": "sticker",
			"mime_type": "image/webp", "filename": "sticker.webp", "data_base64": stickerBase64,
		}, &incoming)
		e2eEventually(t, ctx, "sticker download through real gateway and public API", func() bool {
			var sticker struct {
				Base64 string `json:"base64"`
			}
			status := infra.request(t, "GET", messagePath+"/"+incoming.ID+"/sticker",
				e2eOrgAKey, nil, &sticker, nil)
			return status == http.StatusOK && sticker.Base64 == stickerBase64
		})
		var catalog struct {
			Scenarios []fakewhatsapp.ScenarioInfo `json:"scenarios"`
		}
		fakeModeRequest(t, peer.URL+"/v1/scenarios", nil, &catalog)
		foundScenario := false
		for _, item := range catalog.Scenarios {
			foundScenario = foundScenario || item.ID == "conversation"
		}
		if !foundScenario {
			t.Fatal("typed conversation scenario is absent from the panel catalog")
		}
		var story fakewhatsapp.ScenarioResult
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number+"/scenarios/conversation", struct{}{}, &story)
		if story.FailedAt != nil || story.Delivered == 0 {
			t.Fatalf("typed scenario did not reach the gateway: %+v", story)
		}
		var storyGroup string
		for _, step := range story.Steps {
			if step.JID != "" {
				storyGroup = step.JID
			}
		}
		if storyGroup == "" {
			t.Fatal("conversation scenario did not create its group")
		}
		e2eEventually(t, ctx, "typed scenario messages projected through real gateway", func() bool {
			var direct, groupPage struct {
				Data []domain.Message `json:"data"`
			}
			infra.request(t, "GET", base+"/chats/555000002@s.whatsapp.net/messages", e2eOrgAKey, nil, &direct, nil)
			infra.request(t, "GET", base+"/chats/"+url.PathEscape(storyGroup)+"/messages", e2eOrgAKey, nil, &groupPage, nil)
			types := map[string]bool{}
			var helloID, quotedID string
			for _, message := range direct.Data {
				types[string(message.Type)] = true
				if message.Body != nil && *message.Body == "Hello from the fake phone" {
					helloID = message.WAMessageID
				}
				if message.Body != nil && *message.Body == "I can make it" && message.QuotedMessageID != nil {
					quotedID = *message.QuotedMessageID
				}
			}
			groupMessage := false
			for _, message := range groupPage.Data {
				groupMessage = groupMessage || (message.Body != nil && *message.Body == "Welcome to the fake team")
			}
			return types["text"] && types["location"] && types["contact"] && types["poll"] && groupMessage &&
				helloID != "" && quotedID == helloID
		})
		storyEvidence, _ := json.Marshal(story)
		t.Logf("typed scenario public projection passed: %s", storyEvidence)
		// One more than the journal's published batch entry contract exercises a
		// boundary crossing without introducing a made-up stress quota.
		count := journal.DefaultBatchEntries + 1
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number+"/firehose", fakewhatsapp.FirehoseRequest{
			MessageRequest: fakewhatsapp.MessageRequest{Chat: sender, Sender: sender,
				Message: json.RawMessage(`{"conversation":"fake-mode-burst"}`)}, Count: count,
		}, nil)
		e2eEventually(t, ctx, "all burst messages persisted once", func() bool {
			var rows int
			err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages WHERE session_id=? AND body=?`, created.ID, "fake-mode-burst").Scan(&rows)
			return err == nil && rows == count
		})
		t.Logf("burst assertion: %d peer events crossed journal batch boundary and persisted", count)
		stop()
		start()
		working()
		send("fake-mode-restarted", "paired identity survives gateway restart")
		e2eRequireStatus(t, infra.request(t, "PATCH", base+"/groups/"+url.PathEscape(group.JID),
			e2eOrgAKey, map[string]string{"subject": "Group after gateway restart"}, nil, nil), http.StatusNoContent)
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number,
			fakewhatsapp.Config{Scenario: "connect_error"}, nil)
		e2eEventually(t, ctx, "connection fault visible through public session state", func() bool {
			var current domain.WASession
			infra.request(t, "GET", base, e2eOrgAKey, nil, &current, nil)
			return current.Status != domain.SessionWorking
		})
		fakeModeRequest(t, peer.URL+"/v1/numbers/"+number,
			fakewhatsapp.Config{Scenario: "healthy"}, nil)
		working()
		send("fake-mode-recovered", "healthy after connection fault")
		if _, err := os.Stat(filepath.Join(root, "real-unused.db")); !os.IsNotExist(err) {
			t.Fatalf("fake mode touched the real keystore path: %v", err)
		}
		t.Log("assertions passed: real binary pairing, outbound peer commit, group/image projection, burst, restart, recovery, separate keystore")
	})
}

func fakeModeRequest(t *testing.T, endpoint string, body, output any) {
	t.Helper()
	var input io.Reader
	method := http.MethodGet
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input, method = bytes.NewReader(encoded), http.MethodPost
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(response.Body)
		t.Fatal(fmt.Sprintf("fake peer %s %s: %d %s", method, endpoint, response.StatusCode, data))
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

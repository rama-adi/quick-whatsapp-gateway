package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

func runE2EConcurrentReplay(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("concurrent identical keys dispatch once while WhatsApp is blocked", func(t *testing.T) {
		// Two callers are necessary to make a race. Larger workloads are supplied
		// explicitly by the test operator, never inferred from a performance goal.
		callers := 2
		if raw := os.Getenv("QWG_E2E_RACE_CALLERS"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 2 {
				t.Fatal("QWG_E2E_RACE_CALLERS must be an integer >=2")
			}
			callers = value
		}
		before := len(gateway.getCaptures(t))
		gateway.fault(t, "block_send")
		defer gateway.fault(t, "none")
		request := domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "one raced send",
		}
		first := make(chan outbound.SendResult, 1)
		go func() {
			defer close(first)
			status, result := infra.send(t, e2eOrgAKey, "same-key-while-blocked", request)
			if status != http.StatusOK {
				t.Errorf("first raced send status = %d", status)
			}
			first <- result
		}()
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "WhatsApp send barrier", func() bool {
			response, err := http.Get(gateway.controlURL + "/state")
			if err != nil {
				return false
			}
			defer response.Body.Close()
			var state struct {
				Blocked int `json:"blocked"`
			}
			return json.NewDecoder(response.Body).Decode(&state) == nil && state.Blocked == 1
		})
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range callers - 1 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				status, result := infra.send(t, e2eOrgAKey, "same-key-while-blocked", request)
				if status != http.StatusAccepted || result.Mode != outbound.ModeAsync || !result.Replayed {
					t.Errorf("concurrent replay: status=%d result=%+v", status, result)
				}
			}()
		}
		close(start)
		wg.Wait()
		response, err := http.Post(gateway.controlURL+"/release", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("release blocked WhatsApp send: %d", response.StatusCode)
		}
		result, ok := <-first
		if !ok || result.WAMessageID == "" {
			t.Fatalf("first raced send result = %+v, ok=%t", result, ok)
		}
		captures := gateway.waitCaptureCount(t, before+1)
		if captures[len(captures)-1].ID != result.WAMessageID {
			t.Fatalf("raced capture/result mismatch: %+v %+v", captures, result)
		}
		var count int
		if err := infra.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM outbox WHERE organization_id=? AND idempotency_key=?`,
			e2eOrgA, "same-key-while-blocked",
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("race inserted %d outbox commands", count)
		}
	})
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"github.com/redis/go-redis/v9"
)

func e2eOutboxLookup(t *testing.T, infra *e2eInfra, session, key, credential string) (int, outbound.SendResult) {
	t.Helper()
	var result outbound.SendResult
	status := infra.request(t, http.MethodGet,
		"/api/v1/sessions/"+session+"/outbox/by-idempotency?key="+url.QueryEscape(key),
		credential, nil, &result, nil)
	return status, result
}

func e2eOutboxStatus(t *testing.T, infra *e2eInfra, id string) (string, sql.NullString, sql.NullInt64, int64) {
	t.Helper()
	var status string
	var waID sql.NullString
	var ack sql.NullInt64
	var stamp int64
	if err := infra.db.QueryRow(`SELECT status, wa_message_id, acknowledged_at, updated_at FROM outbox WHERE id=?`, id).
		Scan(&status, &waID, &ack, &stamp); err != nil {
		t.Fatal(err)
	}
	return status, waID, ack, stamp
}

// The lookup is an authorized observation of one existing command. It must not
// create rows or contact the fake WhatsApp recipient, even for a foreign key.
func runE2EOutboxContract(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	t.Run("idempotency lookup scopes tenant and session without effects", func(t *testing.T) {
		const key = "lookup-sent-one"
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "lookup receipt"}
		status, sent := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusOK || sent.WAMessageID == "" {
			t.Fatalf("initial send = %d %+v", status, sent)
		}
		var beforeRows int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&beforeRows); err != nil {
			t.Fatal(err)
		}
		beforeCaptures := len(gateway.getCaptures(t))
		beforeAttempts := gateway.fakeState(t).Attempts
		var beforeAssignments int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM gateway_session_assignments`).Scan(&beforeAssignments); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/sessions/" + e2eSessionID + "/outbox/by-idempotency?key=" + url.QueryEscape(key)
		var found outbound.SendResult
		status = infra.request(t, http.MethodGet, path, e2eOrgAKey, nil, &found, nil)
		if status != http.StatusOK || found.OutboxID != sent.OutboxID || found.WAMessageID != sent.WAMessageID ||
			found.Mode != outbound.ModeSync || found.Status != domain.MessageSent {
			t.Fatalf("lookup = %d %+v, send %+v", status, found, sent)
		}
		for _, probe := range []struct{ path, credential string }{
			{path, e2eOrgBKey},
			{"/api/v1/sessions/missing-session/outbox/by-idempotency?key=" + url.QueryEscape(key), e2eOrgAKey},
			{"/api/v1/sessions/" + e2eSessionID + "/outbox/by-idempotency?key=missing-key", e2eOrgAKey},
		} {
			var body domain.ErrorBody
			if got := infra.request(t, http.MethodGet, probe.path, probe.credential, nil, &body, nil); got != http.StatusNotFound {
				t.Fatalf("lookup %s returned %d: %+v", probe.path, got, body)
			}
		}
		for _, probe := range []struct {
			credential string
			status     int
		}{
			{"", http.StatusUnauthorized},
			{e2eReadOnlyKey, http.StatusForbidden},
		} {
			var body domain.ErrorBody
			if got := infra.request(t, http.MethodGet, path, probe.credential, nil, &body, nil); got != probe.status {
				t.Fatalf("lookup credential returned %d, want %d: %+v", got, probe.status, body)
			}
		}
		// A valid second session within the same organization must not expose the
		// first session's key, even though outbox keys are unique per organization.
		if _, err := infra.db.Exec(`INSERT INTO wa_sessions
			(id, organization_id, gateway_id, status, rate_per_min, rate_per_hour, created_at, updated_at)
			SELECT 'e2e-session-other', organization_id, gateway_id, 'working', 0, 0, created_at, updated_at
			FROM wa_sessions WHERE id=?`, e2eSessionID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = infra.db.Exec(`DELETE FROM wa_sessions WHERE id='e2e-session-other'`) })
		var foreign domain.ErrorBody
		if got := infra.request(t, http.MethodGet,
			"/api/v1/sessions/e2e-session-other/outbox/by-idempotency?key="+url.QueryEscape(key),
			e2eOrgAKey, nil, &foreign, nil); got != http.StatusNotFound {
			t.Fatalf("same-org foreign session lookup = %d %+v", got, foreign)
		}
		var afterRows int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&afterRows); err != nil {
			t.Fatal(err)
		}
		var afterAssignments int
		if err := infra.db.QueryRow(`SELECT COUNT(*) FROM gateway_session_assignments`).Scan(&afterAssignments); err != nil {
			t.Fatal(err)
		}
		if afterRows != beforeRows || afterAssignments != beforeAssignments ||
			len(gateway.getCaptures(t)) != beforeCaptures || gateway.fakeState(t).Attempts != beforeAttempts {
			t.Fatalf("lookup changed rows/assignments/recipient attempts: rows %d->%d assignments %d->%d captures %d->%d attempts %d->%d",
				beforeRows, afterRows, beforeAssignments, afterAssignments, beforeCaptures, len(gateway.getCaptures(t)),
				beforeAttempts, gateway.fakeState(t).Attempts)
		}
	})

	t.Run("replay cannot cross session ownership before key lookup", func(t *testing.T) {
		const key = "auth-before-replay-send"
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "owned send"}
		status, original := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusOK || original.OutboxID == "" {
			t.Fatalf("initial send = %d %+v", status, original)
		}
		var body domain.ErrorBody
		status = infra.request(t, http.MethodPost, "/api/v1/sessions/missing-session/messages", e2eOrgAKey,
			request, &body, map[string]string{"Idempotency-Key": key})
		if status != http.StatusNotFound {
			t.Fatalf("foreign session replay = %d %+v", status, body)
		}
	})

	t.Run("reaction replay checks authority and documents pending success", func(t *testing.T) {
		status, source := infra.send(t, e2eOrgAKey, "reaction-auth-source", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "reaction authority source",
		})
		if status != http.StatusOK || source.WAMessageID == "" {
			t.Fatalf("source send = %d %+v", status, source)
		}
		gateway.fault(t, "block_send")
		defer gateway.fault(t, "none")
		const key = "reaction-auth-pending"
		path := "/api/v1/sessions/" + e2eSessionID + "/messages/" + source.WAMessageID + "/reaction"
		body := map[string]any{"chat": e2eGroupJID, "emoji": "👍"}
		finished := make(chan int, 1)
		go func() {
			var result json.RawMessage
			finished <- infra.request(t, http.MethodPost, path, e2eOrgAKey,
				body, &result, map[string]string{"Idempotency-Key": key})
		}()
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "first reaction held before acknowledgement", func() bool {
			return gateway.fakeState(t).Blocked > 0
		})
		var pending outbound.SendResult
		if got := infra.request(t, http.MethodPost, path, e2eOrgAKey, body, &pending,
			map[string]string{"Idempotency-Key": key}); got != http.StatusAccepted ||
			pending.Mode != outbound.ModeAsync || !pending.Replayed {
			t.Fatalf("pending reaction replay = %d %+v", got, pending)
		}
		if got, lookup := e2eOutboxLookup(t, infra, e2eSessionID, key, e2eOrgAKey); got != http.StatusAccepted || lookup.Mode != outbound.ModeAsync ||
			lookup.OutboxID != pending.OutboxID {
			t.Fatalf("pending reaction lookup = %d %+v", got, lookup)
		}
		before := gateway.fakeState(t)
		for _, probe := range []struct {
			session, credential string
			want                int
		}{
			{"missing-session", e2eOrgAKey, http.StatusNotFound},
			{e2eSessionID, e2eOrgBKey, http.StatusNotFound},
			{e2eSessionID, e2eReadOnlyKey, http.StatusForbidden},
			{e2eSessionID, "", http.StatusUnauthorized},
		} {
			var result json.RawMessage
			got := infra.request(t, http.MethodPost,
				"/api/v1/sessions/"+probe.session+"/messages/"+source.WAMessageID+"/reaction",
				probe.credential, body, &result, map[string]string{"Idempotency-Key": key})
			if got != probe.want {
				t.Fatalf("unauthorized reaction replay for %s = %d, want %d: %s",
					probe.session, got, probe.want, result)
			}
		}
		if after := gateway.fakeState(t); after.Attempts != before.Attempts ||
			len(after.Captures) != len(before.Captures) {
			t.Fatalf("unauthorized replay reached recipient: before=%+v after=%+v", before.AttemptTrace, after.AttemptTrace)
		}
		response, err := http.Post(gateway.fakeURL+"/v1/sessions/"+gateway.fakeSessionKey(t)+"/release",
			"application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("release held reaction = %d", response.StatusCode)
		}
		select {
		case got := <-finished:
			if got != http.StatusOK {
				t.Fatalf("acknowledged reaction = %d", got)
			}
		case <-ctx.Done():
			t.Fatalf("reaction did not finish: %v", ctx.Err())
		}
		// Ownership can change after acceptance. A replay of the original key
		// must still authorize the current session before reading that row.
		if _, err := infra.db.ExecContext(ctx,
			`UPDATE wa_sessions SET organization_id=? WHERE id=?`, e2eOrgB, e2eSessionID); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = infra.db.Exec(`UPDATE wa_sessions SET organization_id=? WHERE id=?`, e2eOrgA, e2eSessionID)
		}()
		var denied json.RawMessage
		if got := infra.request(t, http.MethodPost, path, e2eOrgAKey, body, &denied,
			map[string]string{"Idempotency-Key": key}); got != http.StatusNotFound {
			t.Fatalf("reaction replay after ownership change = %d %s", got, denied)
		}
		if _, err := infra.db.ExecContext(ctx,
			`UPDATE wa_sessions SET organization_id=? WHERE id=?`, e2eOrgA, e2eSessionID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acknowledged button header is not fetched again for media capture", func(t *testing.T) {
		var downloads atomic.Int32
		header := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if downloads.Add(1) != 1 {
				w.WriteHeader(http.StatusGone)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("button header fixture"))
		}))
		defer header.Close()
		before := len(gateway.getCaptures(t))
		status, sent := infra.send(t, e2eOrgAKey, "button-header-one-fetch", domain.SendRequest{
			Type: domain.SendTypeButtons, To: e2eGroupJID, Text: "Choose",
			Buttons:     []domain.ReplyButton{{ID: "yes", Title: "Yes"}},
			HeaderImage: &domain.MediaPayload{URL: header.URL, Mimetype: "image/png"},
		})
		if status != http.StatusOK || sent.Status != domain.MessageSent ||
			len(gateway.getCaptures(t)) != before+1 || downloads.Load() != 1 {
			t.Fatalf("button send/header fetch = %d %+v captures=%d downloads=%d",
				status, sent, len(gateway.getCaptures(t))-before, downloads.Load())
		}
	})

	t.Run("acknowledged image captures its prepared one-time URL bytes", func(t *testing.T) {
		var downloads atomic.Int32
		mediaURL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if downloads.Add(1) != 1 {
				w.WriteHeader(http.StatusGone)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("one-time image fixture"))
		}))
		defer mediaURL.Close()
		before := len(gateway.getCaptures(t))
		status, sent := infra.send(t, e2eOrgAKey, "image-one-time-url", domain.SendRequest{
			Type: domain.SendTypeImage, To: e2eGroupJID,
			Media: &domain.MediaPayload{URL: mediaURL.URL, Mimetype: "image/png"},
		})
		if status != http.StatusOK || sent.Status != domain.MessageSent ||
			len(gateway.getCaptures(t)) != before+1 || downloads.Load() != 1 {
			t.Fatalf("image send/source fetch = %d %+v captures=%d downloads=%d",
				status, sent, len(gateway.getCaptures(t))-before, downloads.Load())
		}
	})

	t.Run("accepted expiry is immutable and queued command never reaches recipient", func(t *testing.T) {
		const key = "queued-expiry-one"
		expiresAt := time.Now().Add(300 * time.Millisecond).UnixMilli()
		request := domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID,
			Text: "must expire in queue", ExpiresAt: &expiresAt}
		before := len(gateway.getCaptures(t))
		var accepted outbound.SendResult
		status := infra.request(t, http.MethodPost, "/api/v1/sessions/"+e2eSessionID+"/messages?async=true",
			e2eOrgAKey, request, &accepted, map[string]string{"Idempotency-Key": key})
		if status != http.StatusAccepted || accepted.OutboxID == "" {
			t.Fatalf("async acceptance = %d %+v", status, accepted)
		}
		if status, queued := e2eOutboxLookup(t, infra, e2eSessionID, key, e2eOrgAKey); status != http.StatusAccepted ||
			queued.Mode != outbound.ModeAsync || queued.OutboxID != accepted.OutboxID {
			t.Fatalf("queued lookup = %d %+v", status, queued)
		}
		later := time.Now().Add(time.Hour).UnixMilli()
		request.ExpiresAt = &later
		status, replay := infra.send(t, e2eOrgAKey, key, request)
		if status != http.StatusAccepted || !replay.Replayed || replay.OutboxID != accepted.OutboxID {
			t.Fatalf("changed-deadline replay = %d %+v", status, replay)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "queued expiry settlement", func() bool {
			var state string
			var saved sql.NullInt64
			err := infra.db.QueryRowContext(ctx, `SELECT status,expires_at FROM outbox WHERE id=?`, accepted.OutboxID).
				Scan(&state, &saved)
			return err == nil && state == string(domain.OutboxFailed) && saved.Valid && saved.Int64 == expiresAt
		})
		if got := len(gateway.getCaptures(t)); got != before {
			t.Fatalf("expired queued command reached recipient: %d->%d", before, got)
		}
		if status, failed := e2eOutboxLookup(t, infra, e2eSessionID, key, e2eOrgAKey); status != http.StatusOK ||
			failed.Mode != outbound.ModeSync || failed.Status != domain.MessageFailed || failed.OutboxID != accepted.OutboxID {
			t.Fatalf("expired lookup = %d %+v", status, failed)
		}
	})

	t.Run("rate limited queued command expires before next retry", func(t *testing.T) {
		const key = "rate-limited-expiry"
		options, err := redis.ParseURL(infra.redisURL)
		if err != nil {
			t.Fatal(err)
		}
		limiterRedis := redis.NewClient(options)
		defer func() { _ = limiterRedis.Close() }()
		if err := limiterRedis.Del(context.Background(),
			"wa:rl:"+e2eSessionID+":min", "wa:rl:"+e2eSessionID+":hour").Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := infra.db.Exec(`UPDATE wa_sessions SET rate_per_min=1 WHERE id=?`, e2eSessionID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = infra.db.Exec(`UPDATE wa_sessions SET rate_per_min=0 WHERE id=?`, e2eSessionID) })
		status, first := infra.send(t, e2eOrgAKey, "rate-limiter-primer", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "consume one rate token",
		})
		if status != http.StatusOK || first.WAMessageID == "" {
			t.Fatalf("rate primer = %d %+v", status, first)
		}
		before := gateway.fakeState(t)
		expiresAt := time.Now().Add(3 * time.Second).UnixMilli()
		var accepted outbound.SendResult
		status = infra.request(t, http.MethodPost, "/api/v1/sessions/"+e2eSessionID+"/messages?async=true",
			e2eOrgAKey, domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID,
				Text: "expired behind rate limiter", ExpiresAt: &expiresAt},
			&accepted, map[string]string{"Idempotency-Key": key})
		if status != http.StatusAccepted || accepted.OutboxID == "" {
			t.Fatalf("rate limited acceptance = %d %+v", status, accepted)
		}
		// The scheduler polls every 2s in this E2E composition. A 3s deadline
		// must settle on the next poll rather than wait for the minute budget.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		e2eEventually(t, ctx, "rate-limited outbox expiry settlement", func() bool {
			var state string
			return infra.db.QueryRowContext(ctx, `SELECT status FROM outbox WHERE id=?`, accepted.OutboxID).
				Scan(&state) == nil && state == string(domain.OutboxFailed)
		})
		after := gateway.fakeState(t)
		if len(after.Captures) != len(before.Captures) || after.Attempts != before.Attempts {
			t.Fatalf("rate-limited expired command reached fake recipient: before=%+v after=%+v", before.AttemptTrace, after.AttemptTrace)
		}
	})

	t.Run("offline gateway expiry remains a definite no-dispatch failure", func(t *testing.T) {
		const key = "offline-expiry"
		before := gateway.fakeState(t)
		gateway.stop()
		defer func() {
			if gateway.cmd == nil {
				gateway.run(t, infra)
			}
		}()
		expiresAt := time.Now().Add(6 * time.Second).UnixMilli()
		var accepted outbound.SendResult
		status := infra.request(t, http.MethodPost, "/api/v1/sessions/"+e2eSessionID+"/messages?async=true",
			e2eOrgAKey, domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID,
				Text: "offline and expires", ExpiresAt: &expiresAt}, &accepted,
			map[string]string{"Idempotency-Key": key})
		if status != http.StatusAccepted || accepted.OutboxID == "" {
			t.Fatalf("offline async acceptance = %d %+v", status, accepted)
		}
		// The 6s deadline plus the API's 2s poll bounds settlement; waiting for
		// the backoff/rate interval would leave a misleading pending result.
		ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
		defer cancel()
		e2eEventually(t, ctx, "offline expiry settlement", func() bool {
			var state string
			return infra.db.QueryRowContext(ctx, `SELECT status FROM outbox WHERE id=?`, accepted.OutboxID).
				Scan(&state) == nil && state == string(domain.OutboxFailed)
		})
		lookupStatus, settled := e2eOutboxLookup(t, infra, e2eSessionID, key, e2eOrgAKey)
		if lookupStatus != http.StatusOK || settled.Status != domain.MessageFailed {
			t.Fatalf("offline expiry lookup = %d %+v", lookupStatus, settled)
		}
		gateway.run(t, infra)
		if after := gateway.fakeState(t); len(after.Captures) != len(before.Captures) || after.Attempts != before.Attempts {
			t.Fatalf("offline command reached recipient after reconnect: before=%+v after=%+v",
				before.AttemptTrace, after.AttemptTrace)
		}
	})

	t.Run("queued batch checks each entry after an earlier engine wait", func(t *testing.T) {
		gateway.fault(t, "block_send")
		defer gateway.fault(t, "none")
		before := gateway.fakeState(t)
		firstExpiry := time.Now().Add(7 * time.Second).UnixMilli()
		secondExpiry := time.Now().Add(4 * time.Second).UnixMilli()
		queued := func(key, message string, expiresAt *int64) outbound.SendResult {
			var result outbound.SendResult
			status := infra.request(t, http.MethodPost, "/api/v1/sessions/"+e2eSessionID+"/messages?async=true",
				e2eOrgAKey, domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID,
					Text: message, ExpiresAt: expiresAt}, &result, map[string]string{"Idempotency-Key": key})
			if status != http.StatusAccepted {
				t.Fatalf("batch acceptance %s = %d %+v", key, status, result)
			}
			return result
		}
		first := queued("batch-wait-first", "blocked first", &firstExpiry)
		second := queued("batch-wait-second", "expires while first waits", &secondExpiry)
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "first command at fake peer barrier", func() bool {
			return gateway.fakeState(t).Blocked > 0
		})
		e2eEventually(t, ctx, "second batch entry expired without dispatch", func() bool {
			status, _, _, _ := e2eOutboxStatus(t, infra, second.OutboxID)
			return status == string(domain.OutboxFailed)
		})
		for _, attempt := range gateway.fakeState(t).AttemptTrace[len(before.AttemptTrace):] {
			if len(second.ReservedMessageIDs) > 0 && attempt.ID == second.ReservedMessageIDs[0] {
				t.Fatalf("expired second entry was attempted: %+v", attempt)
			}
		}
		if len(gateway.getCaptures(t)) != len(before.Captures) {
			t.Fatalf("expired batch reached recipient; first=%+v second=%+v", first, second)
		}
	})

	t.Run("reaction deadline cancels blocked engine effect", func(t *testing.T) {
		status, source := infra.send(t, e2eOrgAKey, "reaction-deadline-source", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "react source",
		})
		if status != http.StatusOK || source.WAMessageID == "" {
			t.Fatalf("reaction source = %d %+v", status, source)
		}
		gateway.fault(t, "block_send")
		defer gateway.fault(t, "none")
		before := gateway.fakeState(t)
		expiresAt := time.Now().Add(time.Second).UnixMilli()
		var result json.RawMessage
		status = infra.request(t, http.MethodPost,
			"/api/v1/sessions/"+e2eSessionID+"/messages/"+source.WAMessageID+"/reaction",
			e2eOrgAKey, map[string]any{"chat": e2eGroupJID, "emoji": "👍", "expiresAt": expiresAt},
			&result, map[string]string{"Idempotency-Key": "reaction-deadline"})
		if status != http.StatusServiceUnavailable {
			t.Fatalf("blocked reaction outcome = %d %s, want uncertain service response", status, result)
		}
		ctx, cancel := e2eContext(t)
		defer cancel()
		e2eEventually(t, ctx, "reaction attempt canceled at fake peer", func() bool {
			for _, attempt := range gateway.fakeState(t).AttemptTrace[len(before.AttemptTrace):] {
				if attempt.Outcome == "cancelled_before_commit" {
					return true
				}
			}
			return false
		})
		if len(gateway.getCaptures(t)) != len(before.Captures) {
			t.Fatalf("expired reaction committed to recipient: %v", gateway.getCaptures(t)[len(before.Captures):])
		}
		if status, unknown := e2eOutboxLookup(t, infra, e2eSessionID, "reaction-deadline", e2eOrgAKey); status != http.StatusOK ||
			unknown.Mode != outbound.ModeSync || unknown.Status != domain.MessageUnknown {
			t.Fatalf("uncertain reaction lookup = %d %+v", status, unknown)
		}
	})

	t.Run("stale owner cannot erase acknowledged sent row", func(t *testing.T) {
		status, sent := infra.send(t, e2eOrgAKey, "stale-owner-sent", domain.SendRequest{
			Type: domain.SendTypeText, To: e2eGroupJID, Text: "known success remains sent",
		})
		if status != http.StatusOK || sent.OutboxID == "" {
			t.Fatalf("sent source = %d %+v", status, sent)
		}
		state, waID, ack, stamp := e2eOutboxStatus(t, infra, sent.OutboxID)
		if state != string(domain.OutboxSent) || !waID.Valid || !ack.Valid {
			t.Fatalf("sent row = %s %+v %+v", state, waID, ack)
		}
		failedNote := "stale owner falsely failed"
		ctx, cancel := e2eContext(t)
		defer cancel()
		owned, err := store.NewOutboxRepo(infra.db).CompleteOwned(
			ctx, sent.OutboxID, stamp, domain.OutboxFailed, nil, &failedNote, time.Now().UnixMilli())
		if err != nil || owned {
			t.Fatalf("stale completion = %t %v", owned, err)
		}
		state, waID, ack, _ = e2eOutboxStatus(t, infra, sent.OutboxID)
		if state != string(domain.OutboxSent) || waID.String != sent.WAMessageID || !ack.Valid {
			t.Fatalf("stale owner erased acknowledged outcome: %s %+v %+v", state, waID, ack)
		}
	})
}

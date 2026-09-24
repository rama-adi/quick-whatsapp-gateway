package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
)

// This scenario runs last: it deliberately drains and revokes the real isolated
// gateway, then removes its test session and registry entry.
func runE2EAdminGateways(t *testing.T, infra *e2eInfra, gateway *e2eGateway, adminToken string) {
	t.Run("admin gateway lifecycle and enrollment secrets", func(t *testing.T) {
		defer func() {
			if t.Failed() {
				t.Logf("API diagnostics: %s", infra.apiOutput.String())
			}
		}()
		ctx, cancel := e2eContext(t)
		defer cancel()
		headers := map[string]string{"Authorization": "Bearer " + adminToken}
		const base = "/api/v1/admin/gateways"
		e2eRequireStatus(t, infra.request(t, "GET", base, e2eOrgAKey, nil, nil, nil), 403)
		var created apitypes.GatewayEnrollmentResult
		e2eRequireStatus(t, infra.request(t, "POST", base, "", map[string]any{"label": "isolated spare", "notes": "test lifecycle"}, &created, headers), 201)
		if created.Token == "" || created.GatewayID == "" {
			t.Fatal("gateway enrollment credentials missing")
		}
		spare := base + "/" + created.GatewayID
		var replaced apitypes.GatewayEnrollmentResult
		e2eRequireStatus(t, infra.request(t, "POST", spare+":replace-enrollment-token", "", nil, &replaced, headers), 201)
		if replaced.Token == "" || replaced.Token == created.Token || replaced.TokenID == created.TokenID {
			t.Fatal("replacement did not rotate enrollment bearer")
		}
		var raw json.RawMessage
		e2eRequireStatus(t, infra.request(t, "GET", spare, "", nil, &raw, headers), 200)
		if bytes.Contains(raw, []byte(created.Token)) || bytes.Contains(raw, []byte(replaced.Token)) {
			t.Fatal("enrollment bearer leaked through detail")
		}
		var detail apitypes.GatewayAdminDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Gateway.ID != created.GatewayID || len(detail.Audit) == 0 {
			t.Fatalf("gateway detail missing identity/audit: %+v", detail)
		}
		var listing apitypes.List[apitypes.GatewayAdmin]
		e2eRequireStatus(t, infra.request(t, "GET", base, "", nil, &listing, headers), 200)
		found := false
		for _, item := range listing.Data {
			found = found || item.ID == created.GatewayID
		}
		if !found {
			t.Fatal("created gateway absent from list")
		}
		e2eRequireStatus(t, infra.request(t, "DELETE", spare, "", map[string]any{"consequencesAcknowledged": false}, nil, headers), 409)
		e2eRequireStatus(t, infra.request(t, "POST", spare+":drain", "", nil, nil, headers), 409)
		e2eRequireStatus(t, infra.request(t, "POST", spare+":disable", "", nil, nil, headers), 204)
		e2eRequireStatus(t, infra.request(t, "POST", spare+":reenable", "", nil, nil, headers), 204)
		e2eRequireStatus(t, infra.request(t, "POST", spare+":disable", "", nil, nil, headers), 204)
		e2eRequireStatus(t, infra.request(t, "DELETE", spare, "", map[string]any{"consequencesAcknowledged": true}, nil, headers), 409)
		// Revoke the isolated unused credential to satisfy the deletion guard;
		// token revocation has no public API of its own.
		if _, err := infra.db.ExecContext(ctx, `UPDATE gateway_enrollment_tokens SET status='revoked',revoked_at=? WHERE gateway_id=? AND status='active'`, time.Now().UnixMilli()-1, created.GatewayID); err != nil {
			t.Fatal(err)
		}
		e2eRequireStatus(t, infra.request(t, "DELETE", spare, "", map[string]any{"consequencesAcknowledged": true}, nil, headers), 204)
		e2eRequireStatus(t, infra.request(t, "GET", spare, "", nil, nil, headers), 404)

		live := base + "/" + gateway.enrollment.id
		e2eRequireStatus(t, infra.request(t, "POST", live+":drain", "", nil, nil, headers), 204)
		e2eEventually(t, ctx, "gateway drain acknowledged by real supervisor", func() bool {
			return infra.request(t, "GET", live, "", nil, &detail, headers) == 200 && detail.Gateway.Status == "drained"
		})
		e2eRequireStatus(t, infra.request(t, "POST", live+":resume", "", nil, nil, headers), 204)
		// Production DRAIN is terminal for the manager. Resume requires a process
		// restart with the same keystore and credentials.
		gateway.stop()
		gateway.run(t, infra)
		e2eRequireStatus(t, infra.request(t, "POST", live+":disable", "", nil, nil, headers), 204)
		gateway.stop()
		e2eRequireStatus(t, infra.request(t, "POST", live+":reenable", "", nil, nil, headers), 204)
		gateway.run(t, infra)
		e2eRequireStatus(t, infra.request(t, http.MethodDelete, "/api/v1/sessions/"+e2eSessionID, e2eOrgAKey, nil, nil, nil), 204)
		e2eRequireStatus(t, infra.request(t, "POST", live+":disable", "", nil, nil, headers), 204)
		gateway.stop()
		var reenrolled apitypes.GatewayEnrollmentResult
		e2eRequireStatus(t, infra.request(t, "POST", live+":reenroll", "", nil, &reenrolled, headers), 201)
		if reenrolled.Token == "" || reenrolled.GatewayID != gateway.enrollment.id {
			t.Fatal("reenrollment did not issue replacement credential")
		}
		var usable int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_certificates WHERE gateway_id=? AND revoked_at IS NULL`, gateway.enrollment.id).Scan(&usable); err != nil {
			t.Fatal(err)
		}
		if usable != 0 {
			t.Fatalf("reenrollment retained %d usable old certificates", usable)
		}
		e2eRequireStatus(t, infra.request(t, "GET", live, "", nil, &raw, headers), 200)
		if bytes.Contains(raw, []byte(reenrolled.Token)) {
			t.Fatal("reenrollment secret leaked in read model")
		}
	})
}

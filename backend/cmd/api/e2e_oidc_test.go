package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/oidp"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	e2eOAuthRedirect = "https://example.org/e2e-oidc-callback"
	e2eOAuthPhoneJID = "6283333333333@s.whatsapp.net"
	e2eOAuthLID      = "333333333333@lid"
)

// runE2EOIDCScenarios uses only the mounted public protocol routes and the
// gateway's incoming-message hook. OAuth state, grants, and tokens stay in the
// child API's real Redis/MySQL stores; no provider implementation is replaced.
func runE2EOIDCScenarios(t *testing.T, infra *e2eInfra, gateway *e2eGateway, adminToken string) {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	if _, err := oidp.GenerateNextKey(ctx, store.NewOAuthSigningKeyRepo(infra.db), key, time.Now().UnixMilli()); err != nil {
		t.Fatalf("initialize disposable OIDC signing key: %v", err)
	}
	adminHeaders := map[string]string{"Authorization": "Bearer " + adminToken}
	var app apitypes.OAuthAppWithSecret
	e2eRequireStatus(t, infra.request(t, http.MethodPost, "/api/v1/oauth-apps", "",
		map[string]any{
			"sessionId": e2eSessionID, "name": "E2E protocol client",
			"clientType": "confidential", "loginCommand": "oidce2e",
			"redirectUris": []string{e2eOAuthRedirect}, "modes": []string{"dm"},
			"allowedScopes": []string{"openid", "profile", "phone", "offline_access"},
		}, &app, adminHeaders), http.StatusCreated)
	if app.ClientID == "" || app.ClientSecret == "" || app.LoginCommand != "oidce2e" {
		t.Fatal("OAuth client creation returned incomplete credentials")
	}
	t.Cleanup(func() {
		infra.request(t, http.MethodDelete, "/api/v1/oauth-apps/"+url.PathEscape(app.ID),
			"", nil, nil, adminHeaders)
	})

	t.Run("oauth discovery and signed code flow", func(t *testing.T) {
		e2eOIDCFlow(t, infra, gateway, app)
	})
	t.Run("oauth hostile inputs and cancellation", func(t *testing.T) {
		e2eOIDCHostile(t, infra, gateway, app)
	})
	t.Run("oauth grant listing and revocation", func(t *testing.T) {
		e2eOIDCGrants(t, infra, gateway, app, adminToken)
	})
}

func e2eOIDCGrants(t *testing.T, infra *e2eInfra, gateway *e2eGateway, app apitypes.OAuthAppWithSecret, adminToken string) {
	t.Helper()
	appPath := "/api/v1/oauth-apps/" + url.PathEscape(app.ID)
	adminHeaders := map[string]string{"Authorization": "Bearer " + adminToken}
	var apps apitypes.List[apitypes.OAuthApp]
	e2eRequireStatus(t, infra.request(t, http.MethodGet, "/api/v1/oauth-apps", "", nil, &apps, adminHeaders), http.StatusOK)
	foundApp := false
	for _, item := range apps.Data {
		foundApp = foundApp || item.ID == app.ID
	}
	if !foundApp {
		t.Fatal("OAuth app absent from administrator inventory")
	}
	var listed apitypes.List[apitypes.OAuthGrant]
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appPath+"/grants", "", nil, &listed, adminHeaders), http.StatusOK)
	if len(listed.Data) != 1 || listed.Data[0].Sub != e2eOAuthLID || listed.Data[0].RevokedAt != nil {
		t.Fatalf("OAuth grant list has wrong claimant or state: %+v", listed.Data)
	}
	grantID := listed.Data[0].ID
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appPath+"/grants", e2eOrgBKey, nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, appPath+"/grants/"+url.PathEscape(grantID),
		"", nil, nil, adminHeaders), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appPath+"/grants", "", nil, &listed, adminHeaders), http.StatusOK)
	if len(listed.Data) != 1 || listed.Data[0].RevokedAt == nil {
		t.Fatalf("individual grant revocation not visible: %+v", listed.Data)
	}

	verifier := "e2e-grant-renew-verifier"
	browserCode := e2eOIDCAuthorize(t, infra, app, verifier, "grant-renew-state", "")
	userCode := e2eOIDCWaitSnapshot(t, infra, browserCode, "pending")
	e2eOIDCSendInbound(t, gateway, "e2e-oauth-grant-renew", app.LoginCommand+" "+userCode)
	code := e2eOIDCFinalizeWhenClaimed(t, infra, browserCode, "grant-renew-state", "e2e-oauth-grant-renew")
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "code": {code},
		"redirect_uri": {e2eOAuthRedirect}, "code_verifier": {verifier},
	}
	var tokens map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", form, nil, &tokens, http.StatusOK)
	access := e2eOIDCString(t, tokens, "access_token")
	refresh := e2eOIDCString(t, tokens, "refresh_token")
	e2eRequireStatus(t, infra.request(t, http.MethodPost, appPath+"/grants:revoke-all", "", nil, nil, adminHeaders), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appPath+"/grants", "", nil, &listed, adminHeaders), http.StatusOK)
	if len(listed.Data) != 1 || listed.Data[0].RevokedAt == nil {
		t.Fatalf("revoke-all left renewed grant active: %+v", listed.Data)
	}
	e2eOIDCJSON(t, infra, http.MethodGet, "/oauth/userinfo", nil,
		map[string]string{"Authorization": "Bearer " + access}, nil, http.StatusUnauthorized)
	refreshForm := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "refresh_token": {refresh},
	}
	var rejected map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", refreshForm, nil, &rejected, http.StatusBadRequest)
	if rejected["error"] != "invalid_grant" {
		t.Fatalf("revoked grant refresh error = %v", rejected["error"])
	}
}

func e2eOIDCFlow(t *testing.T, infra *e2eInfra, gateway *e2eGateway, app apitypes.OAuthAppWithSecret) {
	t.Helper()
	var discovery map[string]any
	e2eOIDCJSON(t, infra, http.MethodGet, "/.well-known/openid-configuration", nil, nil, &discovery, http.StatusOK)
	if discovery["issuer"] != infra.apiURL || discovery["token_endpoint"] != infra.apiURL+"/oauth/token" {
		t.Fatalf("discovery has wrong issuer or token endpoint: %v", discovery)
	}
	t.Log("OIDC discovery returned the running API issuer")
	var serverMetadata map[string]any
	e2eOIDCJSON(t, infra, http.MethodGet, "/.well-known/oauth-authorization-server", nil, nil, &serverMetadata, http.StatusOK)
	if serverMetadata["issuer"] != infra.apiURL {
		t.Fatalf("OAuth metadata issuer = %v", serverMetadata["issuer"])
	}
	t.Log("OAuth authorization-server metadata returned")

	verifier := "e2e-correct-verifier-for-pkce"
	browserCode := e2eOIDCAuthorize(t, infra, app, verifier, "e2e-state", "e2e-nonce")
	userCode := e2eOIDCWaitSnapshot(t, infra, browserCode, "pending")
	t.Log("authorization and wait snapshot returned")
	e2eOIDCSendInbound(t, gateway, "e2e-identity-seed", "ordinary message")
	t.Log("identity seed delivered to gateway")
	identity := store.NewIdentityRepo(infra.db)
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "OAuth claimant identity projection", func() bool {
		_, err := identity.GetByLID(ctx, e2eOAuthLID)
		return err == nil
	})
	t.Log("claimant identity projected")
	e2eOIDCSendInbound(t, gateway, "e2e-oauth-claim", app.LoginCommand+" "+userCode)
	t.Log("OAuth login command delivered to gateway")
	code := e2eOIDCFinalizeWhenClaimed(t, infra, browserCode, "e2e-state", "e2e-oauth-claim")
	t.Log("OAuth claim finalized")
	if replay := e2eOIDCFinalize(t, infra, browserCode, "e2e-state"); replay != code {
		t.Fatal("finalize replay minted a different code")
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "code": {code},
		"redirect_uri": {e2eOAuthRedirect}, "code_verifier": {verifier},
	}
	var rejected map[string]any
	badSecret := url.Values{}
	for key, values := range form {
		badSecret[key] = append([]string(nil), values...)
	}
	badSecret.Set("client_secret", "incorrect")
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", badSecret, nil, &rejected, http.StatusUnauthorized)
	if rejected["error"] != "invalid_client" {
		t.Fatalf("wrong secret error = %v", rejected["error"])
	}
	var tokens map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", form, nil, &tokens, http.StatusOK)
	access := e2eOIDCString(t, tokens, "access_token")
	idRaw := e2eOIDCString(t, tokens, "id_token")
	refresh := e2eOIDCString(t, tokens, "refresh_token")
	if tokens["token_type"] != "Bearer" {
		t.Fatalf("token type = %v", tokens["token_type"])
	}
	var jwks map[string]any
	e2eOIDCJSON(t, infra, http.MethodGet, "/.well-known/oauth-jwks.json", nil, nil, &jwks, http.StatusOK)
	jwksRaw, err := json.Marshal(jwks)
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(jwksRaw)
	if err != nil {
		t.Fatal(err)
	}
	idToken, err := jwt.Parse([]byte(idRaw), jwt.WithKeySet(set), jwt.WithValidate(true),
		jwt.WithIssuer(infra.apiURL), jwt.WithAudience(app.ClientID))
	if err != nil {
		t.Fatalf("published JWKS did not verify ID token: %v", err)
	}
	subject, ok := idToken.Subject()
	var nonce string
	_ = idToken.Get("nonce", &nonce)
	if !ok || subject != e2eOAuthLID || nonce != "e2e-nonce" {
		t.Fatalf("ID token subject/nonce mismatch: subject=%q nonce=%q", subject, nonce)
	}
	var userinfo map[string]any
	e2eOIDCJSON(t, infra, http.MethodGet, "/oauth/userinfo", nil,
		map[string]string{"Authorization": "Bearer " + access}, &userinfo, http.StatusOK)
	if userinfo["sub"] != e2eOAuthLID {
		t.Fatalf("userinfo subject = %v", userinfo["sub"])
	}
	var postUserinfo map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/userinfo", nil,
		map[string]string{"Authorization": "Bearer " + access}, &postUserinfo, http.StatusOK)
	if postUserinfo["sub"] != e2eOAuthLID {
		t.Fatalf("POST userinfo subject = %v", postUserinfo["sub"])
	}
	var replayed map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", form, nil, &replayed, http.StatusBadRequest)
	if replayed["error"] != "invalid_grant" {
		t.Fatalf("code replay error = %v", replayed["error"])
	}
	refreshForm := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "refresh_token": {refresh},
	}
	var rotated map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", refreshForm, nil, &rotated, http.StatusOK)
	rotatedRefresh := e2eOIDCString(t, rotated, "refresh_token")
	if rotatedRefresh == refresh {
		t.Fatal("refresh token was not rotated")
	}
	revoke := url.Values{"token": {rotatedRefresh}, "client_id": {app.ClientID}, "client_secret": {app.ClientSecret}}
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/revoke", revoke, nil, nil, http.StatusOK)
	refreshForm.Set("refresh_token", rotatedRefresh)
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", refreshForm, nil, &replayed, http.StatusBadRequest)
	if replayed["error"] != "invalid_grant" {
		t.Fatalf("revoked refresh error = %v", replayed["error"])
	}
}

func e2eOIDCHostile(t *testing.T, infra *e2eInfra, gateway *e2eGateway, app apitypes.OAuthAppWithSecret) {
	t.Helper()
	query := e2eOIDCAuthQuery(app, "hostile-verifier", "hostile-state", "")
	query.Set("redirect_uri", "https://attacker.example/callback")
	response := e2eOIDCRequest(t, infra, http.MethodGet, "/oauth/authorize?"+query.Encode(), nil, nil)
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Location") != "" {
		t.Fatalf("unregistered redirect escaped locally: status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	query.Set("redirect_uri", e2eOAuthRedirect)
	query.Set("code_challenge_method", "plain")
	response = e2eOIDCRequest(t, infra, http.MethodGet, "/oauth/authorize?"+query.Encode(), nil, nil)
	location := response.Header.Get("Location")
	if response.StatusCode != http.StatusFound || !strings.HasPrefix(location, e2eOAuthRedirect) || !strings.Contains(location, "error=invalid_request") {
		t.Fatalf("plain PKCE response status=%d location=%q", response.StatusCode, location)
	}
	_ = response.Body.Close()

	browserCode := e2eOIDCAuthorize(t, infra, app, "hostile-verifier", "cancel-state", "")
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/wait/"+browserCode+"/cancel", nil, nil, nil, http.StatusNoContent)
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/wait/"+browserCode+"/finalize", nil, nil, nil, http.StatusNotFound)

	browserCode = e2eOIDCAuthorize(t, infra, app, "real-pkce-verifier", "pkce-state", "")
	userCode := e2eOIDCWaitSnapshot(t, infra, browserCode, "pending")
	e2eOIDCSendInbound(t, gateway, "e2e-oauth-pkce-claim", app.LoginCommand+" "+userCode)
	code := e2eOIDCFinalizeWhenClaimed(t, infra, browserCode, "pkce-state", "e2e-oauth-pkce-claim")
	wrongPKCE := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "code": {code},
		"redirect_uri": {e2eOAuthRedirect}, "code_verifier": {"wrong-pkce-verifier"},
	}
	var rejected map[string]any
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", wrongPKCE, nil, &rejected, http.StatusBadRequest)
	if rejected["error"] != "invalid_grant" {
		t.Fatalf("wrong PKCE verifier error = %v", rejected["error"])
	}
	wrongPKCE.Set("code_verifier", "real-pkce-verifier")
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/token", wrongPKCE, nil, &rejected, http.StatusBadRequest)
	if rejected["error"] != "invalid_grant" {
		t.Fatalf("consumed bad-PKCE code replay error = %v", rejected["error"])
	}
}

func e2eOIDCAuthQuery(app apitypes.OAuthAppWithSecret, verifier, state, nonce string) url.Values {
	sum := sha256.Sum256([]byte(verifier))
	return url.Values{
		"response_type": {"code"}, "client_id": {app.ClientID},
		"redirect_uri": {e2eOAuthRedirect}, "scope": {"openid profile phone offline_access"},
		"state": {state}, "nonce": {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
}

func e2eOIDCAuthorize(t *testing.T, infra *e2eInfra, app apitypes.OAuthAppWithSecret, verifier, state, nonce string) string {
	t.Helper()
	query := e2eOIDCAuthQuery(app, verifier, state, nonce)
	response := e2eOIDCRequest(t, infra, http.MethodGet, "/oauth/authorize?"+query.Encode(), nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d", response.StatusCode)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(location.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	code := fragment.Get("c")
	if code == "" {
		t.Fatal("authorize redirect has no browser code")
	}
	return code
}

func e2eOIDCWaitSnapshot(t *testing.T, infra *e2eInfra, browserCode, want string) string {
	t.Helper()
	response := e2eOIDCRequest(t, infra, http.MethodGet, "/oauth/wait/"+browserCode+"/stream", nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("wait stream status = %d", response.StatusCode)
	}
	frame, err := bufio.NewReader(response.Body).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read wait snapshot: %v", err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(frame, &snapshot); err != nil {
		t.Fatal(err)
	}
	status, _ := snapshot["status"].(string)
	if want != "" && status != want {
		t.Fatalf("wait stream status = %q, want %q", status, want)
	}
	if status == "pending" {
		code, _ := snapshot["user_code"].(string)
		if code == "" {
			t.Fatal("pending wait stream has no user code")
		}
		return code
	}
	return status
}

func e2eOIDCSendInbound(t *testing.T, gateway *e2eGateway, id, body string) {
	t.Helper()
	message, err := protojson.Marshal(&waE2E.Message{Conversation: proto.String(body)})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"id": id, "chat": e2eOAuthPhoneJID, "sender": e2eOAuthPhoneJID,
		"senderAlt": e2eOAuthLID, "message": json.RawMessage(message),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.controlURL+"/incoming", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("gateway incoming status = %d", response.StatusCode)
	}
}

func e2eOIDCFinalize(t *testing.T, infra *e2eInfra, browserCode, state string) string {
	t.Helper()
	var result map[string]string
	e2eOIDCJSON(t, infra, http.MethodPost, "/oauth/wait/"+browserCode+"/finalize", nil, nil, &result, http.StatusOK)
	return e2eOIDCCodeFromRedirect(t, infra, result["redirect"], state)
}

func e2eOIDCFinalizeWhenClaimed(t *testing.T, infra *e2eInfra, browserCode, state, loginMessageID string) string {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	var code string
	e2eEventually(t, ctx, "OAuth WhatsApp claim and finalization", func() bool {
		response := e2eOIDCRequest(t, infra, http.MethodPost, "/oauth/wait/"+browserCode+"/finalize", nil, nil)
		defer response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			for _, line := range strings.Split(infra.apiOutput.String(), "\n") {
				if strings.Contains(line, "committed event processing failed") {
					t.Fatalf("API could not process OAuth setup event: %s", line)
				}
			}
			var leaked int
			if err := infra.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
				e2eSessionID, loginMessageID).Scan(&leaked); err != nil {
				t.Fatal(err)
			}
			if leaked != 0 {
				t.Fatal("OAuth login command was projected into ordinary message history")
			}
			return false
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("finalize status = %d", response.StatusCode)
		}
		var result map[string]string
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		code = e2eOIDCCodeFromRedirect(t, infra, result["redirect"], state)
		return true
	})
	return code
}

func e2eOIDCCodeFromRedirect(t *testing.T, infra *e2eInfra, raw, state string) string {
	t.Helper()
	redirect, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Scheme != "https" || redirect.Host != "example.org" || redirect.Path != "/e2e-oidc-callback" ||
		redirect.Query().Get("state") != state || redirect.Query().Get("iss") != infra.apiURL {
		t.Fatalf("finalize redirect has wrong origin, state, or issuer: %q", raw)
	}
	code := redirect.Query().Get("code")
	if code == "" {
		t.Fatal("finalize redirect has no authorization code")
	}
	return code
}

func e2eOIDCRequest(t *testing.T, infra *e2eInfra, method, path string, form url.Values, headers map[string]string) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	ctx, cancel := e2eContext(t)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, method, infra.apiURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func e2eOIDCJSON(t *testing.T, infra *e2eInfra, method, path string, form url.Values, headers map[string]string, result any, want int) {
	t.Helper()
	response := e2eOIDCRequest(t, infra, method, path, form, headers)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d", method, path, response.StatusCode, want)
	}
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
}

func e2eOIDCString(t *testing.T, response map[string]any, key string) string {
	t.Helper()
	value, ok := response[key].(string)
	if !ok || value == "" {
		t.Fatalf("OAuth response has no %s", key)
	}
	return value
}

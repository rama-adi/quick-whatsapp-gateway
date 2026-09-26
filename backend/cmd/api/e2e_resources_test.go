package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/backup"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/media"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	_ "modernc.org/sqlite"
)

// runE2EResourceScenarios exercises the API's local resource writers through
// the running router, then checks their durable MySQL effects and org boundary.
func runE2EResourceScenarios(t *testing.T, infra *e2eInfra, gateway *e2eGateway, adminToken string) {
	t.Helper()
	t.Run("chat lifecycle", func(t *testing.T) { e2eChatLifecycle(t, infra) })
	t.Run("webhook lifecycle", func(t *testing.T) { e2eWebhookLifecycle(t, infra) })
	t.Run("storage lifecycle", func(t *testing.T) { e2eStorageLifecycle(t, infra) })
	t.Run("admin and OAuth applications", func(t *testing.T) { e2eAdminOAuth(t, infra, adminToken) })
	t.Run("live contacts and groups", func(t *testing.T) { e2eLiveContactsAndGroups(t, infra, gateway) })
	t.Run("session lifecycle and pairing", func(t *testing.T) { e2eSessionLifecycle(t, infra) })
	t.Run("backup validation and status", func(t *testing.T) { e2eBackupValidation(t, infra) })
	t.Run("channels and status", func(t *testing.T) { e2eChannelsAndStatus(t, infra, gateway) })
}

func e2eRequireStatus(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("HTTP status = %d, want %d", got, want)
	}
}

func e2eChatLifecycle(t *testing.T, infra *e2eInfra) {
	const chatJID = "628999000111@s.whatsapp.net"
	status, sent := infra.send(t, e2eOrgAKey, "e2e-resource-chat", domain.SendRequest{
		Type: domain.SendTypeText,
		To:   chatJID,
		Text: "resource chat seed",
	})
	e2eRequireStatus(t, status, http.StatusOK)
	if sent.WAMessageID == "" {
		t.Fatalf("send result has no WhatsApp id: %#v", sent)
	}
	chatPath := "/api/v1/sessions/" + e2eSessionID + "/chats/" + url.PathEscape(chatJID)
	var chat domain.Chat
	e2eRequireStatus(t, infra.request(t, http.MethodGet, chatPath, e2eOrgAKey, nil, &chat, nil), http.StatusOK)
	if chat.ChatJID != chatJID || chat.LastMessageAt == nil {
		t.Fatalf("stored chat = %#v", chat)
	}
	var chatList struct {
		Data []domain.Chat `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet,
		"/api/v1/sessions/"+e2eSessionID+"/chats", e2eOrgAKey,
		nil, &chatList, nil), http.StatusOK)
	foundChat := false
	for _, item := range chatList.Data {
		foundChat = foundChat || item.ChatJID == chatJID
	}
	if !foundChat {
		t.Fatalf("new chat absent from list: %#v", chatList.Data)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, chatPath, e2eOrgBKey, nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, chatPath, "", nil, nil, nil), http.StatusUnauthorized)

	e2eRequireStatus(t, infra.request(t, http.MethodPatch, chatPath, e2eOrgAKey,
		map[string]any{"archived": true, "pinned": true}, &chat, nil), http.StatusOK)
	if !chat.Archived || !chat.Pinned {
		t.Fatalf("updated chat = %#v", chat)
	}
	var archived, pinned bool
	if err := infra.db.QueryRowContext(context.Background(),
		`SELECT archived,pinned FROM chats WHERE session_id=? AND chat_jid=?`,
		e2eSessionID, chatJID).Scan(&archived, &pinned); err != nil {
		t.Fatal(err)
	}
	if !archived || !pinned {
		t.Fatalf("stored chat flags = archived:%t pinned:%t", archived, pinned)
	}

	e2eRequireStatus(t, infra.request(t, http.MethodPost, chatPath+"/read", e2eOrgAKey, nil, &chat, nil), http.StatusOK)
	if chat.UnreadCount != 0 {
		t.Fatalf("read chat unread count = %d", chat.UnreadCount)
	}
	var presence domain.PresenceStatus
	e2eRequireStatus(t, infra.request(t, http.MethodGet, chatPath+"/presence", e2eOrgAKey,
		nil, &presence, nil), http.StatusOK)
	e2eRequireStatus(t, infra.request(t, http.MethodPut, chatPath+"/presence", e2eOrgAKey,
		map[string]any{"state": "composing"}, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, chatPath, e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, chatPath, e2eOrgAKey, nil, nil, nil), http.StatusNotFound)
	var remaining int
	if err := infra.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
		e2eSessionID, sent.WAMessageID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("deleted chat retained %d messages", remaining)
	}
}

func e2eWebhookLifecycle(t *testing.T, infra *e2eInfra) {
	const path = "/api/v1/webhooks"
	var created domain.Webhook
	e2eRequireStatus(t, infra.request(t, http.MethodPost, path, e2eOrgAKey,
		map[string]any{
			"url":       "https://example.org/e2e-webhook",
			"events":    []string{"message.from_me"},
			"secret":    "e2e-webhook-secret",
			"sessionId": e2eSessionID,
		}, &created, nil), http.StatusCreated)
	if created.ID == "" || created.OrganizationID != e2eOrgA || len(created.HMACSecret) != 0 {
		t.Fatalf("created webhook = %#v", created)
	}
	var ciphertext []byte
	if err := infra.db.QueryRowContext(context.Background(),
		`SELECT hmac_secret FROM webhooks WHERE id=? AND organization_id=?`,
		created.ID, e2eOrgA).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 || string(ciphertext) == "e2e-webhook-secret" {
		t.Fatal("webhook secret was not encrypted at rest")
	}
	hookPath := path + "/" + url.PathEscape(created.ID)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, hookPath, e2eOrgBKey, nil, nil, nil), http.StatusNotFound)
	var retrieved domain.Webhook
	e2eRequireStatus(t, infra.request(t, http.MethodGet, hookPath, e2eOrgAKey,
		nil, &retrieved, nil), http.StatusOK)
	if retrieved.ID != created.ID || len(retrieved.HMACSecret) != 0 {
		t.Fatalf("retrieved webhook = %#v", retrieved)
	}
	var listed struct {
		Data []domain.Webhook `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, path, e2eOrgAKey, nil, &listed, nil), http.StatusOK)
	if len(listed.Data) != 1 || listed.Data[0].ID != created.ID {
		t.Fatalf("listed webhooks = %#v", listed.Data)
	}
	var updated domain.Webhook
	e2eRequireStatus(t, infra.request(t, http.MethodPatch, hookPath, e2eOrgAKey,
		map[string]any{"url": "https://example.org/e2e-updated", "active": false},
		&updated, nil), http.StatusOK)
	if updated.URL != "https://example.org/e2e-updated" || updated.Active {
		t.Fatalf("updated webhook = %#v", updated)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, hookPath, e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, hookPath, e2eOrgAKey, nil, nil, nil), http.StatusNotFound)
}

func e2eStorageLifecycle(t *testing.T, infra *e2eInfra) {
	const bucketPath = "/api/v1/storage/buckets"
	input := media.BucketInput{
		Name:      "E2E attachments",
		Endpoint:  "https://storage.example.org",
		Region:    "test-region",
		Bucket:    "e2e-bucket",
		PathStyle: true,
		AccessKey: "e2e-access",
		SecretKey: "e2e-secret",
	}
	for _, endpoint := range []string{"http://s3.internal:9000", "http://nas.local:9000", "http://nas.lan:9000", "http://nas.home.arpa:9000", "http://nas.localdomain:9000", "http://s3.team.svc:9000", "http://s3.team.svc.cluster.local:9000"} {
		candidate := input
		candidate.Endpoint = endpoint
		var connection media.Bucket
		e2eRequireStatus(t, infra.request(t, http.MethodPost, bucketPath, e2eOrgAKey, candidate, &connection, nil), http.StatusOK)
		e2eRequireStatus(t, infra.request(t, http.MethodDelete, bucketPath+"/"+connection.ID, e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	}
	for _, endpoint := range []string{"http://s3.example.com", "http://s3.internal.example.com", "http://127.0.0.1:9000", "http://169.254.169.254", "ftp://s3.internal", "http://user:secret@s3.internal"} {
		candidate := input
		candidate.Endpoint = endpoint
		e2eRequireStatus(t, infra.request(t, http.MethodPost, bucketPath, e2eOrgAKey, candidate, nil, nil), http.StatusBadRequest)
	}
	var created media.Bucket
	e2eRequireStatus(t, infra.request(t, http.MethodPost, bucketPath, e2eOrgAKey,
		input, &created, nil), http.StatusOK)
	if created.ID == "" || created.Name != input.Name {
		t.Fatalf("created bucket = %#v", created)
	}
	var listed struct {
		Items []media.Bucket `json:"items"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, bucketPath, e2eOrgAKey, nil, &listed, nil), http.StatusOK)
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("listed buckets = %#v", listed.Items)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, bucketPath, e2eOrgBKey, nil, &listed, nil), http.StatusOK)
	if len(listed.Items) != 0 {
		t.Fatalf("other organization sees buckets: %#v", listed.Items)
	}
	retention := int64(30)
	input.Name = "E2E updated attachments"
	input.RetentionDays = &retention
	input.AccessKey, input.SecretKey = "", ""
	var updated media.Bucket
	e2eRequireStatus(t, infra.request(t, http.MethodPut,
		bucketPath+"/"+url.PathEscape(created.ID), e2eOrgAKey,
		input, &updated, nil), http.StatusOK)
	if updated.Name != input.Name || updated.RetentionDays == nil || *updated.RetentionDays != retention {
		t.Fatalf("updated bucket = %#v", updated)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, bucketPath, e2eOrgAKey,
		nil, &listed, nil), http.StatusOK)
	if len(listed.Items) != 1 || listed.Items[0].Name != input.Name ||
		listed.Items[0].RetentionDays == nil || *listed.Items[0].RetentionDays != retention {
		t.Fatalf("bucket update not persisted: %#v", listed.Items)
	}
	bindingPath := "/api/v1/sessions/" + e2eSessionID + "/storage"
	var binding struct {
		BucketID *string `json:"bucketId"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPut, bindingPath, e2eOrgAKey,
		map[string]any{"bucketId": created.ID}, &binding, nil), http.StatusOK)
	if binding.BucketID == nil || *binding.BucketID != created.ID {
		t.Fatalf("storage binding = %#v", binding)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, bindingPath, e2eOrgAKey,
		nil, &binding, nil), http.StatusOK)
	if binding.BucketID == nil || *binding.BucketID != created.ID {
		t.Fatalf("retrieved storage binding = %#v", binding)
	}
	var denied map[string]any
	deniedStatus := infra.request(t, http.MethodGet, bindingPath, e2eOrgBKey, nil, &denied, nil)
	if deniedStatus != http.StatusNotFound {
		t.Fatalf("cross-org storage binding status = %d, response = %#v",
			deniedStatus, denied)
	}
	var storedID string
	if err := infra.db.QueryRowContext(context.Background(),
		`SELECT bucket_id FROM session_media_storage WHERE session_id=? AND organization_id=?`,
		e2eSessionID, e2eOrgA).Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if storedID != created.ID {
		t.Fatalf("stored bucket binding = %q, want %q", storedID, created.ID)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPut, bindingPath, e2eOrgAKey,
		map[string]any{"bucketId": nil}, &binding, nil), http.StatusOK)
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, bucketPath+"/"+url.PathEscape(created.ID),
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
}

func e2eAdminOAuth(t *testing.T, infra *e2eInfra, adminToken string) {
	adminHeaders := map[string]string{"Authorization": "Bearer " + adminToken}
	const adminPath = "/api/v1/admin/sessions"
	e2eRequireStatus(t, infra.request(t, http.MethodGet, adminPath, e2eOrgAKey,
		nil, nil, nil), http.StatusForbidden)
	var sessions struct {
		Data []domain.WASession `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, adminPath, "",
		nil, &sessions, adminHeaders), http.StatusOK)
	if len(sessions.Data) != 1 || sessions.Data[0].ID != e2eSessionID {
		t.Fatalf("administrator sessions = %#v", sessions.Data)
	}
	backfillPath := adminPath + "/" + e2eSessionID + ":backfill"
	var job domain.BackfillJob
	e2eRequireStatus(t, infra.request(t, http.MethodPost, backfillPath, "",
		nil, &job, adminHeaders), http.StatusAccepted)
	if job.ID == "" || job.SessionID != e2eSessionID {
		t.Fatalf("started backfill = %#v", job)
	}
	statusPath := adminPath + "/" + e2eSessionID + "/backfill"
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "admin backfill completion", func() bool {
		return infra.request(t, http.MethodGet, statusPath, "",
			nil, &job, adminHeaders) == http.StatusOK && job.Status == "succeeded"
	})

	const appPath = "/api/v1/oauth-apps"
	var created apitypes.OAuthAppWithSecret
	e2eRequireStatus(t, infra.request(t, http.MethodPost, appPath, "",
		map[string]any{
			"sessionId":    e2eSessionID,
			"name":         "E2E sign in",
			"clientType":   "confidential",
			"redirectUris": []string{"https://example.org/oauth/callback"},
			"modes":        []string{"dm"},
		}, &created, adminHeaders), http.StatusCreated)
	if created.ID == "" || created.ClientSecret == "" || created.OrganizationID != e2eOrgA {
		t.Fatalf("created OAuth app = %#v", created)
	}
	appIDPath := appPath + "/" + url.PathEscape(created.ID)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appIDPath,
		e2eOrgBKey, nil, nil, nil), http.StatusNotFound)
	var retrieved apitypes.OAuthAppWithSecret
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appIDPath,
		"", nil, &retrieved, adminHeaders), http.StatusOK)
	if retrieved.ID != created.ID || retrieved.ClientSecret != "" {
		t.Fatalf("retrieved OAuth app leaks secret or mismatches: %#v", retrieved)
	}
	var updated apitypes.OAuthApp
	e2eRequireStatus(t, infra.request(t, http.MethodPatch, appIDPath, "",
		map[string]any{"name": "E2E updated"}, &updated, adminHeaders), http.StatusOK)
	if updated.Name != "E2E updated" {
		t.Fatalf("updated OAuth app = %#v", updated)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, appIDPath+":disable", "",
		nil, &updated, adminHeaders), http.StatusOK)
	if updated.Status != apitypes.OAuthAppDisabled {
		t.Fatalf("disabled OAuth app = %#v", updated)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, appIDPath+":enable", "",
		nil, &updated, adminHeaders), http.StatusOK)
	if updated.Status != apitypes.OAuthAppActive {
		t.Fatalf("enabled OAuth app = %#v", updated)
	}
	var rotated apitypes.OAuthAppWithSecret
	e2eRequireStatus(t, infra.request(t, http.MethodPost, appIDPath+":rotate-secret", "",
		nil, &rotated, adminHeaders), http.StatusOK)
	if rotated.ClientSecret == "" || rotated.ClientSecret == created.ClientSecret {
		t.Fatalf("rotated OAuth secret = %#v", rotated)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, appIDPath, "",
		nil, nil, adminHeaders), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, appIDPath, "",
		nil, nil, adminHeaders), http.StatusNotFound)
}

func e2eLiveContactsAndGroups(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	const contactJID = "628777000222@s.whatsapp.net"
	const contactLID = "205227043110953@lid"
	base := "/api/v1/sessions/" + e2eSessionID
	var me service.Me
	e2eRequireStatus(t, infra.request(t, http.MethodGet, base+"/me", e2eOrgAKey,
		nil, &me, nil), http.StatusOK)
	if me.SessionID != e2eSessionID || me.WAJID == nil || *me.WAJID != e2eDeviceJID {
		t.Fatalf("paired account identity = %#v", me)
	}
	inbound, err := json.Marshal(map[string]any{
		"id": "e2e-contact-inbound", "chat": contactJID,
		"sender": contactLID, "senderAlt": contactJID, "fromMe": false,
		"message": map[string]any{"conversation": "contact discovery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(gateway.controlURL+"/incoming", "application/json", bytes.NewReader(inbound))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("inject contact discovery: HTTP %d", response.StatusCode)
	}
	var contacts struct {
		Data []domain.Contact `json:"data"`
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "discovered contact projection", func() bool {
		if infra.request(t, http.MethodGet, base+"/contacts?source=dm", e2eOrgAKey,
			nil, &contacts, nil) != http.StatusOK {
			return false
		}
		for _, contact := range contacts.Data {
			if contact.LID == contactLID {
				return true
			}
		}
		return false
	})
	var detail service.ContactDetail
	e2eRequireStatus(t, infra.request(t, http.MethodGet,
		base+"/contacts/"+url.PathEscape(contactLID), e2eOrgAKey,
		nil, &detail, nil), http.StatusOK)
	if detail.Identity == nil || detail.Identity.LID != contactLID || !detail.DM {
		t.Fatalf("contact detail = %#v", detail)
	}
	contactPath := base + "/contacts/" + url.PathEscape(contactJID)
	var checked domain.OnWhatsApp
	e2eRequireStatus(t, infra.request(t, http.MethodGet,
		base+"/contacts/check?phone=628777000222", e2eOrgAKey, nil, &checked, nil), http.StatusOK)
	if checked.JID != contactJID {
		t.Fatalf("contact lookup = %#v", checked)
	}
	var picture domain.ProfilePicture
	e2eRequireStatus(t, infra.request(t, http.MethodGet, contactPath+"/picture",
		e2eOrgAKey, nil, &picture, nil), http.StatusOK)
	if picture.URL == "" {
		t.Fatalf("profile picture = %#v", picture)
	}
	var about struct {
		About string `json:"about"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, contactPath+"/about",
		e2eOrgAKey, nil, &about, nil), http.StatusOK)
	if about.About != "Isolated profile" {
		t.Fatalf("contact about = %#v", about)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, contactPath+"/block",
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, contactPath+"/unblock",
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, contactPath+"/picture",
		e2eOrgBKey, nil, nil, nil), http.StatusNotFound)

	groupPath := base + "/groups"
	var created domain.GroupInfo
	e2eRequireStatus(t, infra.request(t, http.MethodPost, groupPath, e2eOrgAKey,
		map[string]any{"name": "E2E group", "participants": []string{contactJID}},
		&created, nil), http.StatusCreated)
	if created.GroupJID == "" || created.Subject != "E2E group" {
		t.Fatalf("created group = %#v", created)
	}
	groupIDPath := groupPath + "/" + url.PathEscape(created.GroupJID)
	var stored domain.Group
	e2eRequireStatus(t, infra.request(t, http.MethodGet, groupIDPath,
		e2eOrgAKey, nil, &stored, nil), http.StatusOK)
	if stored.GroupJID != created.GroupJID {
		t.Fatalf("stored group = %#v", stored)
	}
	var groups struct {
		Data []domain.Group `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, groupPath, e2eOrgAKey,
		nil, &groups, nil), http.StatusOK)
	foundGroup := false
	for _, group := range groups.Data {
		foundGroup = foundGroup || group.GroupJID == created.GroupJID
	}
	if !foundGroup {
		t.Fatalf("created group absent from list: %#v", groups.Data)
	}
	var members struct {
		Data []domain.GroupMember `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, groupIDPath+"/members", e2eOrgAKey,
		nil, &members, nil), http.StatusOK)
	if len(members.Data) == 0 || members.Data[0].LID != e2eDeviceLID {
		t.Fatalf("creator membership absent: %#v", members.Data)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, groupIDPath,
		e2eOrgBKey, nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodPatch, groupIDPath, e2eOrgAKey,
		map[string]any{"subject": "E2E renamed", "announce": true},
		nil, nil), http.StatusNoContent)
	memberPath := groupIDPath + "/members/" + url.PathEscape(contactJID)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, groupIDPath+"/members", e2eOrgAKey,
		map[string]any{"participants": []string{contactJID}}, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, memberPath+"/promote",
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, memberPath+"/demote",
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, memberPath,
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)
	var invite struct {
		Invite string `json:"invite"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, groupIDPath+"/invite",
		e2eOrgAKey, nil, &invite, nil), http.StatusOK)
	if invite.Invite == "" {
		t.Fatalf("group invite = %#v", invite)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, groupIDPath+"/invite",
		e2eOrgAKey, nil, &invite, nil), http.StatusOK)
	var joined struct {
		GroupJID string `json:"groupJid"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, groupPath+":join", e2eOrgAKey,
		map[string]any{"invite": invite.Invite}, &joined, nil), http.StatusOK)
	if joined.GroupJID != "120363999@g.us" {
		t.Fatalf("joined group = %#v", joined)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, groupIDPath+"/members:approve",
		e2eOrgAKey, map[string]any{"participants": []string{contactJID}}, nil, nil), http.StatusNotImplemented)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, groupIDPath+":leave",
		e2eOrgAKey, nil, nil, nil), http.StatusNoContent)

	operations := e2eGatewayOperations(t, gateway)
	for _, name := range []string{
		"lookup", "picture", "user-info", "blocklist", "create-group",
		"group-name", "group-announce", "group-participants", "group-invite", "group-join", "group-leave",
	} {
		if !operations[name] {
			t.Fatalf("live operation %q was not observed; operations=%v", name, operations)
		}
	}
}

func e2eGatewayOperations(t *testing.T, gateway *e2eGateway) map[string]bool {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		gateway.controlURL+"/operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("gateway operations HTTP status = %d", response.StatusCode)
	}
	var rows []struct {
		Operation string `json:"operation"`
	}
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(rows))
	for _, row := range rows {
		names[row.Operation] = true
	}
	return names
}

func e2eSessionLifecycle(t *testing.T, infra *e2eInfra) {
	const path = "/api/v1/sessions"
	var created domain.WASession
	var response map[string]any
	status := infra.request(t, http.MethodPost, path, e2eOrgAKey,
		map[string]any{"label": "E2E pairing", "start": false}, &response, nil)
	if status != http.StatusCreated {
		t.Fatalf("create session status=%d body=%#v API logs=%s", status, response, infra.apiOutput.String())
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OrganizationID != e2eOrgA || created.GatewayID == "" {
		t.Fatalf("created session = %#v", created)
	}
	itemPath := path + "/" + url.PathEscape(created.ID)
	var retrieved domain.WASession
	e2eRequireStatus(t, infra.request(t, http.MethodGet, itemPath, e2eOrgAKey,
		nil, &retrieved, nil), http.StatusOK)
	if retrieved.ID != created.ID || retrieved.GatewayID != created.GatewayID {
		t.Fatalf("retrieved session = %#v", retrieved)
	}
	var listed struct {
		Data []domain.WASession `json:"data"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, path, e2eOrgAKey,
		nil, &listed, nil), http.StatusOK)
	found := false
	for _, session := range listed.Data {
		found = found || session.ID == created.ID
	}
	if !found {
		t.Fatalf("new session absent from list: %#v", listed.Data)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, itemPath, e2eOrgBKey,
		nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, itemPath+"/me", e2eOrgAKey,
		nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+"/pairing-code", e2eOrgAKey,
		map[string]any{"phone": ""}, nil, nil), http.StatusBadRequest)
	var pairing struct {
		Code string `json:"code"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+"/pairing-code", e2eOrgAKey,
		map[string]any{"phone": "628777000222"}, &pairing, nil), http.StatusOK)
	if pairing.Code != "E2E-CODE" {
		t.Fatalf("pairing code = %#v", pairing)
	}
	var qr struct {
		Code string `json:"code"`
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "QR code", func() bool {
		return infra.request(t, http.MethodGet, itemPath+"/qr", e2eOrgAKey,
			nil, &qr, nil) == http.StatusOK && qr.Code == "isolated-e2e-qr"
	})
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+":start", e2eOrgAKey,
		nil, &created, nil), http.StatusOK)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+":stop", e2eOrgAKey,
		nil, &created, nil), http.StatusOK)
	status, result := infra.send(t, e2eOrgAKey, "e2e-after-other-session-stop", domain.SendRequest{
		Type: domain.SendTypeText, To: "628999000333@s.whatsapp.net", Text: "unrelated session still routable",
	})
	e2eRequireStatus(t, status, http.StatusOK)
	if result.WAMessageID == "" {
		t.Fatal("unrelated session send after stop has no message id")
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+":restart", e2eOrgAKey,
		nil, &created, nil), http.StatusOK)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, itemPath+":logout", e2eOrgAKey,
		nil, &created, nil), http.StatusOK)
	e2eRequireStatus(t, infra.request(t, http.MethodDelete, itemPath, e2eOrgAKey,
		nil, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, itemPath, e2eOrgAKey,
		nil, nil, nil), http.StatusNotFound)
	status, result = infra.send(t, e2eOrgAKey, "e2e-after-other-session-delete", domain.SendRequest{
		Type: domain.SendTypeText, To: "628999000333@s.whatsapp.net", Text: "unrelated session after deletion",
	})
	e2eRequireStatus(t, status, http.StatusOK)
	if result.WAMessageID == "" {
		t.Fatal("unrelated session send after deletion has no message id")
	}
}

func e2eBackupValidation(t *testing.T, infra *e2eInfra) {
	path := "/api/v1/sessions/" + e2eSessionID + "/backfill"
	e2eRequireStatus(t, infra.request(t, http.MethodGet, path, e2eOrgAKey,
		nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, infra.request(t, http.MethodGet, path, e2eOrgBKey,
		nil, nil, nil), http.StatusNotFound)
	e2eRequireStatus(t, e2eBackupUpload(t, infra, path, e2eOrgAKey,
		nil, "valid-looking-key", nil), http.StatusBadRequest)
	e2eRequireStatus(t, e2eBackupUpload(t, infra, path, e2eOrgAKey,
		[]byte("corrupt encrypted backup"), "valid-looking-key", nil), http.StatusBadRequest)
	ciphertext, key := e2eCrypt15Fixture(t)
	var job domain.BackfillImport
	e2eRequireStatus(t, e2eBackupUpload(t, infra, path, e2eOrgAKey,
		ciphertext, key, &job), http.StatusAccepted)
	if job.ID == "" || job.SessionID != e2eSessionID {
		t.Fatalf("accepted backup import = %#v", job)
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "backup import completion", func() bool {
		return infra.request(t, http.MethodGet, path, e2eOrgAKey,
			nil, &job, nil) == http.StatusOK && job.Status == "succeeded"
	})
	if job.Chats != 1 || job.Messages != 1 {
		t.Fatalf("backup import counts = %#v", job)
	}
	var stored int
	if err := infra.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id=? AND wa_message_id=?`,
		e2eSessionID, "e2e-backup-message").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("backup message persisted rows = %d", stored)
	}
}

func e2eBackupUpload(t *testing.T, infra *e2eInfra, path, key string, data []byte, backupKey string, result any) int {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "msgstore.db.crypt15")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("key", backupKey); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, infra.apiURL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", key)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return response.StatusCode
}

func e2eCrypt15Fixture(t *testing.T) ([]byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "msgstore.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE jid (_id INTEGER PRIMARY KEY, raw_string TEXT, server TEXT, user TEXT);
		CREATE TABLE chat (_id INTEGER PRIMARY KEY, jid_row_id INTEGER, subject TEXT, sort_timestamp INTEGER);
		CREATE TABLE message (_id INTEGER PRIMARY KEY, chat_row_id INTEGER, sender_jid_row_id INTEGER,
			key_id TEXT, from_me INTEGER, message_type INTEGER, text_data TEXT, timestamp INTEGER);
		INSERT INTO jid VALUES (1, '628999000444@s.whatsapp.net', 's.whatsapp.net', '628999000444');
		INSERT INTO chat VALUES (1, 1, 'Backup chat', 1720000000000);
		INSERT INTO message VALUES (1, 1, 1, 'e2e-backup-message', 0, 0, 'restored from encrypted backup', 1720000000000);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	rootKey := bytes.Repeat([]byte{1}, 32)
	block, err := aes.NewCipher(backup.DeriveAESKey(rootKey))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x42}, 16)
	sealed := gcm.Seal(nil, iv, compressed.Bytes(), nil)
	ivField := append([]byte{0x0a, 0x10}, iv...)
	header := append([]byte{0x1a, byte(len(ivField))}, ivField...)
	file := append([]byte{byte(len(header))}, header...)
	file = append(file, sealed...)
	file = append(file, bytes.Repeat([]byte{0}, 16)...)
	return file, hex.EncodeToString(rootKey)
}

func e2eChannelsAndStatus(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	base := "/api/v1/sessions/" + e2eSessionID
	channels := base + "/channels"
	channel := channels + "/12345@newsletter"
	e2eRequireStatus(t, infra.request(t, http.MethodPost, channels, e2eOrgAKey,
		map[string]any{"name": "E2E channel"}, nil, nil), http.StatusNotImplemented)
	for _, action := range []string{":follow", ":unfollow", ":mute"} {
		var body any
		if action == ":mute" {
			body = map[string]any{}
		}
		e2eRequireStatus(t, infra.request(t, http.MethodPost, channel+action, e2eOrgAKey,
			body, nil, nil), http.StatusNotImplemented)
	}
	var messages struct {
		Data []domain.Message `json:"data"`
	}
	channelMessage := domain.Message{
		ID: domain.NewMessageID(), SessionID: e2eSessionID,
		WAMessageID: "e2e-channel-history", ChatJID: "12345@newsletter",
		Direction: domain.DirectionIn, Type: domain.SendTypeText,
		Body:      ptr("stored channel announcement"),
		Timestamp: domain.NowMs(), CreatedAt: domain.NowMs(),
	}
	if err := store.NewMessageRepo(infra.db).Upsert(context.Background(), channelMessage); err != nil {
		t.Fatal(err)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, channel+"/messages", e2eOrgAKey,
		nil, &messages, nil), http.StatusOK)
	if len(messages.Data) != 1 || messages.Data[0].WAMessageID != channelMessage.WAMessageID {
		t.Fatalf("stored channel history: %#v", messages.Data)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodGet, channel+"/messages", e2eOrgBKey,
		nil, nil, nil), http.StatusNotFound)

	statusPath := base + "/status"
	e2eRequireStatus(t, infra.request(t, http.MethodPost, statusPath, e2eOrgAKey,
		map[string]any{"type": "text", "text": ""}, nil, nil), http.StatusBadRequest)
	e2eRequireStatus(t, infra.request(t, http.MethodPost, statusPath, e2eOrgAKey,
		map[string]any{"type": "image", "text": "ignored"}, nil, nil), http.StatusNotImplemented)
	var sent struct {
		MessageID string `json:"messageId"`
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPost, statusPath, e2eOrgAKey,
		map[string]any{"type": "text", "text": "E2E status"}, &sent, nil), http.StatusOK)
	if sent.MessageID == "" {
		t.Fatalf("status send = %#v", sent)
	}
	statusCaptured := false
	for _, capture := range gateway.getCaptures(t) {
		if capture.ID == sent.MessageID && capture.To == "status@broadcast" &&
			strings.Contains(string(capture.Message), "E2E status") {
			statusCaptured = true
		}
	}
	if !statusCaptured {
		t.Fatalf("status broadcast %q was not captured with its text", sent.MessageID)
	}
	e2eRequireStatus(t, infra.request(t, http.MethodPut, base+"/presence", e2eOrgAKey,
		map[string]any{"state": "online"}, nil, nil), http.StatusNoContent)
	e2eRequireStatus(t, infra.request(t, http.MethodPut, base+"/presence", e2eOrgAKey,
		map[string]any{"state": "invalid"}, nil, nil), http.StatusBadRequest)
	operations := e2eGatewayOperations(t, gateway)
	if !operations["presence"] {
		t.Fatalf("presence operation absent: %#v", operations)
	}
}

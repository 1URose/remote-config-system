package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"log/slog"

	"github.com/1URose/remote-config-system/internal/auth"
	"github.com/1URose/remote-config-system/internal/domain"
	"github.com/1URose/remote-config-system/internal/metrics"
	configservice "github.com/1URose/remote-config-system/internal/services/config"
	featureservice "github.com/1URose/remote-config-system/internal/services/feature"
	redisstorage "github.com/1URose/remote-config-system/internal/storage/redis"
	"github.com/1URose/remote-config-system/pkg/sdk"
)

func TestRBACForbidden(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	readerToken, err := authManager.Generate("reader@example.com", []string{auth.RoleReader}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := newRequest(stdhttp.MethodPost, "/config/update", readerToken, `{"namespace":"payments","entries":[{"key":"flag","value":"true","type":"bool"}]}`, "application/json")
	req.Header.Set("Authorization", "Bearer "+readerToken)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestPerKeyAndMetricsRBAC(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)
	readerToken := mustToken(t, authManager, "reader@example.com", auth.RoleReader)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/metrics", "", "", ""))
	if rec.Code != stdhttp.StatusUnauthorized {
		t.Fatalf("expected metrics without token to return 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/metrics", readerToken, "", ""))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected metrics with reader token to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", readerToken, `{"value":"15","type":"int"}`, "application/json"))
	if rec.Code != stdhttp.StatusForbidden {
		t.Fatalf("expected reader config put to return 403, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodDelete, "/configs/payments/timeout", editorToken, "", "application/json"))
	if rec.Code != stdhttp.StatusForbidden {
		t.Fatalf("expected editor config delete to return 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdatedByIsRejectedFromClientPayload(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, `{"value":"15","type":"int","updatedBy":"spoof@example.com"}`, "application/json"))
	if rec.Code != stdhttp.StatusBadRequest {
		t.Fatalf("expected config put with updatedBy to return 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, `{"namespace":"payments","updatedBy":"spoof@example.com","entries":[{"key":"flag","value":"true","type":"bool"}]}`, "application/json"))
	if rec.Code != stdhttp.StatusBadRequest {
		t.Fatalf("expected update with updatedBy to return 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	ownerToken := mustToken(t, authManager, "owner@example.com", auth.RoleOwner)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodDelete, "/configs/payments/timeout?updatedBy=spoof@example.com", ownerToken, "", ""))
	if rec.Code != stdhttp.StatusBadRequest {
		t.Fatalf("expected delete with updatedBy query to return 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateEndpoint(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := newRequest(stdhttp.MethodPost, "/config/update", editorToken, `{"namespace":"payments","entries":[{"key":"flag","value":"true","type":"bool"}]}`, "application/json")
	req = req.WithContext(context.Background())

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfigCRUDEndpointsWithRBAC(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)
	readerToken := mustToken(t, authManager, "reader@example.com", auth.RoleReader)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)
	ownerToken := mustToken(t, authManager, "owner@example.com", auth.RoleOwner)

	putBody := `{"value":"15","type":"int"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, putBody, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected put 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/configs/payments/timeout", readerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected get 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var item domain.ConfigItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal config item: %v", err)
	}
	if item.Value != "15" || item.Type != "int" || item.Version != 1 {
		t.Fatalf("unexpected config item %+v", item)
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, `{"value":"30","type":"int"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected second put 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal updated config item: %v", err)
	}
	if item.Value != "30" || item.Version != 2 {
		t.Fatalf("expected server-computed version 2, got %+v", item)
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodDelete, "/configs/payments/timeout", ownerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusNoContent {
		t.Fatalf("expected delete 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/configs/payments/timeout", readerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusNotFound {
		t.Fatalf("expected get after delete 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetAllConfigsEndpoint(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	readerToken, err := authManager.Generate("reader@example.com", []string{auth.RoleReader}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, `{"value":"15","type":"int"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("seed payments config: %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/demo-service/app.theme", editorToken, `{"value":"dark","type":"string"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("seed demo-service config: %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/configs", readerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Namespaces []string                `json:"namespaces"`
		Items      []domain.ExportResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(response.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %+v", response.Namespaces)
	}
	if response.Namespaces[0] != "demo-service" || response.Namespaces[1] != "payments" {
		t.Fatalf("unexpected namespaces order/content: %+v", response.Namespaces)
	}
	if len(response.Items) != 2 {
		t.Fatalf("expected 2 namespace groups, got %+v", response.Items)
	}
	if response.Items[0].Namespace != "demo-service" || len(response.Items[0].Items) != 1 {
		t.Fatalf("unexpected first namespace payload: %+v", response.Items[0])
	}
	if response.Items[1].Namespace != "payments" || len(response.Items[1].Items) != 1 {
		t.Fatalf("unexpected second namespace payload: %+v", response.Items[1])
	}
}

func TestUpdateEndpointComputesNextVersion(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	body := `{"namespace":"payments","entries":[{"key":"flag","value":"true","type":"bool"}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected first update to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected second update to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Items []domain.ConfigItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Version != 2 {
		t.Fatalf("expected second bulk write to version 2, got %+v", response.Items)
	}
}

func TestPutConfigKeyReturnsLockedForConcurrentWrite(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	if err := mini.Set("config_write_lock:payments:timeout", "held-lock"); err != nil {
		t.Fatalf("set config write lock: %v", err)
	}

	body := `{"value":"30","type":"int"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusLocked {
		t.Fatalf("expected 423, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "locked by another write operation") {
		t.Fatalf("expected lock error body, got %q", rec.Body.String())
	}
}

func TestPutFeatureKeyReturnsLockedForConcurrentWrite(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	if err := mini.Set("feature_write_lock:payments:new-checkout", "held-lock"); err != nil {
		t.Fatalf("set feature write lock: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", editorToken, `{"enabled":true}`, "application/json"))
	if rec.Code != stdhttp.StatusLocked {
		t.Fatalf("expected 423, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "locked by another write operation") {
		t.Fatalf("expected lock error body, got %q", rec.Body.String())
	}
}

func TestPutConfigKeyReturnsLockedWhenBulkNamespaceLockHeld(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	if err := mini.Set("config_bulk_lock:payments", "held-lock"); err != nil {
		t.Fatalf("set namespace lock: %v", err)
	}

	body := `{"value":"30","type":"int"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusLocked {
		t.Fatalf("expected 423, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "namespace") || !strings.Contains(rec.Body.String(), "locked by another write operation") {
		t.Fatalf("expected namespace lock error body, got %q", rec.Body.String())
	}
}

func TestDryRunDoesNotPersist(t *testing.T) {
	server, repo, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	body := `{"namespace":"payments","dryRun":true,"entries":[{"key":"flag","value":"true","type":"bool"}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	items, err := repo.GetNamespace(context.Background(), "payments")
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected dry-run to avoid persistence, got %+v", items)
	}

	records, err := repo.GetAudit(context.Background(), "payments")
	if err != nil {
		t.Fatalf("get audit: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected dry-run to avoid audit records, got %+v", records)
	}
}

func TestImportYAMLAndExportMaskSecret(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	ownerToken, err := authManager.Generate("owner@example.com", []string{auth.RoleOwner}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	importPayload := `
namespace: payments
items:
  api_token:
    value: super-secret
    type: string
    isSecret: true
  max_retries: 3
`

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/import", ownerToken, importPayload, "application/x-yaml"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected import 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/config/export?namespace=payments&format=json", ownerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected export 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var export domain.ExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &export); err != nil {
		t.Fatalf("unmarshal export response: %v", err)
	}
	if len(export.Items) != 2 {
		t.Fatalf("expected 2 exported items, got %d", len(export.Items))
	}
	for _, item := range export.Items {
		if item.Key == "api_token" && item.Value != "****" {
			t.Fatalf("expected secret export to be masked, got %+v", item)
		}
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/audit?namespace=payments", ownerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected audit 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var auditResponse struct {
		Namespace string               `json:"namespace"`
		Items     []domain.AuditRecord `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auditResponse); err != nil {
		t.Fatalf("unmarshal audit response: %v", err)
	}
	if len(auditResponse.Items) != 2 {
		t.Fatalf("expected 2 audit records, got %d", len(auditResponse.Items))
	}
	for _, record := range auditResponse.Items {
		if record.Key == "api_token" && record.NewValue != "****" {
			t.Fatalf("expected secret audit to be masked, got %+v", record)
		}
	}
}

func TestBulkUpdateAndImportAreMergeOnly(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate editor token: %v", err)
	}
	ownerToken, err := authManager.Generate("owner@example.com", []string{auth.RoleOwner}, time.Hour)
	if err != nil {
		t.Fatalf("generate owner token: %v", err)
	}

	seed := `{"namespace":"payments","entries":[{"key":"timeout","value":"30","type":"int"},{"key":"flag","value":"true","type":"bool"}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, seed, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected seed update 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	updateOne := `{"namespace":"payments","entries":[{"key":"timeout","value":"45","type":"int"}]}`
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, updateOne, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected merge update 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	importOne := `{"namespace":"payments","items":{"timeout":{"value":60,"type":"int"}}}`
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/import", ownerToken, importOne, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected merge import 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/config/export?namespace=payments&format=json", ownerToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected export 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var exported domain.ExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if len(exported.Items) != 2 {
		t.Fatalf("expected omitted key to remain after update/import, got %+v", exported.Items)
	}
	values := map[string]string{}
	for _, item := range exported.Items {
		values[item.Key] = item.Value
	}
	if values["timeout"] != "60" || values["flag"] != "true" {
		t.Fatalf("unexpected merge-only values: %+v", values)
	}
}

func TestBulkUpdateNamespaceLockReturnsLocked(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := mini.Set("config_bulk_lock:payments", "held-lock"); err != nil {
		t.Fatalf("set lock: %v", err)
	}

	body := `{"namespace":"payments","entries":[{"key":"flag","value":"true","type":"bool"}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusLocked {
		t.Fatalf("expected 423, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBulkUpdateReleasesNamespaceLockAfterError(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := mini.Set("audit:payments", "wrong-type"); err != nil {
		t.Fatalf("set wrong audit key type: %v", err)
	}

	body := `{"namespace":"payments","entries":[{"key":"flag","value":"true","type":"bool"}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusInternalServerError {
		t.Fatalf("expected first update to fail with 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	mini.Del("audit:payments")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected lock to be released after error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPIUpdatePublishesEventToSDK(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	sdkClient, err := sdk.NewClient(
		sdk.WithRedisAddr(mini.Addr()),
		sdk.WithLogger(slog.Default()),
		sdk.WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	defer func() { _ = sdkClient.Close() }()
	payments := sdkClient.Namespace("payments")
	if err := sdkClient.Start(context.Background()); err != nil {
		t.Fatalf("start sdk client: %v", err)
	}

	callbackCh := make(chan sdk.Value, 1)
	payments.Watch("flag", func(item sdk.Value) {
		callbackCh <- item
	})

	time.Sleep(100 * time.Millisecond)

	body := `{"value":"true","type":"bool"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/flag", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected update 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case item := <-callbackCh:
		if item.Key != "flag" || item.Raw != "true" || item.Version != 1 {
			t.Fatalf("unexpected sdk callback payload %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for sdk callback")
	}
}

func TestFeatureToggleHotReload(t *testing.T) {
	server, _, authManager, mini := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	sdkClient, err := sdk.NewClient(
		sdk.WithRedisAddr(mini.Addr()),
		sdk.WithLogger(slog.Default()),
		sdk.WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	defer func() { _ = sdkClient.Close() }()
	payments := sdkClient.Namespace("payments")
	if err := sdkClient.Start(context.Background()); err != nil {
		t.Fatalf("start sdk client: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", editorToken, `{"enabled":true}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected feature put 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var feature domain.FeatureToggle
	if err := json.Unmarshal(rec.Body.Bytes(), &feature); err != nil {
		t.Fatalf("unmarshal feature put response: %v", err)
	}
	if feature.Version != 1 {
		t.Fatalf("expected created feature version 1, got %+v", feature)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if payments.IsFeatureEnabled("new-checkout") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !payments.IsFeatureEnabled("new-checkout") {
		t.Fatalf("expected feature toggle to hot-reload to true")
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", editorToken, `{"enabled":false}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected feature update 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feature); err != nil {
		t.Fatalf("unmarshal feature update response: %v", err)
	}
	if feature.Version != 2 {
		t.Fatalf("expected updated feature version 2, got %+v", feature)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !payments.IsFeatureEnabled("new-checkout") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected feature toggle to hot-reload to false")
}

func TestFeaturePutAppearsInAuditEndpoint(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)
	editorToken := mustToken(t, authManager, "editor@example.com", auth.RoleEditor)

	req := newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", editorToken, `{"enabled":true}`, "application/json")
	req.Header.Set("X-Request-ID", "req-feature-audit")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected feature put 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/audit?namespace=payments", editorToken, "", "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected audit 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Namespace string               `json:"namespace"`
		Items     []domain.AuditRecord `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal audit response: %v", err)
	}
	if response.Namespace != "payments" || len(response.Items) != 1 {
		t.Fatalf("unexpected feature audit response: %+v", response)
	}
	record := response.Items[0]
	if record.Key != "new-checkout" || record.Type != "feature" || record.Result != "updated" {
		t.Fatalf("unexpected feature audit metadata: %+v", record)
	}
	if record.OldValue != "" || record.NewValue != "true" || record.Version != 1 || record.IsSecret {
		t.Fatalf("unexpected feature audit values: %+v", record)
	}
	if record.UpdatedBy != "editor@example.com" || record.RequestID != "req-feature-audit" {
		t.Fatalf("unexpected feature audit actor/request: %+v", record)
	}
}

func newTestServer(t *testing.T) (*Server, *redisstorage.ConfigStorage, *auth.Manager, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := redisstorage.NewConfigStorage(client, 100)
	featureRepo := redisstorage.NewFeatureStorage(client, 100)
	metricsRegistry := metrics.NewRegistry()
	svc := configservice.NewService(repo, redisstorage.NewPubSub(client), metricsRegistry)
	featureSvc := featureservice.NewService(featureRepo)
	authManager := auth.NewManager("secret")
	server := NewServer(slog.Default(), svc, featureSvc, authManager, metricsRegistry)
	return server, repo, authManager, mini
}

func mustToken(t *testing.T, authManager *auth.Manager, subject string, roles ...string) string {
	t.Helper()
	token, err := authManager.Generate(subject, roles, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func newRequest(method, path, token, body, contentType string) *stdhttp.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" && (method != stdhttp.MethodGet || body != "") {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(body) == "" && method == stdhttp.MethodGet {
		req.Body = stdhttp.NoBody
	}
	return req
}

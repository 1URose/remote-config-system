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

	req := newRequest(stdhttp.MethodPost, "/config/update", readerToken, `{"namespace":"payments","updatedBy":"reader@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`, "application/json")
	req.Header.Set("Authorization", "Bearer "+readerToken)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestUpdateEndpoint(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := newRequest(stdhttp.MethodPost, "/config/update", editorToken, `{"namespace":"payments","updatedBy":"editor@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`, "application/json")
	req = req.WithContext(context.Background())

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfigCRUDEndpointsWithoutAuth(t *testing.T) {
	server, _, _, _ := newTestServer(t)

	putBody := `{"value":"15","type":"int","expectedVersion":0,"updatedBy":"tester"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", "", putBody, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected put 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/configs/payments/timeout", "", "", "application/json"))
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
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodDelete, "/configs/payments/timeout?updatedBy=tester", "", "", "application/json"))
	if rec.Code != stdhttp.StatusNoContent {
		t.Fatalf("expected delete 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodGet, "/configs/payments/timeout", "", "", "application/json"))
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

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/timeout", "", `{"value":"15","type":"int","expectedVersion":0,"updatedBy":"tester"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("seed payments config: %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/demo-service/app.theme", "", `{"value":"dark","type":"string","expectedVersion":0,"updatedBy":"tester"}`, "application/json"))
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

func TestUpdateEndpointConflict(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	body := `{"namespace":"payments","updatedBy":"editor@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected first update to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != stdhttp.StatusConflict {
		t.Fatalf("expected conflict, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDryRunDoesNotPersist(t *testing.T) {
	server, repo, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	body := `{"namespace":"payments","updatedBy":"editor@example.com","dryRun":true,"entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`
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
updatedBy: owner@example.com
items:
  api_token:
    value: super-secret
    type: string
    expectedVersion: 0
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

func TestAPIUpdatePublishesEventToSDK(t *testing.T) {
	server, _, _, mini := newTestServer(t)

	sdkClient, err := sdk.NewClient(
		sdk.WithRedisAddr(mini.Addr()),
		sdk.WithNamespace("payments"),
		sdk.WithLogger(slog.Default()),
		sdk.WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	defer func() { _ = sdkClient.Close() }()
	if err := sdkClient.Start(context.Background()); err != nil {
		t.Fatalf("start sdk client: %v", err)
	}

	callbackCh := make(chan sdk.Value, 1)
	sdkClient.Watch("flag", func(item sdk.Value) {
		callbackCh <- item
	})

	time.Sleep(100 * time.Millisecond)

	body := `{"value":"true","type":"bool","expectedVersion":0,"updatedBy":"editor@example.com"}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/configs/payments/flag", "", body, "application/json"))
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
	server, _, _, mini := newTestServer(t)

	sdkClient, err := sdk.NewClient(
		sdk.WithRedisAddr(mini.Addr()),
		sdk.WithNamespace("payments"),
		sdk.WithLogger(slog.Default()),
		sdk.WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	defer func() { _ = sdkClient.Close() }()
	if err := sdkClient.Start(context.Background()); err != nil {
		t.Fatalf("start sdk client: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", "", `{"enabled":true,"expectedVersion":0,"updatedBy":"editor@example.com"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected feature put 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sdkClient.IsFeatureEnabled("new-checkout") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !sdkClient.IsFeatureEnabled("new-checkout") {
		t.Fatalf("expected feature toggle to hot-reload to true")
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(stdhttp.MethodPut, "/features/payments/new-checkout", "", `{"enabled":false,"expectedVersion":1,"updatedBy":"editor@example.com"}`, "application/json"))
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected feature update 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !sdkClient.IsFeatureEnabled("new-checkout") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected feature toggle to hot-reload to false")
}

func newTestServer(t *testing.T) (*Server, *redisstorage.ConfigStorage, *auth.Manager, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := redisstorage.NewConfigStorage(client, 100)
	featureRepo := redisstorage.NewFeatureStorage(client)
	metricsRegistry := metrics.NewRegistry()
	svc := configservice.NewService(repo, redisstorage.NewPubSub(client), metricsRegistry)
	featureSvc := featureservice.NewService(featureRepo)
	authManager := auth.NewManager("secret")
	server := NewServer(slog.Default(), svc, featureSvc, authManager, metricsRegistry)
	return server, repo, authManager, mini
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

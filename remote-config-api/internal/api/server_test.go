package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"log/slog"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/auth"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/metrics"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/repository"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/service"
	sdkmodel "github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
	sdkremoteconfig "github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/remoteconfig"
)

func TestRBACForbidden(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	readerToken, err := authManager.Generate("reader@example.com", []string{auth.RoleReader}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := newRequest(http.MethodPost, "/config/update", readerToken, `{"namespace":"payments","updatedBy":"reader@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`, "application/json")
	req.Header.Set("Authorization", "Bearer "+readerToken)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestUpdateEndpoint(t *testing.T) {
	server, _, authManager, _ := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := newRequest(http.MethodPost, "/config/update", editorToken, `{"namespace":"payments","updatedBy":"editor@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`, "application/json")
	req = req.WithContext(context.Background())

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
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
	server.Handler().ServeHTTP(rec, newRequest(http.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected first update to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(http.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != http.StatusConflict {
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
	server.Handler().ServeHTTP(rec, newRequest(http.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != http.StatusOK {
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
	server.Handler().ServeHTTP(rec, newRequest(http.MethodPost, "/config/import", ownerToken, importPayload, "application/x-yaml"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected import 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(http.MethodGet, "/config/export?namespace=payments&format=json", ownerToken, "", "application/json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected export 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var export model.ExportResponse
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
	server.Handler().ServeHTTP(rec, newRequest(http.MethodGet, "/audit?namespace=payments", ownerToken, "", "application/json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected audit 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var auditResponse struct {
		Namespace string              `json:"namespace"`
		Items     []model.AuditRecord `json:"items"`
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
	server, _, authManager, mini := newTestServer(t)

	editorToken, err := authManager.Generate("editor@example.com", []string{auth.RoleEditor}, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	sdkClient, err := sdkremoteconfig.New(context.Background(), sdkremoteconfig.Options{
		RedisAddr:     mini.Addr(),
		Namespaces:    []string{"payments"},
		Logger:        slog.Default(),
		RetryInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new sdk client: %v", err)
	}
	defer func() { _ = sdkClient.Close() }()

	callbackCh := make(chan sdkmodel.ConfigItem, 1)
	sdkClient.Watch("payments", "flag", func(item sdkmodel.ConfigItem) {
		callbackCh <- item
	})

	time.Sleep(100 * time.Millisecond)

	body := `{"namespace":"payments","updatedBy":"editor@example.com","entries":[{"key":"flag","value":"true","type":"bool","expectedVersion":0}]}`
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newRequest(http.MethodPost, "/config/update", editorToken, body, "application/json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected update 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case item := <-callbackCh:
		if item.Key != "flag" || item.Value != "true" || item.Version != 1 {
			t.Fatalf("unexpected sdk callback payload %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for sdk callback")
	}
}

func newTestServer(t *testing.T) (*Server, *repository.RedisRepository, *auth.Manager, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := repository.NewRedisRepository(client, 100)
	metricsRegistry := metrics.NewRegistry()
	svc := service.NewConfigService(repo, metricsRegistry)
	authManager := auth.NewManager("secret")
	server := NewServer(":0", slog.Default(), svc, authManager, metricsRegistry)
	return server, repo, authManager, mini
}

func newRequest(method, path, token, body, contentType string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" && (method != http.MethodGet || body != "") {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(body) == "" && method == http.MethodGet {
		req.Body = http.NoBody
	}
	return req
}

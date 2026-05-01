package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/auth"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/metrics"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/repository"
	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/service"
)

func TestDocsAndSpecEndpoints(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := repository.NewRedisRepository(client, 100)
	metricsRegistry := metrics.NewRegistry()
	svc := service.NewConfigService(repo, metricsRegistry)
	authManager := auth.NewManager("secret")
	server := NewServer(":0", slog.Default(), svc, authManager, metricsRegistry)

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected docs 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("unexpected content-type %q", ct)
	}

	req = httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected spec 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("unexpected spec content-type %q", ct)
	}
}

package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/1URose/remote-config-system/internal/auth"
	"github.com/1URose/remote-config-system/internal/metrics"
	configservice "github.com/1URose/remote-config-system/internal/services/config"
	featureservice "github.com/1URose/remote-config-system/internal/services/feature"
	redisstorage "github.com/1URose/remote-config-system/internal/storage/redis"
)

func TestSwaggerEndpoints(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := redisstorage.NewConfigStorage(client, 100)
	featureRepo := redisstorage.NewFeatureStorage(client, 100)
	metricsRegistry := metrics.NewRegistry()
	svc := configservice.NewService(repo, redisstorage.NewPubSub(client), metricsRegistry)
	featureSvc := featureservice.NewService(featureRepo)
	authManager := auth.NewManager("secret")
	server := NewServer(slog.Default(), svc, featureSvc, authManager, metricsRegistry)

	req := httptest.NewRequest(stdhttp.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusTemporaryRedirect {
		t.Fatalf("expected docs redirect 307, got %d", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/swagger/index.html" {
		t.Fatalf("unexpected redirect location %q", location)
	}

	req = httptest.NewRequest(stdhttp.MethodGet, "/swagger/index.html", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected swagger index 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("unexpected swagger index content-type %q", ct)
	}

	req = httptest.NewRequest(stdhttp.MethodGet, "/swagger/doc.json", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected swagger doc 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("unexpected swagger doc content-type %q", ct)
	}
}

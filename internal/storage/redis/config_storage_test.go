package redis

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/1URose/remote-config-system/internal/domain"
)

func TestUpdateVersionConflict(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "feature_x_enabled", Value: "true", Type: "bool", ExpectedVersion: 0},
		},
	}

	if _, err := repo.Update(ctx, req, "req-1"); err != nil {
		t.Fatalf("unexpected update error: %v", err)
	}
	if _, err := repo.Update(ctx, req, "req-2"); err == nil {
		t.Fatalf("expected version conflict")
	}
}

func TestDryRunDoesNotPersistOrAudit(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		DryRun:    true,
		Entries: []domain.ConfigUpdateEntry{
			{Key: "feature_x_enabled", Value: "true", Type: "bool", ExpectedVersion: 0},
		},
	}

	if _, err := repo.Update(ctx, req, "req-1"); err != nil {
		t.Fatalf("unexpected dry-run error: %v", err)
	}

	items, err := repo.GetNamespace(ctx, "payments")
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no persisted items after dry-run, got %+v", items)
	}

	audit, err := repo.GetAudit(ctx, "payments")
	if err != nil {
		t.Fatalf("get audit: %v", err)
	}
	if len(audit) != 0 {
		t.Fatalf("expected no audit after dry-run, got %+v", audit)
	}
}

func TestAuditMasksSecrets(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "api_token", Value: "super-secret", Type: "string", ExpectedVersion: 0, IsSecret: true},
		},
	}

	if _, err := repo.Update(ctx, req, "req-1"); err != nil {
		t.Fatalf("unexpected update error: %v", err)
	}

	records, err := repo.GetAudit(ctx, "payments")
	if err != nil {
		t.Fatalf("get audit: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].NewValue != "****" {
		t.Fatalf("expected masked secret in audit, got %+v", records[0])
	}

	raw, err := mini.List("audit:payments")
	if err != nil {
		t.Fatalf("list raw audit: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected raw audit list entry")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw[0]), &payload); err != nil {
		t.Fatalf("unmarshal raw audit: %v", err)
	}
	if payload["newValue"] != "****" {
		t.Fatalf("expected masked raw audit payload, got %+v", payload)
	}
}

func TestDeleteConfigPublishesDeleteEvent(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "30", Type: "int", ExpectedVersion: 0},
		},
	}

	if _, err := repo.Update(ctx, req, "req-1"); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := repo.DeleteKey(ctx, "payments", "timeout", "admin@example.com", "req-2"); err != nil {
		t.Fatalf("delete config: %v", err)
	}

	if mini.Exists("config:payments:timeout") {
		t.Fatalf("expected deleted config key to be removed from redis")
	}
}

func TestListNamespaces(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	requests := []domain.ConfigUpdateRequest{
		{
			Namespace: "payments",
			UpdatedBy: "admin@example.com",
			Entries: []domain.ConfigUpdateEntry{
				{Key: "timeout", Value: "30", Type: "int", ExpectedVersion: 0},
			},
		},
		{
			Namespace: "demo-service",
			UpdatedBy: "admin@example.com",
			Entries: []domain.ConfigUpdateEntry{
				{Key: "app.theme", Value: "dark", Type: "string", ExpectedVersion: 0},
			},
		},
	}

	for i, req := range requests {
		if _, err := repo.Update(ctx, req, "req-test"); err != nil {
			t.Fatalf("seed request %d: %v", i, err)
		}
	}

	namespaces, err := repo.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	if len(namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %+v", namespaces)
	}
	if namespaces[0] != "demo-service" || namespaces[1] != "payments" {
		t.Fatalf("unexpected namespaces %+v", namespaces)
	}
}

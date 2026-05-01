package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
)

func TestUpdateVersionConflict(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewRedisRepository(client, 50)

	ctx := context.Background()
	req := model.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []model.ConfigUpdateEntry{
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
	repo := NewRedisRepository(client, 50)

	ctx := context.Background()
	req := model.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		DryRun:    true,
		Entries: []model.ConfigUpdateEntry{
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
	repo := NewRedisRepository(client, 50)

	ctx := context.Background()
	req := model.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []model.ConfigUpdateEntry{
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

	raw, err := mini.List("cfgaudit:payments")
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

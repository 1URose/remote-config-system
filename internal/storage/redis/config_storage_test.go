package redis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/1URose/remote-config-system/internal/domain"
)

func TestUpdateComputesNextVersion(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	req := domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "feature_x_enabled", Value: "true", Type: "bool"},
		},
	}

	items, err := repo.Update(ctx, req, "req-1")
	if err != nil {
		t.Fatalf("unexpected update error: %v", err)
	}
	if len(items) != 1 || items[0].Version != 1 {
		t.Fatalf("expected first write version 1, got %+v", items)
	}

	req.Entries[0].Value = "false"
	items, err = repo.Update(ctx, req, "req-2")
	if err != nil {
		t.Fatalf("unexpected second update error: %v", err)
	}
	if len(items) != 1 || items[0].Value != "false" || items[0].Version != 2 {
		t.Fatalf("expected server-computed version 2, got %+v", items)
	}
}

func TestUpsertKeyCreatesAndUpdatesWithoutExpectedVersion(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	item, err := repo.UpsertKey(ctx, "payments", "timeout", "30", "int", false, "admin@example.com", "req-1")
	if err != nil {
		t.Fatalf("create config key: %v", err)
	}
	if item.Value != "30" || item.Version != 1 {
		t.Fatalf("unexpected created item %+v", item)
	}

	item, err = repo.UpsertKey(ctx, "payments", "timeout", "45", "int", false, "admin@example.com", "req-2")
	if err != nil {
		t.Fatalf("update config key: %v", err)
	}
	if item.Value != "45" || item.Version != 2 {
		t.Fatalf("expected server-computed version 2, got %+v", item)
	}
}

func TestUpsertKeyPublishesEvent(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pubsub := client.Subscribe(ctx, updatesChannel("payments"))
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if _, err := repo.UpsertKey(ctx, "payments", "timeout", "30", "int", false, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("upsert config key: %v", err)
	}

	msg, err := pubsub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive single-key update event: %v", err)
	}
	var event domain.ConfigUpdateEvent
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		t.Fatalf("unmarshal update event: %v", err)
	}
	if event.Namespace != "payments" || event.Resource != "config" || event.Operation != "updated" {
		t.Fatalf("unexpected update event metadata: %+v", event)
	}
	if len(event.Keys) != 1 || event.Keys[0] != "timeout" {
		t.Fatalf("unexpected update event keys: %+v", event.Keys)
	}
}

func TestUpsertKeyReturnsResourceLockedWithoutWritingOrPublishing(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if _, err := repo.UpsertKey(ctx, "payments", "timeout", "30", "int", false, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("seed config key: %v", err)
	}

	pubsub := client.Subscribe(ctx, updatesChannel("payments"))
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := mini.Set(configWriteLockKey("payments", "timeout"), "held-lock"); err != nil {
		t.Fatalf("set config write lock: %v", err)
	}

	_, err := repo.UpsertKey(ctx, "payments", "timeout", "45", "int", false, "admin@example.com", "req-2")
	var locked *domain.ResourceLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected resource locked error, got %v", err)
	}

	stored, err := repo.GetKey(ctx, "payments", "timeout")
	if err != nil {
		t.Fatalf("get config key: %v", err)
	}
	if stored.Value != "30" || stored.Version != 1 {
		t.Fatalf("expected locked write to leave value unchanged, got %+v", stored)
	}

	if msg, err := pubsub.ReceiveTimeout(ctx, 100*time.Millisecond); err == nil {
		t.Fatalf("expected no event for locked write, got %+v", msg)
	}
}

func TestUpsertKeyReturnsNamespaceLockedWhenBulkLockHeld(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if err := mini.Set(bulkLockKey("payments"), "held-lock"); err != nil {
		t.Fatalf("set namespace lock: %v", err)
	}

	_, err := repo.UpsertKey(ctx, "payments", "timeout", "30", "int", false, "admin@example.com", "req-1")
	var locked *domain.NamespaceLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected namespace locked error, got %v", err)
	}
	if mini.Exists(configRedisKey("payments", "timeout")) {
		t.Fatalf("expected namespace locked write not to create config key")
	}
}

func TestUpsertKeyReleasesWriteLockAfterSuccess(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	if _, err := repo.UpsertKey(context.Background(), "payments", "timeout", "30", "int", false, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("upsert config key: %v", err)
	}
	if mini.Exists(configWriteLockKey("payments", "timeout")) {
		t.Fatalf("expected config write lock to be released after success")
	}
}

func TestUpsertKeyReleasesWriteLockAfterError(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if err := client.Set(ctx, auditListKey("payments"), "wrong-type", 0).Err(); err != nil {
		t.Fatalf("seed wrong audit key type: %v", err)
	}

	_, err := repo.UpsertKey(ctx, "payments", "timeout", "30", "int", false, "admin@example.com", "req-1")
	if err == nil {
		t.Fatalf("expected upsert error")
	}
	if mini.Exists(configWriteLockKey("payments", "timeout")) {
		t.Fatalf("expected config write lock to be released after error")
	}
	if mini.Exists(configRedisKey("payments", "timeout")) {
		t.Fatalf("expected failed upsert not to create config key")
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
			{Key: "feature_x_enabled", Value: "true", Type: "bool"},
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

func TestUpdatePublishesEvent(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pubsub := client.Subscribe(ctx, updatesChannel("payments"))
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if _, err := repo.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "30", Type: "int"},
			{Key: "flag", Value: "true", Type: "bool"},
		},
	}, "req-1"); err != nil {
		t.Fatalf("update config: %v", err)
	}

	msg, err := pubsub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive update event: %v", err)
	}
	var event domain.ConfigUpdateEvent
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		t.Fatalf("unmarshal update event: %v", err)
	}
	if event.Namespace != "payments" || event.Resource != "config" || event.Operation != "updated" {
		t.Fatalf("unexpected update event metadata: %+v", event)
	}
	if len(event.Keys) != 2 || event.Keys[0] != "timeout" || event.Keys[1] != "flag" {
		t.Fatalf("unexpected update event keys: %+v", event.Keys)
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
			{Key: "api_token", Value: "super-secret", Type: "string", IsSecret: true},
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
			{Key: "timeout", Value: "30", Type: "int"},
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
				{Key: "timeout", Value: "30", Type: "int"},
			},
		},
		{
			Namespace: "demo-service",
			UpdatedBy: "admin@example.com",
			Entries: []domain.ConfigUpdateEntry{
				{Key: "app.theme", Value: "dark", Type: "string"},
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

func TestUpdateDoesNotDeleteOmittedKeys(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if _, err := repo.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "30", Type: "int"},
			{Key: "flag", Value: "true", Type: "bool"},
		},
	}, "req-1"); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := repo.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "45", Type: "int"},
		},
	}, "req-2"); err != nil {
		t.Fatalf("merge update: %v", err)
	}

	items, err := repo.GetNamespace(ctx, "payments")
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected omitted key to remain, got %+v", items)
	}
}

func TestUpdateReturnsNamespaceLocked(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if ok, err := repo.acquireNamespaceBulkLock(ctx, "payments", "held-lock"); err != nil || !ok {
		t.Fatalf("acquire test lock: locked=%v err=%v", ok, err)
	}

	_, err := repo.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "30", Type: "int"},
		},
	}, "req-1")
	var locked *domain.NamespaceLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected namespace locked error, got %v", err)
	}
}

func TestUpdateReleasesNamespaceLockAfterError(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewConfigStorage(client, 50)

	ctx := context.Background()
	if err := client.Set(ctx, auditListKey("payments"), "wrong-type", 0).Err(); err != nil {
		t.Fatalf("seed wrong audit key type: %v", err)
	}

	_, err := repo.Update(ctx, domain.ConfigUpdateRequest{
		Namespace: "payments",
		UpdatedBy: "admin@example.com",
		Entries: []domain.ConfigUpdateEntry{
			{Key: "timeout", Value: "30", Type: "int"},
		},
	}, "req-1")
	if err == nil {
		t.Fatalf("expected update error")
	}
	if mini.Exists(bulkLockKey("payments")) {
		t.Fatalf("expected namespace lock to be released after update error")
	}
}

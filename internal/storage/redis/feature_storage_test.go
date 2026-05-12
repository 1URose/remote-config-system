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

func TestFeatureUpsertAndGet(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	ctx := context.Background()
	item, err := repo.Upsert(ctx, "payments", "new-ui", true, "admin@example.com", "req-1")
	if err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	if !item.Enabled || item.Version != 1 {
		t.Fatalf("unexpected inserted feature %+v", item)
	}

	item, err = repo.Upsert(ctx, "payments", "new-ui", false, "admin@example.com", "req-2")
	if err != nil {
		t.Fatalf("update feature: %v", err)
	}
	if item.Enabled || item.Version != 2 {
		t.Fatalf("unexpected updated feature %+v", item)
	}

	stored, err := repo.GetKey(ctx, "payments", "new-ui")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if stored.Enabled || stored.Version != 2 {
		t.Fatalf("unexpected stored feature %+v", stored)
	}
}

func TestFeatureUpsertReturnsResourceLockedWithoutWritingOrPublishing(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	ctx := context.Background()
	if _, err := repo.Upsert(ctx, "payments", "new-ui", true, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	pubsub := client.Subscribe(ctx, updatesChannel("payments"))
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := mini.Set(featureWriteLockKey("payments", "new-ui"), "held-lock"); err != nil {
		t.Fatalf("set feature write lock: %v", err)
	}

	_, err := repo.Upsert(ctx, "payments", "new-ui", false, "admin@example.com", "req-2")
	var locked *domain.ResourceLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected resource locked error, got %v", err)
	}

	stored, err := repo.GetKey(ctx, "payments", "new-ui")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if !stored.Enabled || stored.Version != 1 {
		t.Fatalf("expected locked write to leave feature unchanged, got %+v", stored)
	}

	if msg, err := pubsub.ReceiveTimeout(ctx, 100*time.Millisecond); err == nil {
		t.Fatalf("expected no event for locked write, got %+v", msg)
	}
}

func TestFeatureUpsertPublishesEvent(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pubsub := client.Subscribe(ctx, updatesChannel("payments"))
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if _, err := repo.Upsert(ctx, "payments", "new-ui", true, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}

	msg, err := pubsub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive feature update event: %v", err)
	}
	var event domain.ConfigUpdateEvent
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		t.Fatalf("unmarshal feature event: %v", err)
	}
	if event.Namespace != "payments" || event.Resource != "feature" || event.Operation != "updated" {
		t.Fatalf("unexpected feature event metadata: %+v", event)
	}
	if len(event.Keys) != 1 || event.Keys[0] != "new-ui" {
		t.Fatalf("unexpected feature event keys: %+v", event.Keys)
	}
}

func TestFeatureUpsertReleasesWriteLockAfterSuccess(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	if _, err := repo.Upsert(context.Background(), "payments", "new-ui", true, "admin@example.com", "req-1"); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	if mini.Exists(featureWriteLockKey("payments", "new-ui")) {
		t.Fatalf("expected feature write lock to be released after success")
	}
}

func TestFeatureUpsertReleasesWriteLockAfterError(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	ctx := context.Background()
	if err := client.Set(ctx, featureSetKey("payments"), "wrong-type", 0).Err(); err != nil {
		t.Fatalf("seed wrong feature set type: %v", err)
	}

	_, err := repo.Upsert(ctx, "payments", "new-ui", true, "admin@example.com", "req-1")
	if err == nil {
		t.Fatalf("expected feature upsert error")
	}
	if mini.Exists(featureWriteLockKey("payments", "new-ui")) {
		t.Fatalf("expected feature write lock to be released after error")
	}
	if mini.Exists(featureRedisKey("payments", "new-ui")) {
		t.Fatalf("expected failed feature upsert not to create feature key")
	}
}

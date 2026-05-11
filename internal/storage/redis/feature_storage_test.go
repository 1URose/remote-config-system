package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestFeatureUpsertAndGet(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	repo := NewFeatureStorage(client)

	ctx := context.Background()
	item, err := repo.Upsert(ctx, "payments", "new-ui", true, 0, "admin@example.com", "req-1")
	if err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	if !item.Enabled || item.Version != 1 {
		t.Fatalf("unexpected inserted feature %+v", item)
	}

	item, err = repo.Upsert(ctx, "payments", "new-ui", false, 1, "admin@example.com", "req-2")
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

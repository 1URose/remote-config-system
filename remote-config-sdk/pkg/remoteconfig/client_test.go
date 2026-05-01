package remoteconfig

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

func TestInitialLoadAndFallback(t *testing.T) {
	mini := miniredis.RunT(t)
	seedConfig(t, mini.Addr(), "payments", "flag", "true", "bool", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:     mini.Addr(),
		Namespaces:    []string{"payments"},
		Logger:        slog.Default(),
		RetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	value, err := client.GetString("payments", "flag")
	if err != nil || value != "true" {
		t.Fatalf("unexpected initial load value=%q err=%v", value, err)
	}

	mini.Close()

	value, err = client.GetString("payments", "flag")
	if err != nil || value != "true" {
		t.Fatalf("expected last-known-good cache after redis outage value=%q err=%v", value, err)
	}
}

func TestReloadKeys(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag_a", "true", "bool", 1)
	seedConfig(t, addr, "payments", "flag_b", "1", "int", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:  addr,
		Namespaces: []string{"payments"},
		Logger:     slog.Default(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 2)
	if err := client.ReloadKeys("payments", []string{"flag_b"}); err != nil {
		t.Fatalf("reload keys: %v", err)
	}

	valueA, _ := client.GetString("payments", "flag_a")
	valueB, _ := client.GetString("payments", "flag_b")
	if valueA != "true" || valueB != "2" {
		t.Fatalf("expected point reload only for changed key, got A=%s B=%s", valueA, valueB)
	}
}

func TestWatchCallbackAfterReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag", "true", "bool", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:     addr,
		Namespaces:    []string{"payments"},
		Logger:        slog.Default(),
		RetryInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	callbackCh := make(chan model.ConfigItem, 1)
	client.Watch("payments", "flag", func(item model.ConfigItem) {
		if item.Version >= 2 {
			callbackCh <- item
		}
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	if err := client.ReloadKeys("payments", []string{"flag"}); err != nil {
		t.Fatalf("reload keys: %v", err)
	}

	select {
	case item := <-callbackCh:
		if item.Value != "false" {
			t.Fatalf("unexpected callback item %+v", item)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for callback")
	}
}

func TestPubSubHotReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag", "true", "bool", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:     addr,
		Namespaces:    []string{"payments"},
		Logger:        slog.Default(),
		RetryInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	callbackCh := make(chan model.ConfigItem, 1)
	client.Watch("payments", "flag", func(item model.ConfigItem) {
		callbackCh <- item
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	rc := redis.NewClient(&redis.Options{Addr: addr})
	payload, err := json.Marshal(model.ConfigUpdateEvent{
		Namespace: "payments",
		Keys:      []string{"flag"},
		Operation: "updated",
		UpdatedAt: time.Now().UTC(),
		UpdatedBy: "tester",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := rc.Publish(context.Background(), "cfgupdates:payments", payload).Err(); err != nil {
		t.Fatalf("publish event: %v", err)
	}

	select {
	case item := <-callbackCh:
		if item.Value != "false" || item.Version != 2 {
			t.Fatalf("unexpected hot-reload item %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for pubsub hot-reload")
	}
}

func TestFlushReloadsNamespace(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag_a", "true", "bool", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:  addr,
		Namespaces: []string{"payments"},
		Logger:     slog.Default(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 1)
	if err := client.Flush("payments"); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, err := client.GetRaw("payments", "flag_b"); err != nil {
		t.Fatalf("expected namespace to be fully reloaded after flush: %v", err)
	}
	stats := client.Stats()
	if stats.Flushes == 0 || stats.Reloads == 0 {
		t.Fatalf("expected flush and reload counters to advance, got %+v", stats)
	}
}

func TestFlushRestoresLastKnownGoodOnReloadError(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag", "true", "bool", 1)

	client, err := New(context.Background(), Options{
		RedisAddr:     addr,
		Namespaces:    []string{"payments"},
		Logger:        slog.Default(),
		RetryInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	mini.Close()

	if err := client.Flush("payments"); err == nil {
		t.Fatalf("expected flush to fail when redis is unavailable")
	}

	value, err := client.GetString("payments", "flag")
	if err != nil || value != "true" {
		t.Fatalf("expected last-known-good cache to be restored after failed flush value=%q err=%v", value, err)
	}
}

func seedConfig(t *testing.T, addr, namespace, key, value, kind string, version int64) {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rc.HSet(ctx, "cfg:"+namespace+":"+key,
		"namespace", namespace,
		"key", key,
		"value", value,
		"type", kind,
		"version", version,
		"is_secret", "0",
		"updated_at", time.Now().UTC().Format(time.RFC3339Nano),
		"updated_by", "seed",
	).Err(); err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	if err := rc.SAdd(ctx, "cfgkeys:"+namespace, key).Err(); err != nil {
		t.Fatalf("seed set: %v", err)
	}
}

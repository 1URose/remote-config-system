package sdk

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInitialLoadAndFallback(t *testing.T) {
	mini := miniredis.RunT(t)
	seedConfig(t, mini.Addr(), "payments", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(mini.Addr()),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
		WithRetryInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	value, ok := client.GetString("flag")
	if !ok || value != "true" {
		t.Fatalf("unexpected initial load value=%q ok=%v", value, ok)
	}

	mini.Close()

	value, ok = client.GetString("flag")
	if !ok || value != "true" {
		t.Fatalf("expected last-known-good cache after redis outage value=%q ok=%v", value, ok)
	}
}

func TestReloadKeys(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag_a", "true", "bool", 1)
	seedConfig(t, addr, "payments", "flag_b", "1", "int", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 2)
	if err := client.ReloadKeys([]string{"flag_b"}); err != nil {
		t.Fatalf("reload keys: %v", err)
	}

	valueA, _ := client.GetString("flag_a")
	valueB, _ := client.GetString("flag_b")
	if valueA != "true" || valueB != "2" {
		t.Fatalf("expected point reload only for changed key, got A=%s B=%s", valueA, valueB)
	}
}

func TestWatchCallbackAfterReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	callbackCh := make(chan Value, 1)
	client.Watch("flag", func(item Value) {
		if item.Version >= 2 {
			callbackCh <- item
		}
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	if err := client.ReloadKeys([]string{"flag"}); err != nil {
		t.Fatalf("reload keys: %v", err)
	}

	select {
	case item := <-callbackCh:
		if item.Raw != "false" {
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

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	callbackCh := make(chan Value, 1)
	client.Watch("flag", func(item Value) {
		callbackCh <- item
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	rc := redis.NewClient(&redis.Options{Addr: addr})
	payload, err := json.Marshal(map[string]any{
		"namespace": "payments",
		"keys":      []string{"flag"},
		"operation": "updated",
		"updatedAt": time.Now().UTC(),
		"updatedBy": "tester",
		"requestId": "req-1",
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := rc.Publish(context.Background(), "events:payments", payload).Err(); err != nil {
		t.Fatalf("publish event: %v", err)
	}

	select {
	case item := <-callbackCh:
		if item.Raw != "false" || item.Version != 2 {
			t.Fatalf("unexpected hot-reload item %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for pubsub hot-reload")
	}
}

func TestFeatureToggleInitialLoadAndHotReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedFeature(t, addr, "payments", "new-checkout", false, 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	if client.IsFeatureEnabled("new-checkout") {
		t.Fatalf("expected initial feature state to be false")
	}
	feature, ok := client.GetFeature("new-checkout")
	if !ok {
		t.Fatalf("expected feature to be present")
	}
	if feature.Enabled || feature.Version != 1 {
		t.Fatalf("unexpected feature payload %+v", feature)
	}
	if _, ok := client.GetFeature("missing-feature"); ok {
		t.Fatalf("expected missing feature to be absent")
	}

	seedFeature(t, addr, "payments", "new-checkout", true, 2)
	rc := redis.NewClient(&redis.Options{Addr: addr})
	payload, err := json.Marshal(map[string]any{
		"namespace": "payments",
		"resource":  "feature",
		"keys":      []string{"new-checkout"},
		"operation": "updated",
		"updatedAt": time.Now().UTC(),
		"updatedBy": "tester",
		"requestId": "req-2",
	})
	if err != nil {
		t.Fatalf("marshal feature event: %v", err)
	}
	if err := rc.Publish(context.Background(), "events:payments", payload).Err(); err != nil {
		t.Fatalf("publish feature event: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsFeatureEnabled("new-checkout") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected feature state to hot-reload to true")
}

func TestFlushReloadsNamespace(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag_a", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 1)
	if err := client.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, ok := client.GetRaw("flag_b"); !ok {
		t.Fatalf("expected namespace to be fully reloaded after flush")
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

	client, err := NewClient(
		WithRedisAddr(addr),
		WithNamespace("payments"),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	mini.Close()

	if err := client.Flush(); err == nil {
		t.Fatalf("expected flush to fail when redis is unavailable")
	}

	value, ok := client.GetString("flag")
	if !ok || value != "true" {
		t.Fatalf("expected last-known-good cache to be restored after failed flush value=%q ok=%v", value, ok)
	}
}

func seedConfig(t *testing.T, addr, namespace, key, value, kind string, version int64) {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rc.HSet(ctx, "config:"+namespace+":"+key,
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
	if err := rc.SAdd(ctx, "config_keys:"+namespace, key).Err(); err != nil {
		t.Fatalf("seed set: %v", err)
	}
}

func seedFeature(t *testing.T, addr, namespace, key string, enabled bool, version int64) {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rc.HSet(ctx, "feature:"+namespace+":"+key,
		"namespace", namespace,
		"key", key,
		"enabled", enabled,
		"version", version,
		"updated_at", time.Now().UTC().Format(time.RFC3339Nano),
		"updated_by", "seed",
	).Err(); err != nil {
		t.Fatalf("seed feature hash: %v", err)
	}
	if err := rc.SAdd(ctx, "feature_keys:"+namespace, key).Err(); err != nil {
		t.Fatalf("seed feature set: %v", err)
	}
}

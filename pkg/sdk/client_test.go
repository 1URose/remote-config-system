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

func TestStartLoadsRegisteredNamespaces(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "app", "title", "Remote Config", "string", 1)
	seedConfig(t, addr, "pricing", "discount.percent", "25", "int", 1)
	seedFeature(t, addr, "features", "checkout_enabled", true, 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	app := client.Namespace("app")
	pricing := client.Namespace("pricing")
	features := client.Namespace("features")

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	title, ok := app.GetString("title")
	if !ok || title != "Remote Config" {
		t.Fatalf("unexpected app title value=%q ok=%v", title, ok)
	}
	discount, ok := pricing.GetInt("discount.percent")
	if !ok || discount != 25 {
		t.Fatalf("unexpected pricing discount value=%d ok=%v", discount, ok)
	}
	if !features.IsFeatureEnabled("checkout_enabled") {
		t.Fatalf("expected checkout feature to be enabled")
	}
}

func TestInitialLoadAndFallback(t *testing.T) {
	mini := miniredis.RunT(t)
	seedConfig(t, mini.Addr(), "payments", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(mini.Addr()),
		WithLogger(slog.Default()),
		WithRetryInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	value, ok := payments.GetString("flag")
	if !ok || value != "true" {
		t.Fatalf("unexpected initial load value=%q ok=%v", value, ok)
	}

	mini.Close()

	value, ok = payments.GetString("flag")
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
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 2)
	if err := payments.ReloadKeys([]string{"flag_b"}); err != nil {
		t.Fatalf("reload keys: %v", err)
	}

	valueA, _ := payments.GetString("flag_a")
	valueB, _ := payments.GetString("flag_b")
	if valueA != "true" || valueB != "2" {
		t.Fatalf("expected point reload only for changed key, got A=%s B=%s", valueA, valueB)
	}
}

func TestWatchAndWatchNamespaceStayInNamespace(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "app", "flag", "true", "bool", 1)
	seedConfig(t, addr, "pricing", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	app := client.Namespace("app")
	pricing := client.Namespace("pricing")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}
	waitForReloads(t, client, 4)

	appKeyCh := make(chan Value, 1)
	appNamespaceCh := make(chan []Value, 1)
	pricingKeyCh := make(chan Value, 1)
	pricingNamespaceCh := make(chan []Value, 1)
	app.Watch("flag", func(item Value) {
		appKeyCh <- item
	})
	app.WatchNamespace(func(items []Value) {
		appNamespaceCh <- items
	})
	pricing.Watch("flag", func(item Value) {
		pricingKeyCh <- item
	})
	pricing.WatchNamespace(func(items []Value) {
		pricingNamespaceCh <- items
	})

	seedConfig(t, addr, "pricing", "flag", "false", "bool", 2)
	if err := pricing.ReloadKeys([]string{"flag"}); err != nil {
		t.Fatalf("reload pricing key: %v", err)
	}

	select {
	case item := <-pricingKeyCh:
		if item.Namespace != "pricing" || item.Key != "flag" || item.Raw != "false" {
			t.Fatalf("unexpected pricing key callback payload %+v", item)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for pricing key callback")
	}

	select {
	case items := <-pricingNamespaceCh:
		if len(items) != 1 || items[0].Namespace != "pricing" || items[0].Key != "flag" {
			t.Fatalf("unexpected pricing namespace callback payload %+v", items)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for pricing namespace callback")
	}

	select {
	case item := <-appKeyCh:
		t.Fatalf("unexpected app key callback payload %+v", item)
	case items := <-appNamespaceCh:
		t.Fatalf("unexpected app namespace callback payload %+v", items)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchCallbackAfterReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "payments", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	callbackCh := make(chan Value, 1)
	payments.Watch("flag", func(item Value) {
		if item.Version >= 2 {
			callbackCh <- item
		}
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	if err := payments.ReloadKeys([]string{"flag"}); err != nil {
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
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}
	waitForReloads(t, client, 2)

	callbackCh := make(chan Value, 1)
	payments.Watch("flag", func(item Value) {
		if item.Version >= 2 {
			callbackCh <- item
		}
	})

	seedConfig(t, addr, "payments", "flag", "false", "bool", 2)
	publishUpdateEvent(t, addr, "payments", "config", []string{"flag"}, "req-1")

	select {
	case item := <-callbackCh:
		if item.Raw != "false" || item.Version != 2 {
			t.Fatalf("unexpected hot-reload item %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for pubsub hot-reload")
	}
}

func TestNamespaceReturnsSameObjectAndSingleSubscription(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "app", "flag", "true", "bool", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()

	appA := client.Namespace("app")
	appB := client.Namespace("app")
	if appA != appB {
		t.Fatalf("expected repeated Namespace call to return the same object")
	}
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}
	waitForReloads(t, client, 2)
	if _, err := client.AttachNamespace(context.Background(), "app"); err != nil {
		t.Fatalf("attach existing namespace: %v", err)
	}

	client.subscriptionMu.Lock()
	subscriptions := len(client.subscriptions)
	client.subscriptionMu.Unlock()
	if subscriptions != 1 {
		t.Fatalf("expected one subscription for repeated namespace, got %d", subscriptions)
	}
}

func TestFeatureToggleInitialLoadAndHotReload(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedFeature(t, addr, "payments", "new-checkout", false, 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	if payments.IsFeatureEnabled("new-checkout") {
		t.Fatalf("expected initial feature state to be false")
	}
	feature, ok := payments.GetFeature("new-checkout")
	if !ok {
		t.Fatalf("expected feature to be present")
	}
	if feature.Enabled || feature.Version != 1 {
		t.Fatalf("unexpected feature payload %+v", feature)
	}
	if _, ok := payments.GetFeature("missing-feature"); ok {
		t.Fatalf("expected missing feature to be absent")
	}

	seedFeature(t, addr, "payments", "new-checkout", true, 2)
	publishUpdateEvent(t, addr, "payments", "feature", []string{"new-checkout"}, "req-2")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if payments.IsFeatureEnabled("new-checkout") {
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
		WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	seedConfig(t, addr, "payments", "flag_b", "2", "int", 1)
	if err := payments.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, ok := payments.GetRaw("flag_b"); !ok {
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
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	payments := client.Namespace("payments")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	mini.Close()

	if err := payments.Flush(); err == nil {
		t.Fatalf("expected flush to fail when redis is unavailable")
	}

	value, ok := payments.GetString("flag")
	if !ok || value != "true" {
		t.Fatalf("expected last-known-good cache to be restored after failed flush value=%q ok=%v", value, ok)
	}
}

func TestAttachNamespaceAfterStartLoadsAndSubscribes(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()
	seedConfig(t, addr, "app", "title", "Remote Config", "string", 1)
	seedConfig(t, addr, "pricing", "discount.percent", "10", "int", 1)

	client, err := NewClient(
		WithRedisAddr(addr),
		WithLogger(slog.Default()),
		WithRetryInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = client.Close() }()
	client.Namespace("app")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	pricing, err := client.AttachNamespace(context.Background(), "pricing")
	if err != nil {
		t.Fatalf("attach pricing namespace: %v", err)
	}
	waitForReloads(t, client, 4)
	discount, ok := pricing.GetInt("discount.percent")
	if !ok || discount != 10 {
		t.Fatalf("unexpected attached pricing discount value=%d ok=%v", discount, ok)
	}

	callbackCh := make(chan Value, 1)
	pricing.Watch("discount.percent", func(item Value) {
		if item.Version >= 2 {
			callbackCh <- item
		}
	})
	seedConfig(t, addr, "pricing", "discount.percent", "15", "int", 2)
	publishUpdateEvent(t, addr, "pricing", "config", []string{"discount.percent"}, "req-attach")

	select {
	case item := <-callbackCh:
		if item.Raw != "15" || item.Namespace != "pricing" {
			t.Fatalf("unexpected attach hot-reload item %+v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for attached namespace hot-reload")
	}
}

func TestAttachNamespaceAfterStartReturnsReloadError(t *testing.T) {
	mini := miniredis.RunT(t)
	addr := mini.Addr()

	client, err := NewClient(
		WithRedisAddr(addr),
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

	if _, err := client.AttachNamespace(context.Background(), "pricing"); err == nil {
		t.Fatalf("expected attach namespace to return redis reload error")
	}
}

func waitForReloads(t *testing.T, client *Client, min int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.Stats().Reloads >= min {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d reloads, got %d", min, client.Stats().Reloads)
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

func publishUpdateEvent(t *testing.T, addr, namespace, resource string, keys []string, requestID string) {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	payload, err := json.Marshal(map[string]any{
		"namespace": namespace,
		"resource":  resource,
		"keys":      keys,
		"operation": "updated",
		"updatedAt": time.Now().UTC(),
		"updatedBy": "tester",
		"requestId": requestID,
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := rc.Publish(context.Background(), "events:"+namespace, payload).Err(); err != nil {
		t.Fatalf("publish event: %v", err)
	}
}

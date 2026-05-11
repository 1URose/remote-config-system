package sdk

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	appcache "github.com/1URose/remote-config-system/internal/cache"
	"github.com/1URose/remote-config-system/internal/domain"
	redisstorage "github.com/1URose/remote-config-system/internal/storage/redis"
)

type Client struct {
	redisClient    *goredis.Client
	storage        *redisstorage.ConfigStorage
	featureStorage *redisstorage.FeatureStorage
	pubsub         *redisstorage.PubSub
	cache          *appcache.Memory
	watchers       *watchRegistry
	logger         *slog.Logger
	namespace      string
	retryInterval  time.Duration
	closeCh        chan struct{}
	wg             sync.WaitGroup
	started        atomic.Bool
	closed         atomic.Bool
	redisConnected atomic.Bool
	reloads        atomic.Int64
	pointReloads   atomic.Int64
	flushes        atomic.Int64
	reconnects     atomic.Int64
}

type Stats struct {
	Reloads        int64
	PointReloads   int64
	Flushes        int64
	Reconnects     int64
	RedisConnected bool
	CacheSize      int
}

func NewClient(options ...Option) (*Client, error) {
	cfg := defaultOptions()
	for _, option := range options {
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.redisAddr == "" {
		return nil, ErrRedisAddrRequired
	}
	if cfg.namespace == "" {
		return nil, ErrNamespaceRequired
	}

	redisClient := redisstorage.NewClient(redisstorage.ClientConfig{
		Addr:     cfg.redisAddr,
		Password: cfg.redisPassword,
		DB:       cfg.redisDB,
	})

	return &Client{
		redisClient:    redisClient,
		storage:        redisstorage.NewConfigStorage(redisClient, 200),
		featureStorage: redisstorage.NewFeatureStorage(redisClient),
		pubsub:         redisstorage.NewPubSub(redisClient),
		cache:          appcache.New(),
		watchers:       newWatchRegistry(),
		logger:         cfg.logger,
		namespace:      cfg.namespace,
		retryInterval:  cfg.retryInterval,
		closeCh:        make(chan struct{}),
	}, nil
}

func (c *Client) Start(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	if c.closed.Load() {
		return ErrClientClosed
	}
	if !c.started.CompareAndSwap(false, true) {
		return ErrClientAlreadyStarted
	}
	if err := c.reloadWithContext(ctx); err != nil {
		c.started.Store(false)
		return err
	}

	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	c.wg.Add(1)
	go c.runSubscription()
	return nil
}

func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	close(c.closeCh)
	c.wg.Wait()
	return c.redisClient.Close()
}

func (c *Client) Get(key string) (Value, bool) {
	item, ok := c.cache.Get(c.namespace, key)
	if !ok {
		return Value{}, false
	}
	return newValue(item), true
}

func (c *Client) GetRaw(key string) (Value, bool) {
	return c.Get(key)
}

func (c *Client) GetString(key string) (string, bool) {
	value, ok := c.Get(key)
	if !ok {
		return "", false
	}
	return value.Raw, true
}

func (c *Client) GetBool(key string) (bool, bool) {
	value, ok := c.GetString(key)
	if !ok {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}

func (c *Client) GetInt(key string) (int, bool) {
	value, ok := c.GetString(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func (c *Client) Watch(key string, cb func(Value)) {
	c.watchers.addKeyWatch(c.namespace, key, cb)
}

func (c *Client) WatchNamespace(cb func([]Value)) {
	c.watchers.addNamespaceWatch(c.namespace, cb)
}

func (c *Client) Flush() error {
	c.flushes.Add(1)
	backup := c.cache.Snapshot(c.namespace)
	featureBackup := c.cache.SnapshotFeatures(c.namespace)
	c.cache.DeleteNamespace(c.namespace)
	if err := c.Reload(); err != nil {
		if backup != nil {
			c.cache.ReplaceNamespace(c.namespace, backup)
		}
		if featureBackup != nil {
			c.cache.ReplaceFeatures(c.namespace, featureBackup)
		}
		return err
	}
	return nil
}

func (c *Client) Reload() error {
	return c.reloadWithContext(context.Background())
}

func (c *Client) ReloadKeys(keys []string) error {
	return c.reloadKeysWithContext(context.Background(), keys)
}

func (c *Client) CacheSize() int {
	return c.cache.Size()
}

func (c *Client) RedisConnected() bool {
	return c.redisConnected.Load()
}

func (c *Client) Stats() Stats {
	return Stats{
		Reloads:        c.reloads.Load(),
		PointReloads:   c.pointReloads.Load(),
		Flushes:        c.flushes.Load(),
		Reconnects:     c.reconnects.Load(),
		RedisConnected: c.redisConnected.Load(),
		CacheSize:      c.cache.Size(),
	}
}

func (c *Client) runSubscription() {
	defer c.wg.Done()

	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())
		subscription := c.pubsub.Subscribe(ctx, c.namespace)
		channel := subscription.Channel()

		if err := c.Reload(); err != nil {
			c.logger.Warn("initial reload after subscribe failed", "namespace", c.namespace, "error", err)
		}

		subscriptionClosed := false
		for !subscriptionClosed {
			select {
			case <-c.closeCh:
				cancel()
				_ = subscription.Close()
				return
			case msg, ok := <-channel:
				if !ok {
					subscriptionClosed = true
					break
				}

				event, err := c.pubsub.ParseEvent(msg.Payload)
				if err != nil {
					c.logger.Warn("invalid update event", "namespace", c.namespace, "error", err)
					continue
				}

				resource := event.Resource
				if resource == "" {
					resource = "config"
				}

				switch event.Operation {
				case "flush":
					if err := c.Reload(); err != nil {
						c.logger.Warn("flush reload failed", "namespace", c.namespace, "error", err)
					}
				case "updated":
					if len(event.Keys) == 0 {
						continue
					}
					if err := c.reloadResourceKeys(resource, event.Keys); err != nil {
						c.logger.Warn("point reload failed", "namespace", c.namespace, "keys", event.Keys, "error", err)
					}
				case "deleted":
					c.deleteResourceKeys(resource, event.Keys)
				default:
					c.logger.Warn("unknown event operation", "namespace", c.namespace, "operation", event.Operation)
				}
			case <-time.After(c.retryInterval):
				if err := c.ping(); err != nil {
					subscriptionClosed = true
				}
			}
		}

		cancel()
		_ = subscription.Close()
		c.redisConnected.Store(false)
		c.logger.Warn("redis subscription lost, continuing with last-known-good cache", "namespace", c.namespace)

		select {
		case <-c.closeCh:
			return
		case <-time.After(c.retryInterval):
		}

		if err := c.awaitReconnect(); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Warn("reconnect loop interrupted", "namespace", c.namespace, "error", err)
		}
	}
}

func (c *Client) awaitReconnect() error {
	for {
		select {
		case <-c.closeCh:
			return context.Canceled
		case <-time.After(c.retryInterval):
			if err := c.ping(); err != nil {
				continue
			}
			c.redisConnected.Store(true)
			c.reconnects.Add(1)
			if err := c.Reload(); err != nil {
				c.logger.Warn("reload after reconnect failed", "namespace", c.namespace, "error", err)
				continue
			}
			c.logger.Info("redis connection restored", "namespace", c.namespace)
			return nil
		}
	}
}

func (c *Client) reloadWithContext(parent context.Context) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.storage.GetNamespace(ctx, c.namespace)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	features, err := c.featureStorage.GetNamespace(ctx, c.namespace)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.reloads.Add(1)
	c.cache.ReplaceNamespace(c.namespace, items)
	c.cache.ReplaceFeatures(c.namespace, features)
	c.watchers.notify(c.namespace, newValues(items))
	return nil
}

func (c *Client) reloadKeysWithContext(parent context.Context, keys []string) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.storage.GetKeys(ctx, c.namespace, keys)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.pointReloads.Add(1)
	changed := c.cache.UpdateKeys(c.namespace, items)
	c.watchers.notify(c.namespace, newValues(changed))
	return nil
}

func (c *Client) reloadFeatureKeysWithContext(parent context.Context, keys []string) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.featureStorage.GetKeys(ctx, c.namespace, keys)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.pointReloads.Add(1)
	c.cache.UpdateFeatures(c.namespace, items)
	return nil
}

func (c *Client) reloadResourceKeys(resource string, keys []string) error {
	switch resource {
	case "feature":
		return c.reloadFeatureKeysWithContext(context.Background(), keys)
	default:
		return c.ReloadKeys(keys)
	}
}

func (c *Client) deleteResourceKeys(resource string, keys []string) {
	switch resource {
	case "feature":
		c.cache.DeleteFeatures(c.namespace, keys)
	default:
		c.cache.DeleteKeys(c.namespace, keys)
	}
}

func (c *Client) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.storage.Ping(ctx)
}

func newValues(items []domain.ConfigItem) []Value {
	values := make([]Value, 0, len(items))
	for _, item := range items {
		values = append(values, newValue(item))
	}
	return values
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

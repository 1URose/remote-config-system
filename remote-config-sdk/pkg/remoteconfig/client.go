package remoteconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	redisclient "github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/client"
	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/cache"
	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/watcher"
)

type Options struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	Namespaces    []string
	Logger        *slog.Logger
	RetryInterval time.Duration
}

type Client struct {
	store          *redisclient.RedisStore
	cache          *cache.Store
	watchers       *watcher.Registry
	logger         *slog.Logger
	retryInterval  time.Duration
	namespaces     []string
	wg             sync.WaitGroup
	closeCh        chan struct{}
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

func New(ctx context.Context, opts Options) (*Client, error) {
	if len(opts.Namespaces) == 0 {
		return nil, fmt.Errorf("at least one namespace is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 2 * time.Second
	}

	rc := redis.NewClient(&redis.Options{
		Addr:     opts.RedisAddr,
		Password: opts.RedisPassword,
		DB:       opts.RedisDB,
	})
	client := &Client{
		store:         redisclient.NewRedisStore(rc),
		cache:         cache.NewStore(),
		watchers:      watcher.NewRegistry(),
		logger:        logger,
		retryInterval: opts.RetryInterval,
		namespaces:    append([]string(nil), opts.Namespaces...),
		closeCh:       make(chan struct{}),
	}

	for _, namespace := range opts.Namespaces {
		if err := client.Reload(namespace); err != nil {
			return nil, fmt.Errorf("initial namespace load %q: %w", namespace, err)
		}
	}
	client.redisConnected.Store(true)

	for _, namespace := range opts.Namespaces {
		client.wg.Add(1)
		go client.runSubscription(namespace)
	}

	return client, nil
}

func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	close(c.closeCh)
	c.wg.Wait()
	return c.store.Close()
}

func (c *Client) GetString(namespace, key string) (string, error) {
	item, err := c.cache.Get(namespace, key)
	if err != nil {
		return "", err
	}
	return item.Value, nil
}

func (c *Client) GetBool(namespace, key string) (bool, error) {
	value, err := c.GetString(namespace, key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(value)
}

func (c *Client) GetInt(namespace, key string) (int, error) {
	value, err := c.GetString(namespace, key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}

func (c *Client) GetRaw(namespace, key string) (model.ConfigItem, error) {
	return c.cache.Get(namespace, key)
}

func (c *Client) Watch(namespace, key string, cb func(model.ConfigItem)) {
	c.watchers.AddKeyWatch(namespace, key, cb)
}

func (c *Client) WatchNamespace(namespace string, cb func([]model.ConfigItem)) {
	c.watchers.AddNamespaceWatch(namespace, cb)
}

func (c *Client) Flush(namespace string) error {
	c.flushes.Add(1)
	backup := c.cache.Snapshot(namespace)
	c.cache.DeleteNamespace(namespace)
	if err := c.Reload(namespace); err != nil {
		if backup != nil {
			c.cache.ReplaceNamespace(namespace, backup)
		}
		return err
	}
	return nil
}

func (c *Client) Reload(namespace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items, err := c.store.LoadNamespace(ctx, namespace)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.reloads.Add(1)
	c.cache.ReplaceNamespace(namespace, items)
	c.watchers.NotifyKeys(namespace, items)
	return nil
}

func (c *Client) ReloadKeys(namespace string, keys []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items, err := c.store.LoadKeys(ctx, namespace, keys)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.pointReloads.Add(1)
	changed := c.cache.UpdateKeys(namespace, items)
	c.watchers.NotifyKeys(namespace, changed)
	return nil
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

func (c *Client) runSubscription(namespace string) {
	defer c.wg.Done()

	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())
		pubsub := c.store.Subscribe(ctx, namespace)
		channel := pubsub.Channel()

		if err := c.Reload(namespace); err != nil {
			c.logger.Warn("initial reload after subscribe failed", "namespace", namespace, "error", err)
		}

		subscriptionClosed := false
		for !subscriptionClosed {
			select {
			case <-c.closeCh:
				cancel()
				_ = pubsub.Close()
				return
			case msg, ok := <-channel:
				if !ok {
					subscriptionClosed = true
					break
				}
				event, err := c.store.ParseEvent(msg.Payload)
				if err != nil {
					c.logger.Warn("invalid update event", "namespace", namespace, "error", err)
					continue
				}
				switch event.Operation {
				case "flush":
					if err := c.Reload(namespace); err != nil {
						c.logger.Warn("flush reload failed", "namespace", namespace, "error", err)
					}
				case "updated":
					if len(event.Keys) == 0 {
						continue
					}
					if err := c.ReloadKeys(namespace, event.Keys); err != nil {
						c.logger.Warn("point reload failed", "namespace", namespace, "keys", event.Keys, "error", err)
					}
				default:
					c.logger.Warn("unknown event operation", "namespace", namespace, "operation", event.Operation)
				}
			case <-time.After(c.retryInterval):
				if err := c.ping(); err != nil {
					subscriptionClosed = true
				}
			}
		}

		cancel()
		_ = pubsub.Close()
		c.redisConnected.Store(false)
		c.logger.Warn("redis subscription lost, continuing with last-known-good cache", "namespace", namespace)

		select {
		case <-c.closeCh:
			return
		case <-time.After(c.retryInterval):
		}

		if err := c.awaitReconnect(namespace); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Warn("reconnect loop interrupted", "namespace", namespace, "error", err)
		}
	}
}

func (c *Client) awaitReconnect(namespace string) error {
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
			if err := c.Reload(namespace); err != nil {
				c.logger.Warn("reload after reconnect failed", "namespace", namespace, "error", err)
				continue
			}
			c.logger.Info("redis connection restored", "namespace", namespace)
			return nil
		}
	}
}

func (c *Client) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.store.Ping(ctx)
}

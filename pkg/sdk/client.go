package sdk

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
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
	retryInterval  time.Duration
	closeCh        chan struct{}
	wg             sync.WaitGroup
	namespacesMu   sync.RWMutex
	namespaces     map[string]*Namespace
	subscriptionMu sync.Mutex
	subscriptions  map[string]struct{}
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
		retryInterval:  cfg.retryInterval,
		closeCh:        make(chan struct{}),
		namespaces:     make(map[string]*Namespace),
		subscriptions:  make(map[string]struct{}),
	}, nil
}

func (c *Client) Namespace(name string) *Namespace {
	namespace := strings.TrimSpace(name)

	c.namespacesMu.Lock()
	defer c.namespacesMu.Unlock()

	if ns, ok := c.namespaces[namespace]; ok {
		return ns
	}
	ns := &Namespace{
		client:    c,
		namespace: namespace,
	}
	c.namespaces[namespace] = ns
	return ns
}

func (c *Client) AttachNamespace(ctx context.Context, name string) (*Namespace, error) {
	namespace := strings.TrimSpace(name)
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	if c.closed.Load() {
		return nil, ErrClientClosed
	}

	ns := c.Namespace(namespace)
	if err := c.reloadNamespaceWithContext(ctx, namespace); err != nil {
		return ns, err
	}
	if c.closed.Load() {
		return ns, ErrClientClosed
	}
	if c.started.Load() {
		c.startSubscription(namespace)
	}
	return ns, nil
}

func (c *Client) Start(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	if c.closed.Load() {
		return ErrClientClosed
	}
	if !c.started.CompareAndSwap(false, true) {
		return ErrClientAlreadyStarted
	}

	namespaces := c.namespaceNames()
	for _, namespace := range namespaces {
		if namespace == "" {
			c.started.Store(false)
			return ErrNamespaceRequired
		}
	}
	for _, namespace := range namespaces {
		if err := c.reloadNamespaceWithContext(ctx, namespace); err != nil {
			c.started.Store(false)
			return err
		}
	}

	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	for _, namespace := range namespaces {
		c.startSubscription(namespace)
	}
	return nil
}

func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.subscriptionMu.Lock()
	close(c.closeCh)
	c.subscriptionMu.Unlock()
	c.wg.Wait()
	return c.redisClient.Close()
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

func (c *Client) namespaceNames() []string {
	c.namespacesMu.RLock()
	defer c.namespacesMu.RUnlock()

	namespaces := make([]string, 0, len(c.namespaces))
	for namespace := range c.namespaces {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

func (c *Client) startSubscription(namespace string) {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	if c.closed.Load() {
		return
	}
	if _, ok := c.subscriptions[namespace]; ok {
		return
	}

	c.subscriptions[namespace] = struct{}{}
	c.wg.Add(1)
	go c.runSubscription(namespace)
}

func (c *Client) flushNamespace(namespace string) error {
	c.flushes.Add(1)
	backup := c.cache.Snapshot(namespace)
	featureBackup := c.cache.SnapshotFeatures(namespace)
	c.cache.DeleteNamespace(namespace)
	if err := c.reloadNamespaceWithContext(context.Background(), namespace); err != nil {
		if backup != nil {
			c.cache.ReplaceNamespace(namespace, backup)
		}
		if featureBackup != nil {
			c.cache.ReplaceFeatures(namespace, featureBackup)
		}
		return err
	}
	return nil
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
		subscription := c.pubsub.Subscribe(ctx, namespace)
		channel := subscription.Channel()

		if err := c.reloadNamespaceWithContext(context.Background(), namespace); err != nil {
			c.logger.Warn("initial reload after subscribe failed", "namespace", namespace, "error", err)
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
					c.logger.Warn("invalid update event", "namespace", namespace, "error", err)
					continue
				}

				eventNamespace := strings.TrimSpace(event.Namespace)
				if eventNamespace == "" {
					eventNamespace = namespace
				}
				if eventNamespace != namespace {
					c.logger.Warn("event namespace mismatch", "subscription_namespace", namespace, "event_namespace", eventNamespace)
					continue
				}

				resource := event.Resource
				if resource == "" {
					resource = "config"
				}

				switch event.Operation {
				case "flush":
					if err := c.reloadNamespaceWithContext(context.Background(), namespace); err != nil {
						c.logger.Warn("flush reload failed", "namespace", namespace, "error", err)
					}
				case "updated":
					if len(event.Keys) == 0 {
						continue
					}
					if err := c.reloadResourceKeys(namespace, resource, event.Keys); err != nil {
						c.logger.Warn("point reload failed", "namespace", namespace, "keys", event.Keys, "error", err)
					}
				case "deleted":
					c.deleteResourceKeys(namespace, resource, event.Keys)
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
		_ = subscription.Close()
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
			if err := c.reloadNamespaceWithContext(context.Background(), namespace); err != nil {
				c.logger.Warn("reload after reconnect failed", "namespace", namespace, "error", err)
				continue
			}
			c.logger.Info("redis connection restored", "namespace", namespace)
			return nil
		}
	}
}

func (c *Client) reloadNamespaceWithContext(parent context.Context, namespace string) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.storage.GetNamespace(ctx, namespace)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	features, err := c.featureStorage.GetNamespace(ctx, namespace)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.reloads.Add(1)
	c.cache.ReplaceNamespace(namespace, items)
	c.cache.ReplaceFeatures(namespace, features)
	c.watchers.notify(namespace, newValues(items))
	return nil
}

func (c *Client) reloadKeysWithContext(parent context.Context, namespace string, keys []string) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.storage.GetKeys(ctx, namespace, keys)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.pointReloads.Add(1)
	changed := c.cache.UpdateKeys(namespace, items)
	c.watchers.notify(namespace, newValues(changed))
	return nil
}

func (c *Client) reloadFeatureKeysWithContext(parent context.Context, namespace string, keys []string) error {
	parent = normalizeContext(parent)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	items, err := c.featureStorage.GetKeys(ctx, namespace, keys)
	if err != nil {
		c.redisConnected.Store(false)
		return err
	}
	c.redisConnected.Store(true)
	c.pointReloads.Add(1)
	c.cache.UpdateFeatures(namespace, items)
	return nil
}

func (c *Client) reloadResourceKeys(namespace, resource string, keys []string) error {
	switch resource {
	case "feature":
		return c.reloadFeatureKeysWithContext(context.Background(), namespace, keys)
	default:
		return c.reloadKeysWithContext(context.Background(), namespace, keys)
	}
}

func (c *Client) deleteResourceKeys(namespace, resource string, keys []string) {
	switch resource {
	case "feature":
		c.cache.DeleteFeatures(namespace, keys)
	default:
		c.cache.DeleteKeys(namespace, keys)
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

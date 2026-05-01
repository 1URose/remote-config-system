package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-sdk/pkg/model"
)

type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) LoadNamespace(ctx context.Context, namespace string) ([]model.ConfigItem, error) {
	keys, err := s.client.SMembers(ctx, fmt.Sprintf("cfgkeys:%s", namespace)).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return s.LoadKeys(ctx, namespace, keys)
}

func (s *RedisStore) LoadKeys(ctx context.Context, namespace string, keys []string) ([]model.ConfigItem, error) {
	if len(keys) == 0 {
		return []model.ConfigItem{}, nil
	}
	pipe := s.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, 0, len(keys))
	for _, key := range keys {
		cmds = append(cmds, pipe.HGetAll(ctx, fmt.Sprintf("cfg:%s:%s", namespace, key)))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	items := make([]model.ConfigItem, 0, len(cmds))
	for _, cmd := range cmds {
		hash, err := cmd.Result()
		if err != nil || len(hash) == 0 {
			continue
		}
		item, err := decodeItem(hash)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *RedisStore) Subscribe(ctx context.Context, namespace string) *redis.PubSub {
	return s.client.Subscribe(ctx, fmt.Sprintf("cfgupdates:%s", namespace))
}

func (s *RedisStore) ParseEvent(payload string) (model.ConfigUpdateEvent, error) {
	var event model.ConfigUpdateEvent
	err := json.Unmarshal([]byte(payload), &event)
	return event, err
}

func (s *RedisStore) Close() error {
	return s.client.Close()
}

func decodeItem(hash map[string]string) (model.ConfigItem, error) {
	version, err := strconv.ParseInt(hash["version"], 10, 64)
	if err != nil {
		return model.ConfigItem{}, fmt.Errorf("parse version: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, hash["updated_at"])
	if err != nil {
		return model.ConfigItem{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return model.ConfigItem{
		Namespace: hash["namespace"],
		Key:       hash["key"],
		Value:     hash["value"],
		Type:      hash["type"],
		Version:   version,
		IsSecret:  hash["is_secret"] == "1",
		UpdatedAt: updatedAt,
		UpdatedBy: hash["updated_by"],
	}, nil
}


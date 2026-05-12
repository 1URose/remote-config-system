package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/1URose/remote-config-system/internal/domain"
)

type FeatureStorage struct {
	client *goredis.Client
}

func NewFeatureStorage(client *goredis.Client) *FeatureStorage {
	return &FeatureStorage{client: client}
}

func (s *FeatureStorage) GetNamespace(ctx context.Context, namespace string) ([]domain.FeatureToggle, error) {
	keys, err := s.client.SMembers(ctx, featureSetKey(namespace)).Result()
	if err != nil {
		return nil, fmt.Errorf("get feature namespace %q: %w", namespace, err)
	}
	sort.Strings(keys)
	return s.GetKeys(ctx, namespace, keys)
}

func (s *FeatureStorage) GetKeys(ctx context.Context, namespace string, keys []string) ([]domain.FeatureToggle, error) {
	if len(keys) == 0 {
		return []domain.FeatureToggle{}, nil
	}

	pipe := s.client.Pipeline()
	cmds := make([]*goredis.MapStringStringCmd, 0, len(keys))
	for _, key := range keys {
		cmds = append(cmds, pipe.HGetAll(ctx, featureRedisKey(namespace, key)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("get feature keys for namespace %q: %w", namespace, err)
	}

	items := make([]domain.FeatureToggle, 0, len(keys))
	for _, cmd := range cmds {
		hash, err := cmd.Result()
		if err != nil || len(hash) == 0 {
			continue
		}
		item, err := decodeFeatureToggle(hash)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *FeatureStorage) GetKey(ctx context.Context, namespace, key string) (domain.FeatureToggle, error) {
	hash, err := s.client.HGetAll(ctx, featureRedisKey(namespace, key)).Result()
	if err != nil {
		return domain.FeatureToggle{}, fmt.Errorf("get feature key %q: %w", key, err)
	}
	if len(hash) == 0 {
		return domain.FeatureToggle{}, domain.ErrNotFound
	}
	return decodeFeatureToggle(hash)
}

func (s *FeatureStorage) Upsert(ctx context.Context, namespace, key string, enabled bool, updatedBy, requestID string) (item domain.FeatureToggle, err error) {
	lockToken := fmt.Sprintf("%s:%d", requestID, time.Now().UnixNano())
	locked, err := s.acquireFeatureWriteLock(ctx, namespace, key, lockToken)
	if err != nil {
		return domain.FeatureToggle{}, err
	}
	if !locked {
		return domain.FeatureToggle{}, &domain.ResourceLockedError{Resource: "feature", Namespace: namespace, Key: key}
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if releaseErr := s.releaseFeatureWriteLock(releaseCtx, namespace, key, lockToken); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	now := time.Now().UTC()
	result, err := s.client.Eval(ctx, upsertFeatureScript, []string{
		featureSetKey(namespace),
		updatesChannel(namespace),
	}, namespace, key, strconv.FormatBool(enabled), now.Format(time.RFC3339Nano), updatedBy, requestID).Result()
	if err != nil {
		return domain.FeatureToggle{}, fmt.Errorf("upsert feature key %q: %w", key, err)
	}

	raw, ok := result.([]any)
	if !ok || len(raw) != 2 {
		return domain.FeatureToggle{}, fmt.Errorf("unexpected feature upsert result %T", result)
	}

	version, err := toInt64(raw[1])
	if err != nil {
		return domain.FeatureToggle{}, err
	}
	return domain.FeatureToggle{
		Namespace: namespace,
		Key:       key,
		Enabled:   stringify(raw[0]) == "true",
		Version:   version,
		UpdatedAt: now,
		UpdatedBy: updatedBy,
	}, nil
}

func (s *FeatureStorage) acquireFeatureWriteLock(ctx context.Context, namespace, key, token string) (bool, error) {
	locked, err := s.client.SetNX(ctx, featureWriteLockKey(namespace, key), token, singleKeyWriteLockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("acquire feature write lock %q/%q: %w", namespace, key, err)
	}
	return locked, nil
}

func (s *FeatureStorage) releaseFeatureWriteLock(ctx context.Context, namespace, key, token string) error {
	if err := s.client.Eval(ctx, releaseLockScript, []string{featureWriteLockKey(namespace, key)}, token).Err(); err != nil {
		return fmt.Errorf("release feature write lock %q/%q: %w", namespace, key, err)
	}
	return nil
}

func (s *FeatureStorage) DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error {
	if _, err := s.GetKey(ctx, namespace, key); err != nil {
		return err
	}

	payload, err := json.Marshal(domain.ConfigUpdateEvent{
		Namespace: namespace,
		Resource:  "feature",
		Keys:      []string{key},
		Operation: "deleted",
		UpdatedAt: time.Now().UTC(),
		UpdatedBy: updatedBy,
		RequestID: requestID,
	})
	if err != nil {
		return fmt.Errorf("marshal feature delete event: %w", err)
	}

	pipe := s.client.TxPipeline()
	pipe.Del(ctx, featureRedisKey(namespace, key))
	pipe.SRem(ctx, featureSetKey(namespace), key)
	pipe.Publish(ctx, updatesChannel(namespace), payload)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete feature key %q: %w", key, err)
	}
	return nil
}

func decodeFeatureToggle(hash map[string]string) (domain.FeatureToggle, error) {
	version, err := strconv.ParseInt(hash["version"], 10, 64)
	if err != nil {
		return domain.FeatureToggle{}, fmt.Errorf("parse feature version: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, hash["updated_at"])
	if err != nil {
		return domain.FeatureToggle{}, fmt.Errorf("parse feature updated_at: %w", err)
	}
	enabled, err := strconv.ParseBool(hash["enabled"])
	if err != nil {
		return domain.FeatureToggle{}, fmt.Errorf("parse feature enabled: %w", err)
	}
	return domain.FeatureToggle{
		Namespace: hash["namespace"],
		Key:       hash["key"],
		Enabled:   enabled,
		Version:   version,
		UpdatedAt: updatedAt,
		UpdatedBy: hash["updated_by"],
	}, nil
}

const upsertFeatureScript = `
local namespace = ARGV[1]
local itemKey = ARGV[2]
local enabled = ARGV[3]
local updatedAt = ARGV[4]
local updatedBy = ARGV[5]
local requestID = ARGV[6]
local redisKey = 'feature:' .. namespace .. ':' .. itemKey
local featureSetType = redis.call('TYPE', KEYS[1])['ok']

if featureSetType ~= 'none' and featureSetType ~= 'set' then
	return { err = 'INVALID_FEATURE_SET_TYPE' }
end

local currentVersion = tonumber(redis.call('HGET', redisKey, 'version')) or 0

local nextVersion = tostring(currentVersion + 1)
redis.call('HSET', redisKey,
	'namespace', namespace,
	'key', itemKey,
	'enabled', enabled,
	'version', nextVersion,
	'updated_at', updatedAt,
	'updated_by', updatedBy
)
redis.call('SADD', KEYS[1], itemKey)
redis.call('PUBLISH', KEYS[2], cjson.encode({
	namespace = namespace,
	resource = 'feature',
	keys = { itemKey },
	operation = 'updated',
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
}))

return { enabled, nextVersion }
`

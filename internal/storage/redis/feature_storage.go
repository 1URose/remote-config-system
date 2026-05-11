package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

func (s *FeatureStorage) Upsert(ctx context.Context, namespace, key string, enabled bool, expectedVersion int64, updatedBy, requestID string) (domain.FeatureToggle, error) {
	now := time.Now().UTC()
	result, err := s.client.Eval(ctx, upsertFeatureScript, []string{
		featureSetKey(namespace),
		updatesChannel(namespace),
	}, namespace, key, strconv.FormatBool(enabled), expectedVersion, now.Format(time.RFC3339Nano), updatedBy, requestID).Result()
	if err != nil {
		if conflict := parseSingleVersionConflict(err, key, expectedVersion); conflict != nil {
			return domain.FeatureToggle{}, conflict
		}
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

func parseSingleVersionConflict(err error, key string, expectedVersion int64) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if !strings.HasPrefix(message, "VERSION_CONFLICT:") {
		return nil
	}
	parts := strings.Split(message, ":")
	if len(parts) != 3 {
		return err
	}
	currentVersion, parseErr := strconv.ParseInt(parts[2], 10, 64)
	if parseErr != nil {
		return err
	}
	return &domain.VersionConflictError{
		Key:             key,
		CurrentVersion:  currentVersion,
		ExpectedVersion: expectedVersion,
	}
}

const upsertFeatureScript = `
local namespace = ARGV[1]
local itemKey = ARGV[2]
local enabled = ARGV[3]
local expectedVersion = tonumber(ARGV[4])
local updatedAt = ARGV[5]
local updatedBy = ARGV[6]
local requestID = ARGV[7]
local redisKey = 'feature:' .. namespace .. ':' .. itemKey
local currentVersion = tonumber(redis.call('HGET', redisKey, 'version')) or 0

if expectedVersion ~= currentVersion then
	return { err = 'VERSION_CONFLICT:' .. itemKey .. ':' .. tostring(currentVersion) }
end

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

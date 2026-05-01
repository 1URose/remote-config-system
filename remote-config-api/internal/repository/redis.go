package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ya.ermakov/remote-config-system/remote-config-api/internal/model"
)

var ErrNotFound = errors.New("config item not found")

type VersionConflictError struct {
	Key             string
	ExpectedVersion int64
	CurrentVersion  int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict for key %q: expected=%d current=%d", e.Key, e.ExpectedVersion, e.CurrentVersion)
}

type RedisRepository struct {
	client     *redis.Client
	auditLimit int64
}

func NewRedisRepository(client *redis.Client, auditLimit int64) *RedisRepository {
	return &RedisRepository{
		client:     client,
		auditLimit: auditLimit,
	}
}

func (r *RedisRepository) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisRepository) GetNamespace(ctx context.Context, namespace string) ([]model.ConfigItem, error) {
	keys, err := r.client.SMembers(ctx, namespaceSetKey(namespace)).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return r.GetKeys(ctx, namespace, keys)
}

func (r *RedisRepository) GetKey(ctx context.Context, namespace, key string) (model.ConfigItem, error) {
	hash, err := r.client.HGetAll(ctx, configRedisKey(namespace, key)).Result()
	if err != nil {
		return model.ConfigItem{}, err
	}
	if len(hash) == 0 {
		return model.ConfigItem{}, ErrNotFound
	}
	return decodeConfigItem(hash)
}

func (r *RedisRepository) GetKeys(ctx context.Context, namespace string, keys []string) ([]model.ConfigItem, error) {
	if len(keys) == 0 {
		return []model.ConfigItem{}, nil
	}
	pipe := r.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, 0, len(keys))
	for _, key := range keys {
		cmds = append(cmds, pipe.HGetAll(ctx, configRedisKey(namespace, key)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}

	items := make([]model.ConfigItem, 0, len(keys))
	for _, cmd := range cmds {
		hash, err := cmd.Result()
		if err != nil || len(hash) == 0 {
			continue
		}
		item, err := decodeConfigItem(hash)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (r *RedisRepository) Update(ctx context.Context, req model.ConfigUpdateRequest, requestID string) ([]model.ConfigItem, error) {
	args := make([]any, 0, 7+len(req.Entries)*5)
	now := time.Now().UTC()
	nowRaw := now.Format(time.RFC3339Nano)
	dryRun := "0"
	if req.DryRun {
		dryRun = "1"
	}

	args = append(args, req.Namespace, nowRaw, req.UpdatedBy, requestID, dryRun, len(req.Entries), r.auditLimit)
	for _, entry := range req.Entries {
		isSecret := "0"
		if entry.IsSecret {
			isSecret = "1"
		}
		args = append(args, entry.Key, entry.Value, entry.Type, entry.ExpectedVersion, isSecret)
	}

	result, err := r.client.Eval(ctx, updateScript, []string{
		namespaceSetKey(req.Namespace),
		auditListKey(req.Namespace),
		updatesChannel(req.Namespace),
	}, args...).Result()
	if err != nil {
		if conflict := parseVersionConflict(err, req.Entries); conflict != nil {
			return nil, conflict
		}
		return nil, err
	}

	raw, ok := result.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected update script result %T", result)
	}

	items := make([]model.ConfigItem, 0, len(req.Entries))
	for i := 0; i < len(raw); i += 5 {
		version, err := toInt64(raw[i+3])
		if err != nil {
			return nil, err
		}
		isSecret := false
		if s := stringify(raw[i+4]); s == "1" {
			isSecret = true
		}
		item := model.ConfigItem{
			Namespace: req.Namespace,
			Key:       stringify(raw[i]),
			Value:     stringify(raw[i+1]),
			Type:      stringify(raw[i+2]),
			Version:   version,
			IsSecret:  isSecret,
			UpdatedAt: now,
			UpdatedBy: req.UpdatedBy,
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *RedisRepository) PublishFlush(ctx context.Context, namespace, updatedBy, requestID string) error {
	event := model.ConfigUpdateEvent{
		Namespace: namespace,
		Keys:      nil,
		Operation: "flush",
		UpdatedAt: time.Now().UTC(),
		UpdatedBy: updatedBy,
		RequestID: requestID,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, updatesChannel(namespace), payload).Err()
}

func (r *RedisRepository) GetAudit(ctx context.Context, namespace string) ([]model.AuditRecord, error) {
	raw, err := r.client.LRange(ctx, auditListKey(namespace), 0, r.auditLimit-1).Result()
	if err != nil {
		return nil, err
	}
	records := make([]model.AuditRecord, 0, len(raw))
	for _, row := range raw {
		var record model.AuditRecord
		if err := json.Unmarshal([]byte(row), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func configRedisKey(namespace, key string) string {
	return fmt.Sprintf("cfg:%s:%s", namespace, key)
}

func namespaceSetKey(namespace string) string {
	return fmt.Sprintf("cfgkeys:%s", namespace)
}

func auditListKey(namespace string) string {
	return fmt.Sprintf("cfgaudit:%s", namespace)
}

func updatesChannel(namespace string) string {
	return fmt.Sprintf("cfgupdates:%s", namespace)
}

func decodeConfigItem(hash map[string]string) (model.ConfigItem, error) {
	version, err := strconv.ParseInt(hash["version"], 10, 64)
	if err != nil {
		return model.ConfigItem{}, fmt.Errorf("parse version: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, hash["updated_at"])
	if err != nil {
		return model.ConfigItem{}, fmt.Errorf("parse updated_at: %w", err)
	}
	isSecret := hash["is_secret"] == "1"
	return model.ConfigItem{
		Namespace: hash["namespace"],
		Key:       hash["key"],
		Value:     hash["value"],
		Type:      hash["type"],
		Version:   version,
		IsSecret:  isSecret,
		UpdatedAt: updatedAt,
		UpdatedBy: hash["updated_by"],
	}, nil
}

func parseVersionConflict(err error, entries []model.ConfigUpdateEntry) error {
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
	expectedVersion := int64(-1)
	for _, entry := range entries {
		if entry.Key == parts[1] {
			expectedVersion = entry.ExpectedVersion
			break
		}
	}
	return &VersionConflictError{
		Key:             parts[1],
		CurrentVersion:  currentVersion,
		ExpectedVersion: expectedVersion,
	}
}

func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected int64 type %T", value)
	}
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

const updateScript = `
local namespace = ARGV[1]
local updatedAt = ARGV[2]
local updatedBy = ARGV[3]
local requestID = ARGV[4]
local dryRun = ARGV[5]
local count = tonumber(ARGV[6])
local auditLimit = tonumber(ARGV[7])
local pending = {}
local index = 8

for i = 1, count do
	local itemKey = ARGV[index]
	local itemValue = ARGV[index + 1]
	local itemType = ARGV[index + 2]
	local expectedVersion = tonumber(ARGV[index + 3])
	local isSecret = ARGV[index + 4]
	local redisKey = 'cfg:' .. namespace .. ':' .. itemKey
	local current = redis.call('HMGET', redisKey, 'value', 'type', 'version', 'is_secret')
	local oldValue = current[1] or ''
	local currentVersion = tonumber(current[3]) or 0
	local oldIsSecret = current[4] or '0'
	if expectedVersion ~= currentVersion then
		return { err = 'VERSION_CONFLICT:' .. itemKey .. ':' .. tostring(currentVersion) }
	end
	pending[i] = {
		key = itemKey,
		value = itemValue,
		type = itemType,
		version = tostring(currentVersion + 1),
		isSecret = isSecret,
		oldValue = oldValue,
		oldIsSecret = oldIsSecret,
	}
	index = index + 5
end

if dryRun ~= '1' then
	local changedKeys = {}
	for i = 1, count do
		local item = pending[i]
		local redisKey = 'cfg:' .. namespace .. ':' .. item.key
		redis.call('HSET', redisKey,
			'namespace', namespace,
			'key', item.key,
			'value', item.value,
			'type', item.type,
			'version', item.version,
			'is_secret', item.isSecret,
			'updated_at', updatedAt,
			'updated_by', updatedBy
		)
		redis.call('SADD', KEYS[1], item.key)
		changedKeys[i] = item.key
		local oldAuditValue = item.oldValue
		local newAuditValue = item.value
		if item.oldIsSecret == '1' and item.oldValue ~= '' then
			oldAuditValue = '****'
		end
		if item.isSecret == '1' and item.value ~= '' then
			newAuditValue = '****'
		end
		local auditPayload = cjson.encode({
			namespace = namespace,
			key = item.key,
			oldValue = oldAuditValue,
			newValue = newAuditValue,
			type = item.type,
			version = tonumber(item.version),
			isSecret = item.isSecret == '1',
			updatedAt = updatedAt,
			updatedBy = updatedBy,
			requestId = requestID,
			result = 'updated',
		})
		redis.call('LPUSH', KEYS[2], auditPayload)
	end
	redis.call('LTRIM', KEYS[2], 0, auditLimit - 1)
	local eventPayload = cjson.encode({
		namespace = namespace,
		keys = changedKeys,
		operation = 'updated',
		updatedAt = updatedAt,
		updatedBy = updatedBy,
		requestId = requestID,
	})
	redis.call('PUBLISH', KEYS[3], eventPayload)
end

local response = {}
for i = 1, count do
	local item = pending[i]
	table.insert(response, item.key)
	table.insert(response, item.value)
	table.insert(response, item.type)
	table.insert(response, item.version)
	table.insert(response, item.isSecret)
end
return response
`

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

type ConfigStorage struct {
	client     *goredis.Client
	auditLimit int64
}

const namespaceBulkLockTTL = 30 * time.Second
const singleKeyWriteLockTTL = 30 * time.Second

func NewConfigStorage(client *goredis.Client, auditLimit int64) *ConfigStorage {
	return &ConfigStorage{
		client:     client,
		auditLimit: auditLimit,
	}
}

func (s *ConfigStorage) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *ConfigStorage) ListNamespaces(ctx context.Context) ([]string, error) {
	keys, err := s.client.Keys(ctx, namespaceSetKey("*")).Result()
	if err != nil {
		return nil, fmt.Errorf("list config namespaces: %w", err)
	}

	namespaces := make([]string, 0, len(keys))
	for _, key := range keys {
		namespace, ok := strings.CutPrefix(key, "config_keys:")
		if !ok || namespace == "" {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

func (s *ConfigStorage) GetNamespace(ctx context.Context, namespace string) ([]domain.ConfigItem, error) {
	keys, err := s.client.SMembers(ctx, namespaceSetKey(namespace)).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return s.GetKeys(ctx, namespace, keys)
}

func (s *ConfigStorage) GetKey(ctx context.Context, namespace, key string) (domain.ConfigItem, error) {
	hash, err := s.client.HGetAll(ctx, configRedisKey(namespace, key)).Result()
	if err != nil {
		return domain.ConfigItem{}, fmt.Errorf("get config key %q: %w", key, err)
	}
	if len(hash) == 0 {
		return domain.ConfigItem{}, domain.ErrNotFound
	}
	return decodeConfigItem(hash)
}

func (s *ConfigStorage) GetKeys(ctx context.Context, namespace string, keys []string) ([]domain.ConfigItem, error) {
	if len(keys) == 0 {
		return []domain.ConfigItem{}, nil
	}
	pipe := s.client.Pipeline()
	cmds := make([]*goredis.MapStringStringCmd, 0, len(keys))
	for _, key := range keys {
		cmds = append(cmds, pipe.HGetAll(ctx, configRedisKey(namespace, key)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, err
	}

	items := make([]domain.ConfigItem, 0, len(keys))
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

func (s *ConfigStorage) UpsertKey(ctx context.Context, namespace, key, value, kind string, isSecret bool, updatedBy, requestID string) (item domain.ConfigItem, err error) {
	bulkLocked, err := s.isNamespaceBulkLocked(ctx, namespace)
	if err != nil {
		return domain.ConfigItem{}, err
	}
	if bulkLocked {
		return domain.ConfigItem{}, &domain.NamespaceLockedError{Namespace: namespace}
	}

	lockToken := fmt.Sprintf("%s:%d", requestID, time.Now().UnixNano())
	locked, err := s.acquireConfigWriteLock(ctx, namespace, key, lockToken)
	if err != nil {
		return domain.ConfigItem{}, err
	}
	if !locked {
		return domain.ConfigItem{}, &domain.ResourceLockedError{Resource: "config", Namespace: namespace, Key: key}
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if releaseErr := s.releaseConfigWriteLock(releaseCtx, namespace, key, lockToken); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	now := time.Now().UTC()
	isSecretRaw := "0"
	if isSecret {
		isSecretRaw = "1"
	}

	result, err := s.client.Eval(ctx, upsertConfigKeyScript, []string{
		namespaceSetKey(namespace),
		auditListKey(namespace),
		updatesChannel(namespace),
		bulkLockKey(namespace),
	}, namespace, key, value, kind, isSecretRaw, now.Format(time.RFC3339Nano), updatedBy, requestID, s.auditLimit).Result()
	if err != nil {
		if isNamespaceLockedScriptError(err) {
			return domain.ConfigItem{}, &domain.NamespaceLockedError{Namespace: namespace}
		}
		return domain.ConfigItem{}, fmt.Errorf("upsert config key %q: %w", key, err)
	}

	raw, ok := result.([]any)
	if !ok || len(raw) != 5 {
		return domain.ConfigItem{}, fmt.Errorf("unexpected config upsert result %T", result)
	}

	version, err := toInt64(raw[3])
	if err != nil {
		return domain.ConfigItem{}, err
	}
	return domain.ConfigItem{
		Namespace: namespace,
		Key:       stringify(raw[0]),
		Value:     stringify(raw[1]),
		Type:      stringify(raw[2]),
		Version:   version,
		IsSecret:  stringify(raw[4]) == "1",
		UpdatedAt: now,
		UpdatedBy: updatedBy,
	}, nil
}

func (s *ConfigStorage) Update(ctx context.Context, req domain.ConfigUpdateRequest, requestID string) (items []domain.ConfigItem, err error) {
	lockToken := fmt.Sprintf("%s:%d", requestID, time.Now().UnixNano())
	locked, err := s.acquireNamespaceBulkLock(ctx, req.Namespace, lockToken)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, &domain.NamespaceLockedError{Namespace: req.Namespace}
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if releaseErr := s.releaseNamespaceBulkLock(releaseCtx, req.Namespace, lockToken); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	args := make([]any, 0, 7+len(req.Entries)*4)
	now := time.Now().UTC()
	nowRaw := now.Format(time.RFC3339Nano)
	dryRun := "0"
	if req.DryRun {
		dryRun = "1"
	}

	args = append(args, req.Namespace, nowRaw, req.UpdatedBy, requestID, dryRun, len(req.Entries), s.auditLimit)
	for _, entry := range req.Entries {
		isSecret := "0"
		if entry.IsSecret {
			isSecret = "1"
		}
		args = append(args, entry.Key, entry.Value, entry.Type, isSecret)
	}

	result, err := s.client.Eval(ctx, updateScript, []string{
		namespaceSetKey(req.Namespace),
		auditListKey(req.Namespace),
		updatesChannel(req.Namespace),
	}, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("update config namespace %q: %w", req.Namespace, err)
	}

	raw, ok := result.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected update script result %T", result)
	}

	items = make([]domain.ConfigItem, 0, len(req.Entries))
	for i := 0; i < len(raw); i += 5 {
		version, err := toInt64(raw[i+3])
		if err != nil {
			return nil, err
		}
		items = append(items, domain.ConfigItem{
			Namespace: req.Namespace,
			Key:       stringify(raw[i]),
			Value:     stringify(raw[i+1]),
			Type:      stringify(raw[i+2]),
			Version:   version,
			IsSecret:  stringify(raw[i+4]) == "1",
			UpdatedAt: now,
			UpdatedBy: req.UpdatedBy,
		})
	}
	return items, nil
}

func (s *ConfigStorage) acquireNamespaceBulkLock(ctx context.Context, namespace, token string) (bool, error) {
	locked, err := s.client.SetNX(ctx, bulkLockKey(namespace), token, namespaceBulkLockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("acquire namespace bulk lock %q: %w", namespace, err)
	}
	return locked, nil
}

func (s *ConfigStorage) isNamespaceBulkLocked(ctx context.Context, namespace string) (bool, error) {
	exists, err := s.client.Exists(ctx, bulkLockKey(namespace)).Result()
	if err != nil {
		return false, fmt.Errorf("check namespace bulk lock %q: %w", namespace, err)
	}
	return exists > 0, nil
}

func (s *ConfigStorage) releaseNamespaceBulkLock(ctx context.Context, namespace, token string) error {
	if err := s.client.Eval(ctx, releaseLockScript, []string{bulkLockKey(namespace)}, token).Err(); err != nil {
		return fmt.Errorf("release namespace bulk lock %q: %w", namespace, err)
	}
	return nil
}

func (s *ConfigStorage) acquireConfigWriteLock(ctx context.Context, namespace, key, token string) (bool, error) {
	locked, err := s.client.SetNX(ctx, configWriteLockKey(namespace, key), token, singleKeyWriteLockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("acquire config write lock %q/%q: %w", namespace, key, err)
	}
	return locked, nil
}

func (s *ConfigStorage) releaseConfigWriteLock(ctx context.Context, namespace, key, token string) error {
	if err := s.client.Eval(ctx, releaseLockScript, []string{configWriteLockKey(namespace, key)}, token).Err(); err != nil {
		return fmt.Errorf("release config write lock %q/%q: %w", namespace, key, err)
	}
	return nil
}

func isNamespaceLockedScriptError(err error) bool {
	return strings.Contains(err.Error(), "NAMESPACE_LOCKED")
}

func (s *ConfigStorage) DeleteKey(ctx context.Context, namespace, key, updatedBy, requestID string) error {
	item, err := s.GetKey(ctx, namespace, key)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	auditPayload, err := json.Marshal(domain.AuditRecord{
		Namespace: namespace,
		Key:       key,
		OldValue:  maskAuditValue(item.Value, item.IsSecret),
		NewValue:  "",
		Type:      item.Type,
		Version:   item.Version,
		IsSecret:  item.IsSecret,
		UpdatedAt: now,
		UpdatedBy: updatedBy,
		RequestID: requestID,
		Result:    "deleted",
	})
	if err != nil {
		return fmt.Errorf("marshal delete audit payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.ConfigUpdateEvent{
		Namespace: namespace,
		Resource:  "config",
		Keys:      []string{key},
		Operation: "deleted",
		UpdatedAt: now,
		UpdatedBy: updatedBy,
		RequestID: requestID,
	})
	if err != nil {
		return fmt.Errorf("marshal delete event payload: %w", err)
	}

	pipe := s.client.TxPipeline()
	pipe.Del(ctx, configRedisKey(namespace, key))
	pipe.SRem(ctx, namespaceSetKey(namespace), key)
	pipe.LPush(ctx, auditListKey(namespace), auditPayload)
	pipe.LTrim(ctx, auditListKey(namespace), 0, s.auditLimit-1)
	pipe.Publish(ctx, updatesChannel(namespace), eventPayload)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete config key %q: %w", key, err)
	}
	return nil
}

func (s *ConfigStorage) GetAudit(ctx context.Context, namespace string) ([]domain.AuditRecord, error) {
	raw, err := s.client.LRange(ctx, auditListKey(namespace), 0, s.auditLimit-1).Result()
	if err != nil {
		return nil, err
	}
	records := make([]domain.AuditRecord, 0, len(raw))
	for _, row := range raw {
		var record domain.AuditRecord
		if err := json.Unmarshal([]byte(row), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func decodeConfigItem(hash map[string]string) (domain.ConfigItem, error) {
	version, err := strconv.ParseInt(hash["version"], 10, 64)
	if err != nil {
		return domain.ConfigItem{}, fmt.Errorf("parse version: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, hash["updated_at"])
	if err != nil {
		return domain.ConfigItem{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return domain.ConfigItem{
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

func maskAuditValue(value string, secret bool) string {
	if secret && value != "" {
		return "****"
	}
	return value
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
local namespaceSetType = redis.call('TYPE', KEYS[1])['ok']
local auditListType = redis.call('TYPE', KEYS[2])['ok']

if namespaceSetType ~= 'none' and namespaceSetType ~= 'set' then
	return { err = 'INVALID_NAMESPACE_SET_TYPE' }
end
if auditListType ~= 'none' and auditListType ~= 'list' then
	return { err = 'INVALID_AUDIT_LIST_TYPE' }
end

for i = 1, count do
	local itemKey = ARGV[index]
	local itemValue = ARGV[index + 1]
	local itemType = ARGV[index + 2]
	local isSecret = ARGV[index + 3]
	local redisKey = 'config:' .. namespace .. ':' .. itemKey
	local current = redis.call('HMGET', redisKey, 'value', 'type', 'version', 'is_secret')
	local oldValue = current[1] or ''
	local currentVersion = tonumber(current[3]) or 0
	local oldIsSecret = current[4] or '0'
	pending[i] = {
		key = itemKey,
		value = itemValue,
		type = itemType,
		version = tostring(currentVersion + 1),
		isSecret = isSecret,
		oldValue = oldValue,
		oldIsSecret = oldIsSecret,
	}
	index = index + 4
end

if dryRun ~= '1' then
	local changedKeys = {}
	for i = 1, count do
		local item = pending[i]
		local redisKey = 'config:' .. namespace .. ':' .. item.key
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
		resource = 'config',
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

const upsertConfigKeyScript = `
local namespace = ARGV[1]
local itemKey = ARGV[2]
local itemValue = ARGV[3]
local itemType = ARGV[4]
local isSecret = ARGV[5]
local updatedAt = ARGV[6]
local updatedBy = ARGV[7]
local requestID = ARGV[8]
local auditLimit = tonumber(ARGV[9])
local redisKey = 'config:' .. namespace .. ':' .. itemKey
local namespaceSetType = redis.call('TYPE', KEYS[1])['ok']
local auditListType = redis.call('TYPE', KEYS[2])['ok']

if namespaceSetType ~= 'none' and namespaceSetType ~= 'set' then
	return { err = 'INVALID_NAMESPACE_SET_TYPE' }
end
if auditListType ~= 'none' and auditListType ~= 'list' then
	return { err = 'INVALID_AUDIT_LIST_TYPE' }
end
if redis.call('EXISTS', KEYS[4]) == 1 then
	return { err = 'NAMESPACE_LOCKED' }
end

local current = redis.call('HMGET', redisKey, 'value', 'type', 'version', 'is_secret')
local oldValue = current[1] or ''
local currentVersion = tonumber(current[3]) or 0
local oldIsSecret = current[4] or '0'
local nextVersion = tostring(currentVersion + 1)

redis.call('HSET', redisKey,
	'namespace', namespace,
	'key', itemKey,
	'value', itemValue,
	'type', itemType,
	'version', nextVersion,
	'is_secret', isSecret,
	'updated_at', updatedAt,
	'updated_by', updatedBy
)
redis.call('SADD', KEYS[1], itemKey)

local oldAuditValue = oldValue
local newAuditValue = itemValue
if oldIsSecret == '1' and oldValue ~= '' then
	oldAuditValue = '****'
end
if isSecret == '1' and itemValue ~= '' then
	newAuditValue = '****'
end
redis.call('LPUSH', KEYS[2], cjson.encode({
	namespace = namespace,
	key = itemKey,
	oldValue = oldAuditValue,
	newValue = newAuditValue,
	type = itemType,
	version = tonumber(nextVersion),
	isSecret = isSecret == '1',
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
	result = 'updated',
}))
redis.call('LTRIM', KEYS[2], 0, auditLimit - 1)

redis.call('PUBLISH', KEYS[3], cjson.encode({
	namespace = namespace,
	resource = 'config',
	keys = { itemKey },
	operation = 'updated',
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
}))

return { itemKey, itemValue, itemType, nextVersion, isSecret }
`

const releaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`

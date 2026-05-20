local namespace = ARGV[1]
local itemKey = ARGV[2]
local enabled = ARGV[3]
local updatedAt = ARGV[4]
local updatedBy = ARGV[5]
local requestID = ARGV[6]
local auditLimit = tonumber(ARGV[7])
local redisKey = 'feature:' .. namespace .. ':' .. itemKey
local featureSetType = redis.call('TYPE', KEYS[1])['ok']
local auditListType = redis.call('TYPE', KEYS[2])['ok']

if featureSetType ~= 'none' and featureSetType ~= 'set' then
	return { err = 'INVALID_FEATURE_SET_TYPE' }
end
if auditListType ~= 'none' and auditListType ~= 'list' then
	return { err = 'INVALID_AUDIT_LIST_TYPE' }
end

local current = redis.call('HMGET', redisKey, 'enabled', 'version')
local oldValue = current[1] or ''
local currentVersion = tonumber(current[2]) or 0

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
redis.call('LPUSH', KEYS[2], cjson.encode({
	namespace = namespace,
	key = itemKey,
	oldValue = oldValue,
	newValue = enabled,
	type = 'feature',
	version = tonumber(nextVersion),
	isSecret = false,
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
	result = 'updated',
}))
redis.call('LTRIM', KEYS[2], 0, auditLimit - 1)
redis.call('PUBLISH', KEYS[3], cjson.encode({
	namespace = namespace,
	resource = 'feature',
	keys = { itemKey },
	operation = 'updated',
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
}))

return { enabled, nextVersion }

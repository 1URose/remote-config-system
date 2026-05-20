local namespace = ARGV[1]
local itemKey = ARGV[2]
local updatedAt = ARGV[3]
local updatedBy = ARGV[4]
local requestID = ARGV[5]
local auditLimit = tonumber(ARGV[6])
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
if current[1] == false then
	return { err = 'FEATURE_NOT_FOUND' }
end
local oldValue = current[1]
local version = tonumber(current[2]) or 0

redis.call('LPUSH', KEYS[2], cjson.encode({
	namespace = namespace,
	key = itemKey,
	oldValue = oldValue,
	newValue = '',
	type = 'feature',
	version = version,
	isSecret = false,
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
	result = 'deleted',
}))
redis.call('LTRIM', KEYS[2], 0, auditLimit - 1)
redis.call('DEL', redisKey)
redis.call('SREM', KEYS[1], itemKey)
redis.call('PUBLISH', KEYS[3], cjson.encode({
	namespace = namespace,
	resource = 'feature',
	keys = { itemKey },
	operation = 'deleted',
	updatedAt = updatedAt,
	updatedBy = updatedBy,
	requestId = requestID,
}))

return 1

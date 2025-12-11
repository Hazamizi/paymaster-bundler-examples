-- KEYS[1]: The available keys list (FIFO queue) - e.g., "keypool:available_keys:list"
-- KEYS[2]: The reserved keys list - e.g., "keypool:reserved_keys:list"
-- KEYS[3]: The key data hash - e.g., "keypool:key_data:hash"
-- ARGV[1]: The key number to release

local available_keys_list = KEYS[1]
local reserved_keys_list = KEYS[2]
local key_data_hash = KEYS[3]
local key = ARGV[1]
local status = ARGV[2]
-- Check if the key exists in the reserved list
local count = redis.call('LREM', reserved_keys_list, 1, key)
if count == 0 then
  return 0  -- Key was not in the reserved list
end

-- Add the key back to the available list
redis.call('RPUSH', available_keys_list, key)

-- Add the status to the key data if provided
if status ~= "" then
  local hash_key = key_data_hash .. ":" .. key
  redis.call('HSET', hash_key, "status", status)
end

return 1
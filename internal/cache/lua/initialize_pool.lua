-- KEYS[1]: The available keys list (FIFO queue) - e.g., "keypool:available_keys:list"
-- KEYS[2]: The reserved keys list - e.g., "keypool:reserved_keys:list"
-- ARGV[1]: The target total size of the key pool

local available_keys_list = KEYS[1]
local reserved_keys_list = KEYS[2]
local target_size = tonumber(ARGV[1])

-- Validate input: target_size must be a non-negative number
if target_size == nil or target_size <= 0 then
  return redis.error_reply("ERR invalid target_size")
end

-- Get the current counts
local available_count = redis.call('LLEN', available_keys_list)
local reserved_count = redis.call('LLEN', reserved_keys_list)
local total_keys = available_count + reserved_count

-- Calculate how many new keys we need to add
local keys_to_add = 0
if total_keys < target_size then
  keys_to_add = target_size - total_keys
end

-- If we need to add keys
if keys_to_add > 0 then
  -- Add keys to the available list
  for i = 1, keys_to_add do
    local new_key = total_keys + i - 1
    redis.call('RPUSH', available_keys_list, tostring(new_key))
  end
end

return keys_to_add
-- KEYS[1]: The available keys list (FIFO queue) - e.g., "keypool:available_keys:list"
-- KEYS[2]: The reserved keys list - e.g., "keypool:reserved_keys:list"
-- KEYS[3]: The key data hash - e.g., "keypool:key_data:hash"
-- ARGV[1]: The initial timestamp (unix timestamp)

local available_keys_list = KEYS[1]
local reserved_keys_list = KEYS[2]
local key_data_hash = KEYS[3]
local max_keys = tonumber(ARGV[1])
local timestamp = ARGV[2]

-- Pop a key from the available list
local key = redis.call('LPOP', available_keys_list)
if not key then
    local available_count = redis.call('LLEN', available_keys_list)
    local reserved_count = redis.call('LLEN', reserved_keys_list)
    local total_keys = available_count + reserved_count
    if total_keys >= max_keys then
        return nil  -- No keys available
    end
    -- Add a new key to the available list
    local new_key = total_keys
    key = tostring(new_key)  -- Convert to string to ensure compatibility
end

-- Add the key to the reserved list
redis.call('RPUSH', reserved_keys_list, key)

-- Format: keypool:key_data:hash:key is the hash key, reservedAt is the field
local hash_key = key_data_hash .. ":" .. key
redis.call('HSET', hash_key, "reservedAt", timestamp)

-- Get all hash data
local hash_data = redis.call('HGETALL', hash_key)

-- Convert to a serialized format that's easier to parse in Go
-- Create a table with key and data fields
local result_table = {}
result_table["key"] = key

-- Convert the array of alternating keys/values to a key/value map
local data_map = {}
for i = 1, #hash_data, 2 do
    if i+1 <= #hash_data then
        data_map[hash_data[i]] = hash_data[i+1]
    end
end
result_table["data"] = data_map

-- Return the result table (Redis will serialize this to JSON)
return cjson.encode(result_table)
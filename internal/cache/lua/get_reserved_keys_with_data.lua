-- KEYS[1]: The reserved keys list - e.g., "keypool:reserved_keys:list"
-- KEYS[2]: The key data hash - e.g., "keypool:key_data:hash"

local reserved_keys_list = KEYS[1]
local key_data_hash = KEYS[2]

-- Get all keys from the reserved list
local keys = redis.call('LRANGE', reserved_keys_list, 0, -1)
local result = {}

-- For each reserved key, get its data from the hash
for i, key in ipairs(keys) do
  -- Use key as the identifier for result table
  result[i] = {key = key, data = {}}
  
  -- Get the hash key for this key
  local hash_key = key_data_hash .. ":" .. key
  
  -- Get all fields and values for this hash
  local data = redis.call('HGETALL', hash_key)
  
  -- Process the results (HGETALL returns alternating field/value)
  for j = 1, #data, 2 do
    local field = data[j]
    local value = data[j+1]
    result[i].data[field] = value
  end
end

-- Return as a JSON-compatible structure
return cjson.encode(result) 
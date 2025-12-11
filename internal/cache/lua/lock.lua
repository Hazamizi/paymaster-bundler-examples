-- Atomic lock script that handles three cases:
-- 1. If lock already exists with the same ID, extend TTL
-- 2. If lock doesn't exist, create it
-- 3. If lock exists with different ID, return error
--
-- KEYS[1] = lock key
-- ARGV[1] = lock ID
-- ARGV[2] = TTL in milliseconds

local lockKey = KEYS[1]
local lockID = ARGV[1]
local ttlMs = tonumber(ARGV[2])

-- Check if the lock exists
local currentLockID = redis.call('GET', lockKey)

if currentLockID then
    -- Lock exists, check if it's owned by the same ID
    if currentLockID == lockID then
        -- Extend TTL if same ID
        redis.call('PEXPIRE', lockKey, ttlMs)
        return 1 -- Success, lock extended
    else
        -- Locked by different ID
        return 0 -- Error, already locked
    end
else
    -- Lock doesn't exist, create it
    redis.call('SET', lockKey, lockID, 'PX', ttlMs)
    return 1 -- Success, lock acquired
end 
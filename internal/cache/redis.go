package cache

import (
	"context"
	_ "embed" // Used for lua scripts
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/chrishunter/1dler/internal/metrics"
	"github.com/ethereum/go-ethereum/log"
	"github.com/redis/go-redis/v9"
)

//go:embed lua/initialize_pool.lua
var initializePoolScriptSource string

//go:embed lua/reserve_key.lua
var reserveKeyScriptSource string

//go:embed lua/release_key.lua
var releaseKeyScriptSource string

//go:embed lua/get_reserved_keys_with_data.lua
var getReservedKeysWithDataScriptSource string

//go:embed lua/lock.lua
var lockScriptSource string

const (
	availableKeysList = "keypool:available_keys:list"
	reservedKeysList  = "keypool:reserved_keys:list" // Changed from hash to list
	keyDataHash       = "keypool:key_data:hash"      // New hash for storing key data
)

// RedisCache implements Cache using Redis
type RedisCache struct {
	client                        *redis.Client
	ctx                           context.Context
	initializePoolScript          *redis.Script
	reserveKeyScript              *redis.Script
	releaseKeyScript              *redis.Script
	getReservedKeysWithDataScript *redis.Script
	lockScript                    *redis.Script
	maxKeys                       uint
}

// NewRedisCache creates a new Redis cache
func NewRedisCache(addr string, password string, db int, maxKeys uint) (*RedisCache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	// Load Lua scripts
	initializePoolScript := redis.NewScript(initializePoolScriptSource)
	reserveKeyScript := redis.NewScript(reserveKeyScriptSource)
	releaseKeyScript := redis.NewScript(releaseKeyScriptSource)
	getReservedKeysWithDataScript := redis.NewScript(getReservedKeysWithDataScriptSource)
	lockScript := redis.NewScript(lockScriptSource)

	pipe := client.Pipeline()
	initCmd := initializePoolScript.Load(ctx, pipe)
	reserveCmd := reserveKeyScript.Load(ctx, pipe)
	releaseCmd := releaseKeyScript.Load(ctx, pipe)
	getWithDataCmd := getReservedKeysWithDataScript.Load(ctx, pipe)
	lockCmd := lockScript.Load(ctx, pipe)
	_, err := pipe.Exec(ctx)
	if err != nil {
		if initCmd.Err() != nil {
			return nil, fmt.Errorf("failed to load initialize_pool script: %w", initCmd.Err())
		}
		if reserveCmd.Err() != nil {
			return nil, fmt.Errorf("failed to load reserve_key script: %w", reserveCmd.Err())
		}
		if releaseCmd.Err() != nil {
			return nil, fmt.Errorf("failed to load release_key script: %w", releaseCmd.Err())
		}
		if getWithDataCmd.Err() != nil {
			return nil, fmt.Errorf("failed to load get_reserved_keys_with_data script: %w", getWithDataCmd.Err())
		}
		if lockCmd.Err() != nil {
			return nil, fmt.Errorf("failed to load lock script: %w", lockCmd.Err())
		}
		// Fallback error if individual checks didn't catch it
		return nil, fmt.Errorf("failed to load one or more Lua scripts: %w", err)
	}

	return &RedisCache{
		client:                        client,
		ctx:                           ctx,
		initializePoolScript:          initializePoolScript,
		reserveKeyScript:              reserveKeyScript,
		releaseKeyScript:              releaseKeyScript,
		getReservedKeysWithDataScript: getReservedKeysWithDataScript,
		lockScript:                    lockScript,
		maxKeys:                       maxKeys,
	}, nil
}

// Key based
func (c *RedisCache) InitializeKeys(targetSize uint) (int64, error) {
	if targetSize == 0 {
		return 0, fmt.Errorf("targetSize cannot be 0")
	}
	// Execute the Lua script to atomically add keys if needed
	// KEYS[1] = available keys list, KEYS[2] = reserved keys set
	// ARGV[1] = desired target size
	keys := []string{availableKeysList, reservedKeysList}
	argv := []any{targetSize}

	// Use Run which handles EVALSHA fallback automatically
	result, err := c.initializePoolScript.Run(c.ctx, c.client, keys, argv...).Int64() // Expecting integer result (count added)
	if err != nil {
		// Check for specific Lua script errors
		if redis.HasErrorPrefix(err, "ERR") {
			return 0, fmt.Errorf("initialize_pool script error: %w", err)
		}
		// Handle potential NOSCRIPT error
		if redis.HasErrorPrefix(err, "NOSCRIPT") {
			return 0, fmt.Errorf("initialize_pool script not loaded in redis cache: %w", err)
		}
		// General execution error
		return 0, fmt.Errorf("failed to run initialize_pool script for target size %d: %w", targetSize, err)
	}

	return result, nil // Success
}

// ReserveNextKey reserves the next available key
func (c *RedisCache) ReserveNextKey() (uint, map[string]any, error) {
	// Use our atomic Lua script to reserve a key
	// KEYS[1] = available keys list, KEYS[2] = reserved keys list, KEYS[3] = key data hash
	// ARGV[1] = max keys, ARGV[2] = current timestamp
	keys := []string{availableKeysList, reservedKeysList, keyDataHash}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	argv := []any{c.maxKeys, timestamp}

	// Execute the script
	result, err := c.reserveKeyScript.Run(c.ctx, c.client, keys, argv...).Result()

	if err == redis.Nil || result == nil {
		return 0, nil, ErrNoKeysAvailable
	}
	if err != nil {
		return 0, nil, fmt.Errorf("failed to reserve next key: %w", err)
	}

	// Parse the JSON result
	jsonStr, ok := result.(string)
	if !ok {
		return 0, nil, fmt.Errorf("invalid result format from reserve_key script: %v", result)
	}

	// Unmarshal into a struct
	var resultObj struct {
		Key  string         `json:"key"`
		Data map[string]any `json:"data"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &resultObj); err != nil {
		return 0, nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	// Convert key to uint
	keyNum, err := strconv.ParseUint(resultObj.Key, 10, 32)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid key number: %w", err)
	}

	return uint(keyNum), resultObj.Data, nil
}

// ReleaseKey releases a key from the reserved list
func (c *RedisCache) ReleaseKey(keyNum uint, status string) error {
	keyStr := strconv.FormatUint(uint64(keyNum), 10)

	// Try to get reservation time for metrics before releasing
	reservedAt, err := c.client.HGet(c.ctx, keyDataHash, keyStr+":reservedAt").Int64()

	// Use our atomic Lua script to release a key
	// KEYS[1] = available keys list, KEYS[2] = reserved keys list, KEYS[3] = key data hash
	// ARGV[1] = key to release
	keys := []string{availableKeysList, reservedKeysList, keyDataHash}
	argv := []any{keyStr, status}

	// Execute the script
	result, releaseErr := c.releaseKeyScript.Run(c.ctx, c.client, keys, argv...).Int()
	if releaseErr != nil {
		return fmt.Errorf("failed to release key: %w", releaseErr)
	}

	// Check if the key was released (result will be 0 if key was not found)
	if result == 0 {
		return fmt.Errorf("key %d was not in the reserved list", keyNum)
	}

	// If we got reservation time earlier, record metrics
	if err == nil && reservedAt > 0 {
		reservedAtTime := time.Unix(reservedAt, 0)
		duration := time.Since(reservedAtTime)
		metrics.KeyReleaseDuration.WithLabelValues().Observe(duration.Seconds())
	}

	return nil
}

// Cache based
func (c *RedisCache) AddItem(key string, value map[string]any, ttl time.Duration) error {
	// Marshal the map[string]any into JSON bytes
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value for cache: %w", err)
	}

	// Store the JSON byte slice in Redis
	return c.client.Set(c.ctx, key, data, ttl).Err()
}

func (c *RedisCache) RemoveItem(key string) error {
	result := c.client.Del(c.ctx, key)
	if err := result.Err(); err != nil {
		return err
	}
	if result.Val() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (c *RedisCache) GetItem(key string) (map[string]any, error) {
	data, err := c.client.Get(c.ctx, key).Bytes()
	if err == redis.Nil {
		return nil, ErrItemNotFound
	}
	if err != nil {
		return nil, err
	}

	// Unmarshal the JSON data back into a map[string]any
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("failed to unmarshal value from cache: %w", err)
	}

	return value, nil
}

// Lock methods
func (c *RedisCache) Lock(key string, id string, ttl time.Duration) error {
	lockKey := key + ":lock"

	// Execute the atomic Lua lock script
	// KEYS[1] = lock key
	// ARGV[1] = lock ID
	// ARGV[2] = TTL in milliseconds
	keys := []string{lockKey}
	ttlMs := int64(ttl / time.Millisecond)
	argv := []any{id, ttlMs}

	result, err := c.lockScript.Run(c.ctx, c.client, keys, argv...).Int()
	if err != nil {
		return fmt.Errorf("failed to execute lock script: %w", err)
	}

	if result == 0 {
		return ErrItemLocked
	}

	return nil
}

// todo make atomic single call
func (c *RedisCache) Unlock(key string, id string) error {
	lockKey := key + ":lock"
	lockValue, err := c.client.Get(c.ctx, lockKey).Result()
	if err == redis.Nil {
		return ErrItemNotFound
	}
	if err != nil {
		return err
	}
	if lockValue != id {
		return ErrItemLocked
	}

	return c.client.Del(c.ctx, lockKey).Err()
}

// PeekNextKey returns the next available key number without removing it
func (c *RedisCache) PeekNextKey() (uint, error) {
	// Get the first key from the list without removing it using LINDEX
	key, err := c.client.LIndex(c.ctx, availableKeysList, 0).Result()
	if err == redis.Nil {
		return 0, ErrNoKeysAvailable
	}
	if err != nil {
		return 0, fmt.Errorf("failed to peek next key: %w", err)
	}
	if key == "" {
		return 0, ErrNoKeysAvailable
	}

	keyNum, err := strconv.ParseUint(key, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid key number: %w", err)
	}

	return uint(keyNum), nil
}

// IsKeyReserved checks if a key is currently in the reserved list
func (c *RedisCache) IsKeyReserved(keyNum uint) (bool, error) {
	keyStr := strconv.FormatUint(uint64(keyNum), 10)

	// Get all keys in the reserved list
	keys, err := c.client.LRange(c.ctx, reservedKeysList, 0, -1).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check if key is reserved: %w", err)
	}

	// Check if our key is in the list
	for _, k := range keys {
		if k == keyStr {
			return true, nil
		}
	}

	return false, nil
}

// GetPoolStatus returns the count of available and reserved keys
func (c *RedisCache) GetPoolStatus() (available int64, reserved int64, err error) {
	pipe := c.client.Pipeline()
	availableCmd := pipe.LLen(c.ctx, availableKeysList)
	reservedCmd := pipe.LLen(c.ctx, reservedKeysList)

	_, err = pipe.Exec(c.ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get pool status: %w", err)
	}

	return availableCmd.Val(), reservedCmd.Val(), nil
}

// UpdateKeyData updates fields for a key
func (c *RedisCache) UpdateKeyData(keyNum uint, fields map[string]any) error {
	keyStr := strconv.FormatUint(uint64(keyNum), 10)
	hashKey := keyDataHash + ":" + keyStr

	err := c.client.HSet(c.ctx, hashKey, fields).Err()
	if err != nil {
		return fmt.Errorf("failed to update key data: %w", err)
	}

	return nil
}

// GetKeyData retrieves data for a key
func (c *RedisCache) GetKeyData(keyNum uint) (map[string]any, error) {
	keyStr := strconv.FormatUint(uint64(keyNum), 10)
	hashKey := keyDataHash + ":" + keyStr

	// Get all fields for this hash
	cmd := c.client.HGetAll(c.ctx, hashKey)
	if err := cmd.Err(); err != nil {
		return nil, fmt.Errorf("failed to get key data: %w", err)
	}

	// Convert to map[string]any
	stringData := cmd.Val()
	if len(stringData) == 0 {
		return map[string]any{}, nil
	}

	// Convert to map[string]any
	result := make(map[string]any, len(stringData))
	for field, value := range stringData {
		result[field] = value
	}

	return result, nil
}

// GetAllReservedKeysWithData returns all keys and their associated data
func (c *RedisCache) GetAllReservedKeysWithData() (map[uint]map[string]any, error) {
	// Use the Lua script for efficiency
	keys := []string{reservedKeysList, keyDataHash}

	// Execute the script
	result, err := c.getReservedKeysWithDataScript.Run(c.ctx, c.client, keys).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get reserved keys with data: %w", err)
	}

	// Create the return map
	resultMap := make(map[uint]map[string]any)

	// If result is nil, return empty map
	if result == nil {
		return resultMap, nil
	}

	// Parse the JSON result
	jsonStr, ok := result.(string)
	if !ok {
		log.Error("invalid result format from get_reserved_keys_with_data script", "result", result)
		return nil, fmt.Errorf("unable to convert result to string")
	}

	// Handle empty result
	if jsonStr == "" || jsonStr == "{}" {
		return resultMap, nil
	}

	var jsonResult []struct {
		Key  string            `json:"key,omitempty"`
		Data map[string]string `json:"data,omitempty"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &jsonResult); err != nil {
		log.Error("failed to unmarshal result", "result", jsonStr, "error", err)
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	// Convert to the expected format
	for _, item := range jsonResult {
		keyNum, err := strconv.ParseUint(item.Key, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid key in result: %s, error: %w", item.Key, err)
		}

		dataMap := make(map[string]any)
		for k, v := range item.Data {
			dataMap[k] = v
		}

		resultMap[uint(keyNum)] = dataMap
	}

	return resultMap, nil
}

func (c *RedisCache) Close() error {
	return c.client.Close()
}

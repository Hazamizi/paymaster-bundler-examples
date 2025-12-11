package cache

import (
	"fmt"
	"sync"
	"time"
)

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
	locked    bool
	lockUntil time.Time
	data      map[string]interface{} // Added for key data storage
}

// MemoryCache implements Cache using in-memory storage
type MemoryCache struct {
	items         map[string]*cacheItem
	availableKeys []uint                   // Stack of available keys
	queues        map[string][]interface{} // Queue storage
	mu            sync.RWMutex
	maxKeys       uint // Maximum number of keys allowed
	nextKeyNum    uint // Next key number to assign
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache(maxKeys uint) *MemoryCache {
	cache := &MemoryCache{
		items:         make(map[string]*cacheItem),
		availableKeys: make([]uint, 0),
		queues:        make(map[string][]interface{}),
		maxKeys:       maxKeys,
		nextKeyNum:    0,
	}

	// Start cleanup goroutine
	go cache.cleanup()

	return cache
}

func (c *MemoryCache) cleanup() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, item := range c.items {
			if !item.expiresAt.IsZero() && now.After(item.expiresAt) {
				delete(c.items, key)
			}
		}
		c.mu.Unlock()
	}
}

func (c *MemoryCache) InitializeKeys(numKeys uint) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Set the max keys if it's not already set
	if c.maxKeys == 0 {
		c.maxKeys = numKeys
	}

	// Initialize available keys slice
	c.availableKeys = make([]uint, 0, numKeys)

	// Add all keys to both the map and the available keys slice
	// Reverse the order so lower keys are at the end (popped first)
	for i := int(numKeys) - 1; i >= 0; i-- {
		key := GetKeyString(uint(i))
		c.items[key] = &cacheItem{
			value: uint(i),
		}
		c.availableKeys = append(c.availableKeys, uint(i))
	}

	// Set the next key number
	c.nextKeyNum = numKeys

	return int64(numKeys), nil
}

func (c *MemoryCache) ReserveNextKey() (uint, map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If no keys available, try to provision a new one if we're under the max
	if len(c.availableKeys) == 0 {
		totalKeys := c.nextKeyNum
		if totalKeys < c.maxKeys {
			// Create a new key
			newKey := c.nextKeyNum
			key := GetKeyString(newKey)
			c.items[key] = &cacheItem{
				value: newKey,
				data:  make(map[string]interface{}),
			}
			c.availableKeys = append(c.availableKeys, newKey)
			c.nextKeyNum++
		} else {
			return 0, nil, ErrNoKeysAvailable
		}
	}

	// Pop the last key from the available keys slice
	keyNum := c.availableKeys[len(c.availableKeys)-1]
	c.availableKeys = c.availableKeys[:len(c.availableKeys)-1]

	// Lock the key in the items map
	key := GetKeyString(keyNum)
	item := c.items[key]
	item.locked = true

	// Store reservation timestamp
	if item.data == nil {
		item.data = make(map[string]interface{})
	}
	item.data["reservedAt"] = time.Now().Unix()

	// Convert to map[string]any for consistency with Redis implementation
	keyData := make(map[string]any)
	for k, v := range item.data {
		keyData[k] = v
	}

	return keyNum, keyData, nil
}

func (c *MemoryCache) AddItem(key string, value map[string]any, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	c.items[key] = &cacheItem{
		value:     value,
		expiresAt: expiresAt,
		locked:    false,
	}

	return nil
}

func (c *MemoryCache) RemoveItem(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[key]; !exists {
		return ErrItemNotFound
	}

	delete(c.items, key)
	return nil
}

func (c *MemoryCache) Lock(key string, id string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	lockKey := key + ":lock"
	item, exists := c.items[lockKey]

	if exists {
		// Lock exists, check if it's owned by the same ID
		if item.value == id {
			// Extend TTL if same ID
			item.lockUntil = time.Now().Add(ttl)
			item.expiresAt = time.Now().Add(ttl)
			return nil
		} else {
			// Locked by different ID
			return ErrItemLocked
		}
	} else {
		// Lock doesn't exist, create it
		c.items[lockKey] = &cacheItem{
			expiresAt: time.Now().Add(ttl),
			locked:    true,
			lockUntil: time.Now().Add(ttl),
			value:     id,
		}
		return nil
	}
}

func (c *MemoryCache) ExtendLock(key string, id string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, exists := c.items[key]
	if !exists {
		return ErrItemNotFound
	}

	if !item.locked {
		return ErrItemNotFound
	}

	if item.value != id {
		return ErrItemLocked
	}

	now := time.Now()
	item.lockUntil = now.Add(ttl)
	item.expiresAt = now.Add(ttl)
	return nil
}

func (c *MemoryCache) Unlock(key string, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	lockKey := key + ":lock"
	item, exists := c.items[lockKey]
	if !exists {
		return ErrItemNotFound
	}

	if item.value != id {
		return ErrItemLocked
	}

	delete(c.items, lockKey)
	return nil
}

func (c *MemoryCache) GetItem(key string) (map[string]any, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, ErrItemNotFound
	}

	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		return nil, ErrItemNotFound
	}

	// Try to convert to map[string]any
	mapValue, ok := item.value.(map[string]any)
	if ok {
		return mapValue, nil
	}

	// Try to convert from map[string]interface{} if needed
	if mapInterface, ok := item.value.(map[string]interface{}); ok {
		mapValue = make(map[string]any)
		for k, v := range mapInterface {
			mapValue[k] = v
		}
		return mapValue, nil
	}

	return nil, fmt.Errorf("cached item for key '%s' is not of type map[string]any", key)
}

func (c *MemoryCache) Close() error {
	return nil
}

// ReleaseKey releases a key back to the available pool
func (c *MemoryCache) ReleaseKey(keyNum uint, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := GetKeyString(uint(keyNum))

	// Check if the key exists and is locked
	if item, exists := c.items[key]; exists {
		if !item.locked {
			return fmt.Errorf("key %d is not reserved", keyNum)
		}
		item.locked = false

		// Store status if provided
		if status != "" && item.data != nil {
			item.data["status"] = status
		}
	} else {
		return fmt.Errorf("key %d not found", keyNum)
	}

	// Add the key back to the available pool
	c.availableKeys = append(c.availableKeys, uint(keyNum))

	return nil
}

// GetKeyData gets the data associated with a key
func (c *MemoryCache) GetKeyData(keyNum uint) (map[string]any, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := GetKeyString(keyNum)
	item, exists := c.items[key]
	if !exists {
		return nil, fmt.Errorf("key %d not found", keyNum)
	}

	if !item.locked {
		return nil, fmt.Errorf("key %d is not reserved", keyNum)
	}

	// Convert to map[string]any for consistency
	keyData := make(map[string]any)
	for k, v := range item.data {
		keyData[k] = v
	}

	return keyData, nil
}

// PeekNextKey returns the next available key number without removing it
func (c *MemoryCache) PeekNextKey() (uint, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.availableKeys) == 0 {
		return 0, fmt.Errorf("no keys available")
	}

	// Return the last key (which is the next one to be used)
	return c.availableKeys[len(c.availableKeys)-1], nil
}

// GetKeyReservationTime returns when a key was reserved and for how long
func (c *MemoryCache) GetKeyReservationTime(keyNum uint) (reservedAt time.Time, duration time.Duration, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := GetKeyString(keyNum)
	item, exists := c.items[key]
	if !exists {
		return time.Time{}, 0, fmt.Errorf("key %d not found", keyNum)
	}

	if !item.locked {
		return time.Time{}, 0, fmt.Errorf("key %d is not reserved", keyNum)
	}

	// Check if reservedAt exists in the data
	if item.data == nil || item.data["reservedAt"] == nil {
		return time.Time{}, 0, fmt.Errorf("key %d has no reservation timestamp", keyNum)
	}

	// Get the timestamp
	var unixTime int64
	switch v := item.data["reservedAt"].(type) {
	case int64:
		unixTime = v
	case float64:
		unixTime = int64(v)
	case int:
		unixTime = int64(v)
	default:
		return time.Time{}, 0, fmt.Errorf("invalid timestamp format for key %d", keyNum)
	}

	// Create time object and calculate duration
	reservedAt = time.Unix(unixTime, 0)
	duration = time.Since(reservedAt)

	return reservedAt, duration, nil
}

// StoreKeyData stores data for a reserved key
func (c *MemoryCache) StoreKeyData(keyNum uint, data map[string]any, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := GetKeyString(keyNum)
	item, exists := c.items[key]
	if !exists {
		return fmt.Errorf("key %d not found", keyNum)
	}

	if !item.locked {
		return fmt.Errorf("key %d is not reserved", keyNum)
	}

	// Preserve the reservation timestamp if it exists
	reservedAt, hasReservedAt := item.data["reservedAt"]

	// Replace the entire data map with a copy
	newData := make(map[string]interface{})
	for k, v := range data {
		newData[k] = v
	}
	item.data = newData

	// Restore the timestamp if it was present
	if hasReservedAt {
		item.data["reservedAt"] = reservedAt
	}

	return nil
}

// UpdateKeyData updates the data for a key
func (c *MemoryCache) UpdateKeyData(keyNum uint, data map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := GetKeyString(keyNum)
	item, exists := c.items[key]
	if !exists {
		return fmt.Errorf("key %d not found", keyNum)
	}

	// Update the existing data map with new fields
	if item.data == nil {
		item.data = make(map[string]interface{})
	}

	// Add all fields from the provided data map
	for k, v := range data {
		item.data[k] = v
	}

	return nil
}

// GetAllReservedKeysWithData returns all keys and their associated data
func (c *MemoryCache) GetAllReservedKeysWithData() (map[uint]map[string]any, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[uint]map[string]any)

	for _, item := range c.items {
		// Skip if the key is not a valid key format or is not locked
		if !item.locked {
			continue
		}

		// Extract the key number
		var keyNum uint
		switch val := item.value.(type) {
		case uint:
			keyNum = val
		case int:
			keyNum = uint(val)
		case float64:
			keyNum = uint(val)
		default:
			// Skip keys that don't have a numeric value
			continue
		}

		// Add key data to result
		if item.data != nil {
			result[keyNum] = make(map[string]any)
			for k, v := range item.data {
				result[keyNum][k] = v
			}
		} else {
			result[keyNum] = make(map[string]any)
		}
	}

	return result, nil
}

package cache

import (
	"errors"
	"time"
)

var (
	ErrItemNotFound    = errors.New("item not found in cache")
	ErrItemLocked      = errors.New("item is locked")
	ErrLockFailed      = errors.New("failed to acquire lock")
	ErrNoKeysAvailable = errors.New("no keys available")
	ErrTimeout         = errors.New("operation timed out")
)

// Cache defines the interface for cache implementations
type Cache interface {
	// Cache methods
	// AddItem adds an item to the cache with a TTL
	AddItem(key string, value map[string]interface{}, ttl time.Duration) error

	// RemoveItem removes an item from the cache
	RemoveItem(key string) error

	// GetItem retrieves an item from the cache
	GetItem(key string) (map[string]any, error)

	// Lock methods
	// Lock attempts to lock an item for a specified duration
	// Will extend ttl if id currently holds the lock
	// Returns ErrItemLocked if already locked, ErrItemNotFound if item doesn't exist
	Lock(key string, id string, ttl time.Duration) error

	// Unlock releases a lock on an item
	Unlock(key string, id string) error

	// Key based methods
	// InitializeKeys initializes a range of keys in the cache if not already initialized
	InitializeKeys(numKeys uint) (int64, error)

	// ReserveNextKey atomically reserves and locks the next available key from the pool
	// Returns the reserved key number or an error if none available
	ReserveNextKey() (uint, map[string]any, error)

	// ReleaseKey adds a key back to the available pool
	ReleaseKey(keyNum uint, status string) error

	// PeekNextKey returns the next available key number without removing it
	PeekNextKey() (uint, error)

	// GetAllReservedKeysWithData returns all keys and their associated data
	GetAllReservedKeysWithData() (map[uint]map[string]any, error)

	// UpdateKeyData updates the data for a key
	UpdateKeyData(keyNum uint, data map[string]any) error

	// Close cleans up any resources used by the cache
	Close() error
}

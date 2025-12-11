package cache

import (
	"strconv"
)

const (
	keyPrefix = "key:"
)

// GetKeyString converts a key number to its string representation
func GetKeyString(keyNum uint) string {
	return keyPrefix + strconv.FormatUint(uint64(keyNum), 10)
}

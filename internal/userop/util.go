package userop

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Helper functions
func getHexBytesOrDefault(value string, defaultValue []byte) []byte {
	if value == "" {
		return defaultValue
	}
	return common.FromHex(value)
}

// parseNumberOrDefault parses a string as either hex or decimal into a big.Int
// Returns the parsed number or default value if parsing fails
func parseNumberOrDefault(value string, defaultValue *big.Int) *big.Int {
	if value == "" {
		return defaultValue
	}

	// Try parsing as hex first
	if len(value) > 2 && (value[:2] == "0x") {
		if result, success := new(big.Int).SetString(value[2:], 16); success {
			return result
		}
	}

	// Try parsing as decimal
	if result, success := new(big.Int).SetString(value, 10); success {
		return result
	}

	// If both parsing attempts fail, return default
	return defaultValue
}

package util

import (
	"fmt"
	"strconv"
	"strings"
)

func GetNonceFromErrorMessage(errMsg string) (int64, error) {
	prefix := "nonce too low: next nonce"
	start := strings.Index(errMsg, prefix)
	if start != -1 {
		start += len(prefix)
		end := strings.Index(errMsg[start:], ",")
		if end != -1 {
			nextNonceStr := strings.TrimSpace(errMsg[start : start+end])
			nextNonce, err := strconv.ParseInt(nextNonceStr, 10, 64)
			if err == nil {
				return nextNonce, nil
			}
			return 0, fmt.Errorf("failed to parse nonce: %w", err)
		}
	}
	return 0, fmt.Errorf("nonce information not found in error message")
}

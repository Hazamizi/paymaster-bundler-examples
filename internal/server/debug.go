package server

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chrishunter/1dler/internal/keys"
	"github.com/ethereum/go-ethereum/common"
)

// handleGetNextKey handles the debug_key_next method
// Returns the next available key if debug mode is enabled
func (s *Server) handleGetNextKey(ctx context.Context) (*common.Address, error) {
	if !s.debug {
		return nil, fmt.Errorf("debug methods require debug mode to be enabled")
	}
	key, err := s.keyService.GetNextSigner()
	if err != nil {
		return nil, err
	}

	return &key.Address, nil
}

// handleReleaseKey handles the debug_key_release method
// Releases a previously reserved key if debug mode is enabled
func (s *Server) handleReleaseKey(ctx context.Context, params interface{}) (interface{}, error) {
	if !s.debug {
		return nil, fmt.Errorf("debug methods require debug mode to be enabled")
	}

	paramsArray, ok := params.([]interface{})
	if !ok || len(paramsArray) == 0 {
		return nil, fmt.Errorf("invalid parameters: expected array with key index")
	}

	indexStr, ok := paramsArray[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid parameter type: expected string")
	}

	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid key index: %w", err)
	}

	err = s.keyService.ReleaseSigner(&keys.RelaySigner{
		Index: uint(index),
	}, "")
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success": true,
	}, nil
}

func (s *Server) handlePeakNextKey(ctx context.Context) (*common.Address, error) {
	if !s.debug {
		return nil, fmt.Errorf("debug methods require debug mode to be enabled")
	}

	address, err := s.keyService.PeekNextKey()
	if err != nil {
		return nil, err
	}
	return address, nil
}

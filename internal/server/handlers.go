package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"

	"github.com/chrishunter/1dler/internal/errors"
	"github.com/chrishunter/1dler/internal/keys"
	"github.com/chrishunter/1dler/internal/metrics"
	"github.com/chrishunter/1dler/internal/simulator"
	"github.com/chrishunter/1dler/internal/userop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Handler functions for JSON-RPC methods

// eth_sendUserOperation
func (s *Server) handleSendUserOperation(ctx context.Context, params any) (any, any, error) {
	startTime := time.Now()
	log.Info("Handling sendUserOperation request", "params", params)

	// Parse params array
	paramsArray, ok := params.([]any)
	if !ok || len(paramsArray) != 2 {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid params: expected [UserOperation, EntryPoint]",
		}
	}

	// Parse EntryPoint address
	entryPointStr, ok := paramsArray[1].(string)
	if !ok {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid EntryPoint: expected string address",
		}
	}

	entrypointAddress := common.HexToAddress(entryPointStr)
	if !slices.Contains(s.entryPoints, entrypointAddress) {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: fmt.Sprintf("Invalid EntryPoint: expected one of %v", s.entryPoints),
		}
	}

	metrics.EntryPointVersion.WithLabelValues(entrypointAddress.Hex()).Inc()

	// Parse UserOperation
	userOp, err := userop.ParseUserOperation(paramsArray[0], entrypointAddress, s.chainID)
	if err != nil {
		log.Error("Failed to parse UserOperation", "error", err, "raw_userop", paramsArray[0])
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: fmt.Sprintf("Invalid UserOperation: %v", err),
		}
	}

	// Validate gas prices against network conditions using batch call
	blockBaseFee, feePerGas, priorityFee, err := s.getCurrentGasConditions(ctx)
	if err != nil {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("Failed to get network gas prices: %v", err),
		}
	}

	priorityFee = new(big.Int).Add(priorityFee, new(big.Int).Div(priorityFee, common.Big3)) // 1.3x

	submitFee := new(big.Int).Add(
		priorityFee,
		new(big.Int).Mul(blockBaseFee, common.Big3),
	)

	// todo require a maxFeePerGas entry buffer for inclusion which is configurable
	if userOp.MaxFeePerGas.Cmp(feePerGas) < 0 {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidMaxFeePerGas,
			Message: fmt.Sprintf("MaxFeePerGas (%v) is less than current required fee (%v)", userOp.MaxFeePerGas, feePerGas),
		}
	}

	// emit gas metrics
	gasDifference := new(big.Int).Sub(submitFee, userOp.MaxFeePerGas)
	metrics.GasMeasurements.WithLabelValues("maxFeePerGasDiff").Set(float64(gasDifference.Int64()))
	metrics.GasMeasurements.WithLabelValues("baseFee").Set(float64(blockBaseFee.Int64()))
	metrics.GasMeasurements.WithLabelValues("feePerGas").Set(float64(feePerGas.Int64()))
	metrics.GasMeasurements.WithLabelValues("priorityFee").Set(float64(priorityFee.Int64()))

	if userOp.MaxPriorityFeePerGas.Cmp(priorityFee) < 0 {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidMaxPriorityFeePerGas,
			Message: fmt.Sprintf("MaxPriorityFeePerGas (%v) is less than current network priority fee (%v)", userOp.MaxPriorityFeePerGas, priorityFee),
		}
	}

	pvg, err := s.simulator.CalculatePVG(ctx, userOp.Pack())
	if err != nil {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("Failed to calculate PVG: %v", err),
		}
	}

	if userOp.PreVerificationGas.Cmp(pvg) < 0 {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidPreVerificationGas,
			Message: fmt.Sprintf("PreVerificationGas is less than required value of: %v", pvg),
		}
	}

	// Check if transaction size exceeds max allowed size
	txSize := userOp.GetTxSize()
	if txSize > s.maxTxSize {
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: fmt.Sprintf("Transaction total gas limit size (%d) exceeds maximum allowed size of %d", txSize, s.maxTxSize),
		}
	}

	userOpHash := userOp.GetUserOpHash()
	if !s.trusted {
		simErr := s.simulator.CheckValidation(ctx, userOp, nil)
		if simErr != nil {
			log.Error("UserOp validation failed", "error", simErr, "userOpHash", userOpHash)
			return nil, nil, &RPCError{
				Code:    simErr.Code,
				Message: simErr.Message,
			}
		}
	}

	// start simulation
	simulationStart := time.Now()
	simBundlerAddress := s.keyService.GetPrimaryBundlerAddress()
	gasLimit := txSize + 100000
	simOpts := &bind.TransactOpts{
		GasFeeCap: submitFee,
		GasTipCap: priorityFee,
		GasLimit:  gasLimit,
	}

	simErr := s.simulator.CallHandleOps(ctx, userOp, simBundlerAddress, simOpts)
	if simErr != nil {
		log.Error("bundle transaction precheck failed", "error", simErr, "userOpHash", userOpHash)
		metrics.UserOpSimulationResult.WithLabelValues("revert").Inc()
		metrics.UserOpProcessingTime.WithLabelValues("simulation").Observe(time.Since(simulationStart).Seconds())
		return nil, nil, &RPCError{
			Code:    simErr.Code,
			Message: simErr.Message,
		}
	}

	metrics.UserOpSimulationResult.WithLabelValues("success").Inc()
	metrics.UserOpProcessingTime.WithLabelValues("simulation").Observe(time.Since(simulationStart).Seconds())

	// Get a signer for sending the transaction
	submissionStart := time.Now()
	signer, err := s.keyService.GetNextSigner()
	if err != nil {
		log.Error("Failed to get next signer", "error", err)
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("Failed to get signer: %v", err),
		}
	}

	log.Info("Using signer for userOp relay", "address", signer.Address.Hex(), "keyNumber", signer.Index, "userOpHash", userOpHash)

	// Prepare transaction options
	opts, err := signer.GetTransactOpts(ctx, s.writeClient, submitFee, priorityFee)
	if err != nil {
		log.Error("Failed to prepare transaction options", "error", err)
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("Failed to prepare transaction options: %v", err),
		}
	}

	opts.GasLimit = gasLimit
	tx, err := userOp.Send(ctx, s.writeClient, opts)
	if err != nil {
		s.keyService.ReleaseSigner(signer, "") // no status change
		log.Error("Failed to send user operation transaction", "error", err)
		return nil, nil, &RPCError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("Failed to send transaction: %v", err),
		}
	}

	metrics.UserOpProcessingTime.WithLabelValues("submission").Observe(time.Since(submissionStart).Seconds())
	log.Info("UserOp relayed", "txnHash", tx.Hash().Hex(), "userOpHash", userOpHash)

	// here update cache with txn hash
	txnHash := tx.Hash().Hex()
	s.keyService.UpdateTransactionForSigner(signer.Index, txnHash, userOpHash, keys.Pending, opts.Nonce.Uint64(), tx.GasFeeCap().Uint64(), tx.GasTipCap().Uint64())

	// Store info in the cache with UserOpHash as key
	userOpJsonBytes, err := json.Marshal(paramsArray[0])
	if err != nil {
		log.Warn("Failed to marshal UserOperation for caching", "error", err)
	}

	if err := s.cache.AddItem(userOpHash, map[string]any{
		"userOpJson": string(userOpJsonBytes),
		"txnHash":    txnHash,
		"entryPoint": entryPointStr,
		"timestamp":  time.Now().Format(time.RFC3339),
	}, 3*time.Minute); err != nil {
		log.Warn("Failed to cache transaction hash", "error", err)
	}

	metrics.UserOpProcessingTime.WithLabelValues("total").Observe(time.Since(startTime).Seconds())
	if s.returnTxnHashInSendUserOp {
		enhancedResponse := &EnhancedSendUserOpResponse{
			TxnHash: txnHash,
		}

		return userOpHash, enhancedResponse, nil
	}

	return userOpHash, nil, nil
}

// eth_getUserOperationByHash
func (s *Server) handleGetUserOperationByHash(ctx context.Context, params any) (any, error) {
	log.Info("Handling getUserOperationByHash request", "params", params)

	// Parse params array
	paramsArray, ok := params.([]any)
	if !ok || len(paramsArray) != 1 {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid params: expected [userOpHash]",
		}
	}

	// Get userOpHash
	userOpHash, ok := paramsArray[0].(string)
	if !ok {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid userOpHash: expected string",
		}
	}

	if len(userOpHash) != 66 || !strings.HasPrefix(userOpHash, "0x") {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid userOpHash: expected 0x-prefixed 32 byte hex string",
		}
	}

	// Get from cache
	cachedInfo, err := s.cache.GetItem(userOpHash)
	if err != nil {
		metrics.CacheMisses.WithLabelValues("userop").Inc()
		log.Warn("UserOperation not found in cache", "error", err)
		return json.RawMessage("null"), nil
	}

	// Parse the original UserOp JSON
	var userOp map[string]any
	if err := json.Unmarshal([]byte(cachedInfo["userOpJson"].(string)), &userOp); err != nil {
		log.Error("Failed to parse cached UserOperation", "error", err)
		return json.RawMessage("null"), nil
	}

	// Get transaction receipt using the write client
	receipt, err := s.writeClient.TransactionReceipt(ctx, common.HexToHash(cachedInfo["txnHash"].(string)))
	if err != nil {
		// If the transaction is pending (receipt not found), return null as per EIP-4337 spec
		if err.Error() == "not found" {
			metrics.BundleTransactionResult.WithLabelValues("not found").Inc()
			return json.RawMessage("null"), nil
		}

		log.Error("Failed to get transaction receipt", "error", err, "txnHash", cachedInfo["txnHash"], "userOpHash", userOpHash)
		// For other errors, return internal error
		return json.RawMessage("null"), nil
	}

	timeWhenSubmitted, err := time.Parse(time.RFC3339, cachedInfo["timestamp"].(string))
	if err == nil {
		metrics.UserOpProcessingTime.WithLabelValues("hashSuccessResponse").Observe(time.Since(timeWhenSubmitted).Seconds())
	}

	if receipt.Status == types.ReceiptStatusFailed {
		log.Warn("UserOp in failing txn", "txnHash", cachedInfo["txnHash"], "userOpHash", userOpHash)
		metrics.BundleTransactionResult.WithLabelValues("failed").Inc()
		return json.RawMessage("null"), nil
	}

	return map[string]any{
		"userOperation":   userOp,
		"entryPoint":      cachedInfo["entryPoint"],
		"transactionHash": cachedInfo["txnHash"],
		"blockHash":       receipt.BlockHash.Hex(),
		"blockNumber":     hexutil.EncodeBig(receipt.BlockNumber),
	}, nil
}

// eth_getUserOperationReceipt
func (s *Server) handleGetUserOperationReceipt(ctx context.Context, params any) (any, error) {
	log.Info("Handling getUserOperationReceipt request", "params", params)

	// Parse params array
	paramsArray, ok := params.([]any)
	if !ok || len(paramsArray) != 1 {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid params: expected [userOpHash]",
		}
	}

	// Get userOpHash
	userOpHash, ok := paramsArray[0].(string)
	if !ok {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid userOpHash: expected string",
		}
	}

	// check if userOpHash is a valid hash
	if len(userOpHash) != 66 || !strings.HasPrefix(userOpHash, "0x") {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid userOpHash: expected 0x-prefixed 32 byte hex string",
		}
	}

	// Get from cache
	cachedInfo, err := s.cache.GetItem(userOpHash)
	if err != nil {
		metrics.CacheMisses.WithLabelValues("userop").Inc()
		log.Warn("UserOperation not found in cache", "error", err, "userOpHash", userOpHash)
		return json.RawMessage("null"), nil
	}

	// Get transaction receipt using the write client
	receipt, err := s.writeClient.TransactionReceipt(ctx, common.HexToHash(cachedInfo["txnHash"].(string)))
	if err != nil {
		// If the transaction is pending (receipt not found), return null as per EIP-4337 spec
		if err.Error() == "not found" {
			metrics.BundleTransactionResult.WithLabelValues("not found").Inc()
			return json.RawMessage("null"), nil
		}

		log.Error("Failed to get transaction receipt", "error", err, "txnHash", cachedInfo["txnHash"], "userOpHash", userOpHash)
		return json.RawMessage("null"), nil
	}

	if receipt.Status == types.ReceiptStatusFailed {
		log.Warn("UserOp in failing txn", "txnHash", cachedInfo["txnHash"], "userOpHash", userOpHash)
		metrics.BundleTransactionResult.WithLabelValues("failed").Inc()
		return json.RawMessage("null"), nil
	}

	// Get entryPoint contract instance
	entryPointAddress := common.HexToAddress(cachedInfo["entryPoint"].(string))
	entrypointContract, ok := s.entryPointContracts[entryPointAddress]
	if !ok {
		log.Error("EntryPoint contract not found", "address", entryPointAddress)
		return json.RawMessage("null"), nil
	}

	// Parse logs to find UserOperation event
	userOpHashBytes := common.HexToHash(userOpHash)
	for _, receiptLog := range receipt.Logs {
		if receiptLog.Address == entryPointAddress {
			if event, err := entrypointContract.ParseUserOperationEvent(*receiptLog); err == nil {
				if event.UserOpHash == userOpHashBytes {
					// Found our event
					var reason []byte
					// Check if there's a revert reason
					for _, log := range receipt.Logs {
						if revertEvent, err := entrypointContract.ParseUserOperationRevertReason(*log); err == nil {
							if revertEvent.UserOpHash == userOpHashBytes {
								reason = revertEvent.RevertReason
								break
							}
						}
					}

					timeWhenSubmitted, err := time.Parse(time.RFC3339, cachedInfo["timestamp"].(string))
					if err == nil {
						metrics.UserOpProcessingTime.WithLabelValues("hashSuccessResponse").Observe(time.Since(timeWhenSubmitted).Seconds())
					}

					if event.Success {
						metrics.UserOpLandedSuccess.WithLabelValues("success").Inc()
						log.Info("UserOp receipt fetched with success status", "userOpHash", userOpHash, "txnHash", cachedInfo["txnHash"])
					} else {
						metrics.UserOpLandedSuccess.WithLabelValues("failed").Inc()
						log.Info("UserOp receipt fetched with failed status", "userOpHash", userOpHash, "reason", hexutil.Encode(reason), "txnHash", cachedInfo["txnHash"])
					}

					return map[string]any{
						"userOpHash":    userOpHash,
						"entryPoint":    entryPointAddress.Hex(),
						"sender":        event.Sender.Hex(),
						"nonce":         hexutil.EncodeBig(event.Nonce),
						"paymaster":     event.Paymaster.Hex(),
						"actualGasCost": hexutil.EncodeBig(event.ActualGasCost),
						"actualGasUsed": hexutil.EncodeBig(event.ActualGasUsed),
						"success":       event.Success,
						"reason":        hexutil.Encode(reason),
						"logs":          receipt.Logs,
						"receipt":       receipt,
					}, nil
				}
			}
		}
	}

	log.Error("UserOperationEvent not found", "txnHash", cachedInfo["txnHash"])
	return json.RawMessage("null"), nil
}

// eth_estimateUserOperationGas
func (s *Server) handleEstimateUserOperationGas(ctx context.Context, params any) (any, error) {
	log.Info("Handling estimateUserOperationGas request", "params", params)

	// Parse params array
	paramsArray, ok := params.([]any)
	if !ok || len(paramsArray) != 2 && len(paramsArray) != 3 {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid params: expected [UserOperation, EntryPoint, StateOverrides(optional)]",
		}
	}
	// Parse EntryPoint address
	entryPointStr, ok := paramsArray[1].(string)
	if !ok {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: "Invalid EntryPoint: expected string address",
		}
	}
	entrypointAddress := common.HexToAddress(entryPointStr)
	if !slices.Contains(s.entryPoints, entrypointAddress) {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: fmt.Sprintf("Invalid EntryPoint: expected one of %v", s.entryPoints),
		}
	}

	metrics.EntryPointVersion.WithLabelValues(entrypointAddress.Hex()).Inc()

	// Parse UserOperation with defaults
	userOp, err := userop.ParseUserOperation(paramsArray[0], entrypointAddress, s.chainID)
	if err != nil {
		return nil, &RPCError{
			Code:    errors.ErrorCodeInvalidParams,
			Message: fmt.Sprintf("Invalid UserOperation: %v", err),
		}
	}

	// Get optional state overrides
	var overrides *simulator.OverrideSet

	if len(paramsArray) > 2 && paramsArray[2] != nil {
		overridesJsonBytes, marshalErr := json.Marshal(paramsArray[2])
		if marshalErr != nil {
			return nil, &RPCError{
				Code:    errors.ErrorCodeInvalidParams,
				Message: fmt.Sprintf("Invalid state overrides format: could not marshal input: %v", marshalErr),
			}
		}

		// Now parse the JSON bytes using the simulator function
		overrides, err = simulator.ParseOverrides(overridesJsonBytes)
		if err != nil {
			return nil, &RPCError{
				Code:    errors.ErrorCodeInvalidParams,
				Message: fmt.Sprintf("Invalid state overrides content: %v", err),
			}
		}
	}

	result, simErr := s.simulator.EstimateUserOperationGas(ctx, userOp, overrides)
	if simErr != nil {
		return nil, &RPCError{
			Code:    simErr.Code,
			Message: simErr.Message,
		}
	}

	return result, nil
}

// eth_supportedEntryPoints
func (s *Server) handleSupportedEntryPoints(_ context.Context) (any, error) {
	return s.entryPoints, nil
}

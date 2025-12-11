package simulator

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	entrypoint "github.com/chrishunter/1dler/bindings/v06"
	ep "github.com/chrishunter/1dler/internal/entrypoint"
	"github.com/chrishunter/1dler/internal/errors"
	"github.com/chrishunter/1dler/internal/userop"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
)

//go:embed tracer/dist/validationTracerV0_6.js
var validationTracerV06 string

// EstimateUserOperationGasV06 estimates gas costs for a user operation
func (s *Simulator) estimateUserOperationGasV06(ctx context.Context, userOp *entrypoint.UserOperation, overrides *OverrideSet) (*EstimateUserOpGasResult, *SimulatorError) {
	if s.debug {
		log.Info("Estimating gas for UserOperation v0.6", "sender", userOp.Sender, "nonce", userOp.Nonce)
	}

	// Set max fee per gas to 1 for easy calculation of fees
	userOp.MaxFeePerGas = common.Big1
	userOp.PreVerificationGas = common.Big0
	userOp.VerificationGasLimit = big.NewInt(5000000)
	userOp.CallGasLimit = big.NewInt(15000000)

	// Use simulator for execution
	input, err := s.v06EntryPointABI.Pack("simulateHandleOp", userOp, common.Address{}, []byte{})
	if err != nil {
		log.Error("failed to pack simulateHandleOp", "error", err)
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("failed to pack simulateHandleOp: %v", err),
		}
	}

	// make call request
	callTx := map[string]any{
		"to":   ep.EntryPointV06,
		"data": hexutil.Encode(input),
	}

	// Initialize overrides if nil
	if overrides == nil {
		overrides = &OverrideSet{}
	}

	// Add sender balance override
	(*overrides)[userOp.Sender] = OverrideAccount{
		Balance: (*hexutil.Big)(DefaultSenderBalance),
	}

	err = s.ethClient.Client().CallContext(ctx, nil, "eth_call", callTx, "latest", overrides)

	if err == nil {
		log.Error("Simulation succeeded without expected revert")
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: "simulation succeeded without expected revert",
		}
	}

	execResult, code, err := s.decodeV06RevertError(err)
	if err != nil {
		return nil, &SimulatorError{
			Code:    code,
			Message: err.Error(),
		}
	}

	// Calculate gas fields
	vgl := new(big.Int).Mul(execResult.PreOpGas, s.vglMultiplierPercent)
	vgl = vgl.Div(vgl, big.NewInt(100))
	cgl := new(big.Int).Mul(execResult.Paid, s.cglMultiplierPercent)
	cgl = cgl.Div(cgl, big.NewInt(100))

	pvg, err := s.CalculatePVG(ctx, userop.PackV06(userOp))
	if err != nil {
		log.Error("failed to calculate PVG", "error", err)
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: err.Error(),
		}
	}

	// Bump PVG by 27% for fluctuation
	pvg.Mul(pvg, big.NewInt(127))
	pvg.Div(pvg, big.NewInt(100))

	result := EstimateUserOpGasResult{
		PreVerificationGas:   (*hexutil.Big)(pvg),
		VerificationGasLimit: (*hexutil.Big)(vgl),
		CallGasLimit:         (*hexutil.Big)(cgl),
	}

	if s.debug {
		log.Info("Gas estimation result", "result", result)
	}

	return &result, nil
}

// CallHandleOps simulates a handleOps call using eth_call to catch reverts
func (s *Simulator) callHandleOpsV06(ctx context.Context, userOp *entrypoint.UserOperation, sender common.Address, opts *bind.TransactOpts) *SimulatorError {
	txData, err := s.v06EntryPointABI.Pack("handleOps", []entrypoint.UserOperation{*userOp}, sender)
	if err != nil {
		return &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("failed to pack handleOps: %v", err),
		}
	}

	callTx := map[string]any{
		"from":                 sender,
		"to":                   ep.EntryPointV06,
		"data":                 hexutil.Encode(txData),
		"maxFeePerGas":         (*hexutil.Big)(opts.GasFeeCap),
		"maxPriorityFeePerGas": (*hexutil.Big)(opts.GasTipCap),
		"gas":                  hexutil.Uint64(opts.GasLimit),
	}

	overrides := &OverrideSet{
		sender: OverrideAccount{
			Balance: (*hexutil.Big)(DefaultSenderBalance),
		},
	}

	err = s.ethClient.Client().CallContext(ctx, nil, "eth_call", callTx, "latest", overrides)
	if err != nil {
		_, code, decodedErr := s.decodeV06RevertError(err)
		log.Error("Transaction simulation failed", "error", decodedErr, "baseError", err)
		return &SimulatorError{
			Code:    code,
			Message: decodedErr.Error(),
		}
	}

	return nil
}

// simulateValidation simulates the validation of a user operation
func (s *Simulator) simulateValidationV06(ctx context.Context, userOp *entrypoint.UserOperation, overrides *OverrideSet) (*ValidationResult, int, error) {
	input, err := s.v06EntryPointABI.Pack("simulateValidation", userOp)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to pack simulateValidation: %w", err)
	}

	callTx := map[string]any{
		"to":   ep.EntryPointV06,
		"data": hexutil.Encode(input),
	}

	err = s.ethClient.Client().CallContext(ctx, nil, "eth_call", callTx, "latest", overrides)
	if err == nil {
		return nil, errors.ErrorCodeInternal, fmt.Errorf("validation succeeded without expected revert")
	}

	// Check if it's a revert error
	if revertErr, ok := err.(interface{ ErrorData() any }); ok {
		data := fmt.Sprintf("%v", revertErr.ErrorData())
		return s.decodeV06SimulateValidation(data)
	}

	log.Error("validation failed without revert error", "error", err)
	return nil, errors.ErrorCodeInternal, fmt.Errorf("validation failed: %w", err)
}

// simulateValidation simulates the validation of a user operation
func (s *Simulator) traceSimulateValidationV06(ctx context.Context, userOp *entrypoint.UserOperation, overrides *OverrideSet) (*ValidationResult, int, error) {
	input, err := s.v06EntryPointABI.Pack("simulateValidation", userOp)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to pack simulateValidation: %w", err)
	}

	callTx := map[string]any{
		"to":   ep.EntryPointV06,
		"data": hexutil.Encode(input),
	}

	traceOpts := map[string]any{
		"tracer":         validationTracerV06,
		"stateOverrides": overrides,
	}

	var tracerOutput TracerOutput
	err = s.ethClient.Client().CallContext(ctx, &tracerOutput, "debug_traceCall", callTx, "latest", traceOpts)
	if err != nil {
		log.Error("failed to trace validation", "error", err)
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to trace")
	}

	// get validation result
	if tracerOutput.RevertData == nil || len(*tracerOutput.RevertData) < 10 {
		log.Error("expected revert data in validation", "data", tracerOutput.RevertData)
		return nil, errors.ErrorCodeInternal, fmt.Errorf("missing validation data")
	}

	validationResult, code, err := s.decodeV06SimulateValidation(*tracerOutput.RevertData)
	if err != nil {
		return nil, code, err
	}

	// Build entity infos
	entityInfos := map[EntityType]EntityInfo{
		EntityTypeAccount: {
			Entity: Entity{
				Address: userOp.Sender,
				Kind:    EntityTypeAccount,
			},
			IsStaked: validationResult.SenderInfo.Stake.Cmp(big.NewInt(0)) > 0,
		},
	}

	if len(userOp.InitCode) > 0 {
		entityInfos[EntityTypeFactory] = EntityInfo{
			Entity: Entity{
				Address: common.BytesToAddress(userOp.InitCode),
				Kind:    EntityTypeFactory,
			},
			IsStaked: validationResult.FactoryInfo.Stake.Cmp(big.NewInt(0)) > 0,
		}
	}

	if len(userOp.PaymasterAndData) > 0 {
		entityInfos[EntityTypePaymaster] = EntityInfo{
			Entity: Entity{
				Address: common.BytesToAddress(userOp.PaymasterAndData),
				Kind:    EntityTypePaymaster,
			},
			IsStaked: validationResult.PaymasterInfo.Stake.Cmp(big.NewInt(0)) > 0,
		}
	}

	violations, err := s.check7562Violations(&ValidationContext{
		TracerOut:         tracerOutput,
		SenderAddress:     userOp.Sender,
		EntryPointAddress: ep.EntryPointV06,
		EntityInfos:       entityInfos,
		HasFactory:        len(userOp.InitCode) > 0,
		AccessedAddresses: make(map[common.Address]bool),
	})

	if err != nil {
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to check context violations: %w", err)
	}

	if len(violations) > 0 {
		log.Warn("context violations", "violations", violations)
		return nil, errors.ErrorCodeBannedOpcode, fmt.Errorf("validation violation: %v", violations)
	}

	return validationResult, 0, nil
}

// Decode validation errors
func (s *Simulator) decodeV06SimulateValidation(data string) (*ValidationResult, int, error) {
	if len(data) < 10 {
		log.Error("data is too short to be a validation result", "data", data)
		return nil, errors.ErrorCodeInternal, fmt.Errorf("error decoding validation result")
	}

	selector := data[0:10]
	if selector == "0xe0cff05f" {
		// ValidationResult error (0xe0cff05f)
		revert := s.v06EntryPointABI.Errors["ValidationResult"]
		rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
		if err != nil {
			log.Error("Failed to unpack ValidationResult data", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack validation result: %w, data: %s", err, data)
		}

		// Convert to our result type
		values := rawResult.([]any)

		// First convert to JSON to handle the struct tag differences
		jsonBytes, err := json.Marshal(values[0])
		if err != nil {
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal return info: %w", err)
		}
		var returnInfo struct {
			PreOpGas         *big.Int `json:"preOpGas"`
			Prefund          *big.Int `json:"prefund"`
			SigFailed        bool     `json:"sigFailed"`
			ValidAfter       *big.Int `json:"validAfter"`
			ValidUntil       *big.Int `json:"validUntil"`
			PaymasterContext []byte   `json:"paymasterContext"`
		}
		if err := json.Unmarshal(jsonBytes, &returnInfo); err != nil {
			log.Error("failed to unmarshal return info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal return info: %w", err)
		}

		// Convert stake info structs
		jsonBytes, err = json.Marshal(values[1])
		if err != nil {
			log.Error("failed to marshal sender info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal sender info: %w", err)
		}
		var senderInfo StakeInfo
		if err := json.Unmarshal(jsonBytes, &senderInfo); err != nil {
			log.Error("failed to unmarshal sender info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal sender info: %w", err)
		}

		jsonBytes, err = json.Marshal(values[2])
		if err != nil {
			log.Error("failed to marshal factory info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal factory info: %w", err)
		}
		var factoryInfo StakeInfo
		if err := json.Unmarshal(jsonBytes, &factoryInfo); err != nil {
			log.Error("failed to unmarshal factory info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal factory info: %w", err)
		}

		jsonBytes, err = json.Marshal(values[3])
		if err != nil {
			log.Error("failed to marshal paymaster info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal paymaster info: %w", err)
		}
		var paymasterInfo StakeInfo
		if err := json.Unmarshal(jsonBytes, &paymasterInfo); err != nil {
			log.Error("failed to unmarshal paymaster info", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal paymaster info: %w", err)
		}

		result := &ValidationResult{
			ReturnInfo: ReturnInfo{
				PreOpGas:         returnInfo.PreOpGas,
				Prefund:          returnInfo.Prefund,
				SigFailed:        returnInfo.SigFailed,
				ValidAfter:       returnInfo.ValidAfter,
				ValidUntil:       returnInfo.ValidUntil,
				PaymasterContext: returnInfo.PaymasterContext,
			},
			SenderInfo:    senderInfo,
			FactoryInfo:   factoryInfo,
			PaymasterInfo: paymasterInfo,
		}

		return result, 0, nil
	} else if selector == "0x8b7ac980" {
		// ExecutionResult error (0x8b7ac980)
		log.Error("unexpected ExecutionResult error in validation", "data", data)
		return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected ExecutionResult error in validation")
	} else if selector == "0x220266b6" {
		// FailedOp error (0x220266b6)
		revert := s.v06EntryPointABI.Errors["FailedOp"]
		rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
		if err != nil {
			log.Error("Failed to unpack FailedOp data", "error", err)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack FailedOp result: %w", err)
		}

		values := rawResult.([]any)
		if len(values) != 2 {
			log.Error("unexpected number of values in FailedOp result", "values", values)
			return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected number of values in FailedOp result: got %d, want 2", len(values))
		}

		return nil, getErrorCode(values[1].(string)), fmt.Errorf(values[1].(string))
	} else if strings.HasPrefix(data, "0x08c379a0") {
		// Error(string) - 0x08c379a0
		stringData := common.Hex2Bytes(data[10:])
		if len(stringData) >= 96 {
			// Skip first 32 bytes (offset), then 32 bytes (length)
			length := new(big.Int).SetBytes(stringData[32:64]).Uint64()
			if length > 0 && length <= uint64(len(stringData)-64) {
				revertMsg := string(stringData[64 : 64+length])
				log.Error("validation reverted", "revertMsg", revertMsg)
				return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("validation reverted: %s", revertMsg)
			}
		}
		return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("reverted with data: %s", data)
	}

	return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("reverted with data: %s", data)
}

// decodeRevertError attempts to decode an Ethereum revert error
// based on known selectors and formats
func (s *Simulator) decodeV06RevertError(err error) (*V06ExecutionResult, int, error) {
	if revertErr, ok := err.(interface{ ErrorData() any }); ok {
		// Extract revert data
		data := fmt.Sprintf("%v", revertErr.ErrorData())

		// Check if we have enough data and a selector
		if len(data) >= 10 {
			selector := data[0:10]

			if selector == "0x8b7ac980" {
				// ExecutionResult error (0x8b7ac980)
				revert := s.v06EntryPointABI.Errors["ExecutionResult"]
				rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
				if err != nil {
					log.Error("Failed to unpack revert data", "error", err)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack execution result: %v", err)
				}

				// Convert to our result type
				values := rawResult.([]any)
				if len(values) != 6 {
					log.Error("unexpected number of values in execution result", "values", values)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected number of values in execution result: got %d, want 6", len(values))
				}

				return &V06ExecutionResult{
					PreOpGas:      values[0].(*big.Int),
					Paid:          values[1].(*big.Int),
					ValidAfter:    values[2].(*big.Int),
					ValidUntil:    values[3].(*big.Int),
					TargetSuccess: values[4].(bool),
					TargetResult:  values[5].([]byte),
				}, 0, nil
			} else if selector == "0x220266b6" {
				// FailedOp error (0x220266b6)
				revert := s.v06EntryPointABI.Errors["FailedOp"]
				rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
				if err != nil {
					log.Error("Failed to unpack FailedOp data", "error", err)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack FailedOp result: %v", err)
				}

				values := rawResult.([]interface{})
				if len(values) != 2 {
					log.Error("unexpected number of values in FailedOp result", "values", values)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected number of values in FailedOp result: got %d, want 2", len(values))
				}

				return nil, getErrorCode(values[1].(string)), fmt.Errorf(values[1].(string))
			} else if strings.HasPrefix(data, "0x08c379a0") {
				// Error(string) - 0x08c379a0
				stringData := common.Hex2Bytes(data[10:])
				if len(stringData) >= 96 {
					// Skip first 32 bytes (offset), then 32 bytes (length)
					length := new(big.Int).SetBytes(stringData[32:64]).Uint64()
					if length > 0 && length <= uint64(len(stringData)-64) {
						revertMsg := string(stringData[64 : 64+length])
						return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("transaction reverted: %s", revertMsg)
					}
				}
				return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("transaction reverted: %s", data)
			}

			// Unknown selector but we have data
			return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("transaction reverted with data: %s", data)
		}
	}

	// Not a revert error
	return nil, errors.ErrorCodeInternal, err
}

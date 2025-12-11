package simulator

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	entrypointv07 "github.com/chrishunter/1dler/bindings/v07"
	ep "github.com/chrishunter/1dler/internal/entrypoint"
	"github.com/chrishunter/1dler/internal/errors"
	"github.com/chrishunter/1dler/internal/userop"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
)

//go:embed tracer/dist/validationTracerV0_7.js
var validationTracerV07 string

// EstimateUserOperationGasV07 estimates gas costs for a v0.7 user operation
func (s *Simulator) estimateUserOperationGasV07(ctx context.Context, userOp *entrypointv07.PackedUserOperation, overrides *OverrideSet) (*EstimateUserOpGasResult, *SimulatorError) {
	log.Debug("Estimating gas for UserOperation v0.7")

	// Set default gas limits if not provided
	if userOp.AccountGasLimits == [32]byte{} {
		userOp.AccountGasLimits = packBytes(
			DefaultVerificationGasLimit,
			DefaultCallGasLimit,
		)
	}

	// Set default gas fees if not provided
	userOp.GasFees = packBytes(
		DefaultMaxPriorityFee,
		DefaultMaxFee,
	)

	userOp.PreVerificationGas = common.Big0

	// Replace verification gas limit with default value if paymaster exists
	if len(userOp.PaymasterAndData) > 0 {
		copy(userOp.PaymasterAndData[20:36], common.LeftPadBytes(DefaultVerificationGasLimit.Bytes(), 16))
	}

	// Pack the user operation into calldata
	simulateCalldata, err := s.v07EntryPointABI.Pack("simulateHandleOp", userOp, common.Address{}, []byte{})
	if err != nil {
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("failed to pack simulateHandleOp calldata: %v", err),
		}
	}

	// Initialize overrides if nil
	if overrides == nil {
		overrides = &OverrideSet{}
	}

	// Add simulation bytecode override
	(*overrides)[ep.EntryPointV07] = OverrideAccount{
		Code: s.v07SimulationsBytecode,
	}

	// Add sender balance override
	(*overrides)[userOp.Sender] = OverrideAccount{
		Balance: (*hexutil.Big)(DefaultSenderBalance),
	}

	// Make the call
	callTx := map[string]any{
		"to":   ep.EntryPointV07,
		"data": hexutil.Encode(simulateCalldata),
	}

	var res string
	err = s.ethClient.Client().CallContext(ctx, &res, "eth_call", callTx, "latest", overrides)
	execResult, code, err := s.decodeV07SimulateHandleOp(res, err)
	if err != nil {
		return nil, &SimulatorError{
			Code:    code,
			Message: fmt.Sprintf("simulation failed: %v", err),
		}
	}

	// Calculate PVG based on the configured mode
	pvg, err := s.CalculatePVG(ctx, userop.PackV07(userOp))
	if err != nil {
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("failed to calculate preverification gas: %v", err),
		}
	}

	// Calculate gas limits with multipliers
	vgl := new(big.Int).Mul(execResult.PreOpGas, s.vglMultiplierPercent)
	vgl = vgl.Div(vgl, big.NewInt(100))

	// todo: Due to 10% fee CallGas = (Paid - PreOpGas - CallGasLimit * .1)/0.9 but need to account for high end of range
	cgl := new(big.Int).Mul(execResult.Paid, s.cglMultiplierPercent)
	cgl = cgl.Div(cgl, big.NewInt(100))

	// paymaster verification gas used for simulation
	paymasterVerificationGasLimit := DefaultVerificationGasLimit

	// Bump PVG by 27% for fluctuation
	pvg.Mul(pvg, big.NewInt(127))
	pvg.Div(pvg, big.NewInt(100))

	result := EstimateUserOpGasResult{
		PreVerificationGas:            (*hexutil.Big)(pvg),
		VerificationGasLimit:          (*hexutil.Big)(vgl),
		CallGasLimit:                  (*hexutil.Big)(cgl),
		PaymasterVerificationGasLimit: (*hexutil.Big)(paymasterVerificationGasLimit),
	}

	if s.debug {
		log.Info("Gas estimation result", "result", result)
	}

	return &result, nil
}

// CallHandleOpsV07 simulates a handleOps call using eth_call to catch reverts
func (s *Simulator) callHandleOpsV07(ctx context.Context, userOp *entrypointv07.PackedUserOperation, sender common.Address, opts *bind.TransactOpts) *SimulatorError {
	// Get the packed data from a no-send transaction
	txData, err := s.v07EntryPointABI.Pack("handleOps", []entrypointv07.PackedUserOperation{*userOp}, sender)
	if err != nil {
		return &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: fmt.Sprintf("failed to pack handleOps: %v", err),
		}
	}

	callTx := map[string]any{
		"from":                 sender,
		"to":                   ep.EntryPointV07,
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
		_, code, decodedErr := s.decodeV07SimulateHandleOp("", err)
		log.Error("Transaction simulation failed", "error", decodedErr, "baseError", err)
		return &SimulatorError{
			Code:    code,
			Message: decodedErr.Error(),
		}
	}

	return nil
}

func (s *Simulator) decodeV07SimulateHandleOp(res string, err error) (*entrypointv07.IEntryPointSimulationsExecutionResult, int, error) {
	// decode executionResult
	if len(res) > 0 && err == nil {
		bytes := common.Hex2Bytes(res[2:])
		method := s.v07EntryPointABI.Methods["simulateHandleOp"]
		rawResult, err := method.Outputs.Unpack(bytes)
		if err != nil {
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack execution result from string: %w", err)
		}

		raw := rawResult[0]
		rawVal := reflect.ValueOf(raw)

		if rawVal.Kind() != reflect.Struct {
			return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected kind: %s", rawVal.Kind())
		}

		result := &entrypointv07.IEntryPointSimulationsExecutionResult{
			PreOpGas:                rawVal.Field(0).Interface().(*big.Int),
			Paid:                    rawVal.Field(1).Interface().(*big.Int),
			AccountValidationData:   rawVal.Field(2).Interface().(*big.Int),
			PaymasterValidationData: rawVal.Field(3).Interface().(*big.Int),
			TargetSuccess:           rawVal.Field(4).Bool(),
			TargetResult:            rawVal.Field(5).Bytes(),
		}

		return result, 0, nil
	}

	if revertErr, ok := err.(interface{ ErrorData() any }); ok {
		// Extract revert data
		data := fmt.Sprintf("%v", revertErr.ErrorData())

		// Check if we have enough data and a selector
		if len(data) >= 10 {
			selector := data[0:10]
			if selector == "0x220266b6" {
				// FailedOp error (0x220266b6)
				revert := s.v07EntryPointABI.Errors["FailedOp"]
				rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
				if err != nil {
					log.Error("Failed to unpack FailedOp data", "error", err)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack FailedOp result: %w", err)
				}

				values := rawResult.([]any)
				if len(values) != 2 {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected number of values in FailedOp result: got %d, want 2", len(values))
				}

				return nil, getErrorCode(values[1].(string)), fmt.Errorf(values[1].(string))
			} else if selector == "0x65c8fd4d" {
				// FailedOpWithRevert
				revert := s.v07EntryPointABI.Errors["FailedOpWithRevert"]
				rawResult, err := revert.Unpack(common.Hex2Bytes(data[2:]))
				if err != nil {
					log.Error("Failed to unpack FailedOpWithRevert data", "error", err)
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack FailedOpWithRevert result: %w", err)
				}

				values := rawResult.([]any)
				if len(values) != 3 {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected number of values in FailedOpWithRevert result: got %d, want 3", len(values))
				}

				return nil, getErrorCode(values[1].(string)), fmt.Errorf(values[1].(string) + " - " + hexutil.Encode(values[2].([]byte)))
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
				return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("execution reverted without decodable data: %s", stringData)
			}

			// Unknown selector but we have data
			return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("transaction reverted with data: %s", data)
		}
	}

	// Not a revert error
	return nil, errors.ErrorCodeInternal, err
}

// simulateValidation simulates the validation of a user operation
func (s *Simulator) simulateValidationV07(ctx context.Context, userOp *entrypointv07.PackedUserOperation, overrides *OverrideSet) (*ValidationResult, int, error) {
	input, err := s.v07EntryPointABI.Pack("simulateValidation", userOp)
	if err != nil {
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to pack simulateValidation: %w", err)
	}

	callTx := map[string]any{
		"to":   ep.EntryPointV07,
		"data": hexutil.Encode(input),
	}

	// Initialize overrides if nil
	if overrides == nil {
		overrides = &OverrideSet{}
	}

	// Add simulation bytecode override
	(*overrides)[ep.EntryPointV07] = OverrideAccount{
		Code: s.v07SimulationsBytecode,
	}

	var res string
	err = s.ethClient.Client().CallContext(ctx, &res, "eth_call", callTx, "latest", overrides)

	if len(res) > 0 && err == nil {
		bytes := common.Hex2Bytes(res[2:])
		method := s.v07EntryPointABI.Methods["simulateValidation"]
		rawResult, err := method.Outputs.Unpack(bytes)
		if err != nil {
			return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unpack validation result from string: %v", err)
		}

		raw := rawResult[0]
		rawVal := reflect.ValueOf(raw)

		if rawVal.Kind() != reflect.Struct {
			return nil, errors.ErrorCodeInternal, fmt.Errorf("unexpected kind: %s", rawVal.Kind())
		}

		// v07Result := &entrypointv07.IEntryPointSimulationsValidationResult{
		// 	ReturnInfo:    rawVal.Field(0).Interface().(entrypointv07.IEntryPointReturnInfo),
		// 	SenderInfo:    rawVal.Field(1).Interface().(entrypointv07.IStakeManagerStakeInfo),
		// 	FactoryInfo:   rawVal.Field(2).Interface().(entrypointv07.IStakeManagerStakeInfo),
		// 	PaymasterInfo: rawVal.Field(3).Interface().(entrypointv07.IStakeManagerStakeInfo),
		// 	// AggregatorInfo: rawVal.Field(4).Interface().(entrypointv07.IEntryPointAggregatorStakeInfo),
		// }

		sigFailed, validAfter, validUntil := parseValidationData(rawVal.Field(0).Field(2).Interface().(*big.Int))
		paymasterSigFailed, paymasterValidAfter, paymasterValidUntil := parseValidationData(rawVal.Field(0).Field(3).Interface().(*big.Int))

		if validAfter != 0 || paymasterValidAfter != 0 {
			validAfter = max(validAfter, paymasterValidAfter)
		}

		if validUntil == 0 || paymasterValidUntil == 0 {
			validUntil = min(validUntil, paymasterValidUntil)
		}

		result := &ValidationResult{
			ReturnInfo: ReturnInfo{
				PreOpGas:         rawVal.Field(0).Field(0).Interface().(*big.Int),
				Prefund:          rawVal.Field(0).Field(1).Interface().(*big.Int),
				SigFailed:        sigFailed || paymasterSigFailed,
				ValidAfter:       big.NewInt(int64(validAfter)),
				ValidUntil:       big.NewInt(int64(validUntil)),
				PaymasterContext: rawVal.Field(0).Field(4).Interface().([]byte),
			},
			// SenderInfo:    StakeInfo(rawVal.Field(1).Interface().(entrypointv07.IStakeManagerStakeInfo)),
			// FactoryInfo:   StakeInfo(rawVal.Field(2).Interface().(entrypointv07.IStakeManagerStakeInfo)),
			// PaymasterInfo: StakeInfo(rawVal.Field(3).Interface().(entrypointv07.IStakeManagerStakeInfo)),
		}

		return result, 0, nil
	}

	// Check if it's a revert error
	if revertErr, ok := err.(interface{ ErrorData() any }); ok {
		// Extract revert data
		data := fmt.Sprintf("%v", revertErr.ErrorData())

		// Check if we have enough data and a selector
		if len(data) >= 10 {
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
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal return info: %w", err)
				}

				// Convert stake info structs
				jsonBytes, err = json.Marshal(values[1])
				if err != nil {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal sender info: %w", err)
				}
				var senderInfo StakeInfo
				if err := json.Unmarshal(jsonBytes, &senderInfo); err != nil {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal sender info: %w", err)
				}

				jsonBytes, err = json.Marshal(values[2])
				if err != nil {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal factory info: %w", err)
				}
				var factoryInfo StakeInfo
				if err := json.Unmarshal(jsonBytes, &factoryInfo); err != nil {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to unmarshal factory info: %w", err)
				}

				jsonBytes, err = json.Marshal(values[3])
				if err != nil {
					return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to marshal paymaster info: %w", err)
				}
				var paymasterInfo StakeInfo
				if err := json.Unmarshal(jsonBytes, &paymasterInfo); err != nil {
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
						return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("validation reverted: %s", revertMsg)
					}
				}
				return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("failed to decode revert string: %s", data)
			}
		}
		return nil, errors.ErrorCodeExecutionReverted, fmt.Errorf("reverted with data: %s", data)
	}

	return nil, errors.ErrorCodeInternal, fmt.Errorf("validation failed: %w", err)
}

// simulateValidation simulates the validation of a user operation - todo finish implementing this
func (s *Simulator) traceSimulateValidationV07(ctx context.Context, userOp *entrypointv07.PackedUserOperation, overrides *OverrideSet) (*ValidationResult, int, error) {
	input, err := s.v07EntryPointABI.Pack("simulateValidation", userOp)
	if err != nil {
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to pack simulateValidation: %w", err)
	}

	callTx := map[string]any{
		"to":   ep.EntryPointV07,
		"data": hexutil.Encode(input),
	}

	// Initialize overrides if nil
	if overrides == nil {
		overrides = &OverrideSet{}
	}

	// Add simulation bytecode override
	(*overrides)[ep.EntryPointV07] = OverrideAccount{
		Code: s.v07SimulationsBytecode,
	}

	traceOpts := map[string]any{
		"tracer":         validationTracerV07,
		"stateOverrides": overrides,
	}

	var bundlerTracerResult BundlerTracerResult
	err = s.ethClient.Client().CallContext(ctx, &bundlerTracerResult, "debug_traceCall", callTx, "latest", traceOpts)
	if err != nil {
		log.Error("failed to trace validation", "error", err)
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to trace")
	}

	_, err = parseTracerOut(userOp, &bundlerTracerResult)
	if err != nil {
		return nil, errors.ErrorCodeInternal, fmt.Errorf("failed to parse tracer output: %w", err)
	}
	return nil, errors.ErrorCodeInternal, fmt.Errorf("not implemented")
}

// parseTracerOut converts the BundlerTracerResult into our internal TracerOutput format
func parseTracerOut(op *entrypointv07.PackedUserOperation, tracerOut *BundlerTracerResult) (*TracerOutput, error) {
	// Initialize phases array with default values
	phases := make([]Phase, 3)
	factoryCalledCreate2Twice := false

	// Check factory phase
	for _, call := range tracerOut.CallsFromEntryPoint {
		if call.TopLevelMethodSig == CREATE_SENDER_METHOD {
			phase, err := parseCallToPhase(call, EntityTypeFactory)
			if err != nil {
				return nil, fmt.Errorf("failed to parse factory phase: %w", err)
			}
			phases[0] = phase

			// [OP-031] - create call can only be called once
			if count, exists := call.Opcodes["CREATE2"]; exists && count > 1 {
				factoryCalledCreate2Twice = true
			}
			break
		}
	}

	// Check account phase
	for _, call := range tracerOut.CallsFromEntryPoint {
		if call.TopLevelMethodSig == VALIDATE_USER_OP_METHOD {
			phase, err := parseCallToPhase(call, EntityTypeAccount)
			if err != nil {
				return nil, fmt.Errorf("failed to parse account phase: %w", err)
			}
			phases[1] = phase
			break
		}
	}

	// Check paymaster phase
	for _, call := range tracerOut.CallsFromEntryPoint {
		if call.TopLevelMethodSig == VALIDATE_PAYMASTER_USER_OP_METHOD {
			phase, err := parseCallToPhase(call, EntityTypePaymaster)
			if err != nil {
				return nil, fmt.Errorf("failed to parse paymaster phase: %w", err)
			}
			phases[2] = phase
			break
		}
	}

	// Collect accessed contracts
	accessedContracts := make(map[common.Address]ContractInfo)
	for _, call := range tracerOut.CallsFromEntryPoint {
		for addrStr, info := range call.ContractInfo {
			addr := common.HexToAddress(addrStr)
			accessedContracts[addr] = ContractInfo{
				Header: info.Header,
				Opcode: info.Opcode,
				Length: uint64(info.Length),
			}
		}
	}

	// Process associated slots
	associatedSlots := AssociatedSlotsByAddress{
		Slots: make(map[string][]string),
	}

	// Helper function to format address for keccak check
	formatAddr := func(addr common.Address) string {
		return fmt.Sprintf("0x000000000000000000000000%x", addr)
	}

	// Get relevant addresses
	factory := common.BytesToAddress(op.InitCode)
	paymaster := common.BytesToAddress(op.PaymasterAndData)
	sender := op.Sender

	// Process keccak values for associated slots
	for _, k := range tracerOut.Keccak {
		if factory != (common.Address{}) {
			if err := checkAssociatedSlot(formatAddr(factory), factory, k, &associatedSlots); err != nil {
				return nil, err
			}
		}
		if paymaster != (common.Address{}) {
			if err := checkAssociatedSlot(formatAddr(paymaster), paymaster, k, &associatedSlots); err != nil {
				return nil, err
			}
		}
		if err := checkAssociatedSlot(formatAddr(sender), sender, k, &associatedSlots); err != nil {
			return nil, err
		}
	}

	// Convert expected storage format
	expectedStorage := ExpectedStorage{
		Storage: make(map[common.Address]map[string]*big.Int),
	}
	for addrStr, slots := range tracerOut.ExpectedStorage {
		addr := common.HexToAddress(addrStr)
		expectedStorage.Storage[addr] = make(map[string]*big.Int)
		for slot, value := range slots {
			val, ok := new(big.Int).SetString(strings.TrimPrefix(value, "0x"), 16)
			if !ok {
				return nil, fmt.Errorf("failed to parse storage value: %s", value)
			}
			expectedStorage.Storage[addr][slot] = val
		}
	}

	return &TracerOutput{
		Phases:                    phases,
		RevertData:                nil, // This comes from a different source in the original code
		AccessedContracts:         accessedContracts,
		AssociatedSlotsByAddress:  associatedSlots,
		FactoryCalledCreate2Twice: factoryCalledCreate2Twice,
		ExpectedStorage:           expectedStorage,
	}, nil
}

// parseCallToPhase converts a TopLevelCallInfo into a Phase
func parseCallToPhase(call TopLevelCallInfo, entityType EntityType) (Phase, error) {
	phase := Phase{
		StorageAccesses:            make(map[common.Address]AccessInfo),
		ExtCodeAccessInfo:          make(map[common.Address]string),
		UndeployedContractAccesses: make([]common.Address, 0),
	}

	// Convert storage accesses
	for addrStr, access := range call.Access {
		addr := common.HexToAddress(addrStr)
		phase.StorageAccesses[addr] = AccessInfo{
			Reads:  access.Reads,
			Writes: make(map[string]uint64),
		}
		for slot, count := range access.Writes {
			phase.StorageAccesses[addr].Writes[slot] = uint64(count)
		}
	}

	// Convert ext code access info
	for addrStr, opcode := range call.ExtCodeAccessInfo {
		addr := common.HexToAddress(addrStr)
		phase.ExtCodeAccessInfo[addr] = opcode
	}

	// Set OOG flag
	phase.RanOutOfGas = call.OOG

	return phase, nil
}

// checkAssociatedSlot checks if a keccak value represents an associated slot
func checkAssociatedSlot(addrHex string, addr common.Address, keccakValue string, slots *AssociatedSlotsByAddress) error {
	if strings.Contains(keccakValue, addrHex) {
		addrStr := addr.Hex()
		if slots.Slots[addrStr] == nil {
			slots.Slots[addrStr] = make([]string, 0)
		}
		slots.Slots[addrStr] = append(slots.Slots[addrStr], keccakValue)
	}
	return nil
}

func packBytes(firstValue, secondValue *big.Int) [32]byte {
	var result [32]byte
	firstBytes := firstValue.Bytes()
	secondBytes := secondValue.Bytes()
	firstStart := 16 - len(firstBytes)
	secondStart := 32 - len(secondBytes)
	copy(result[firstStart:16], firstBytes)
	copy(result[secondStart:32], secondBytes)
	return result
}

package userop

import (
	"encoding/json"
	"fmt"
	"math/big"

	v06 "github.com/chrishunter/1dler/bindings/v06"
	v07 "github.com/chrishunter/1dler/bindings/v07"

	ep "github.com/chrishunter/1dler/internal/entrypoint"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

var maxGasThreshold = big.NewInt(50000000) // 50M gas as a sanity limit

type UserOperationReceipt struct {
	UserOpHash    string `json:"userOpHash"`
	Sender        string `json:"sender"`
	Nonce         string `json:"nonce"`
	Success       bool   `json:"success"`
	ActualGasUsed string `json:"actualGasUsed"`
	ActualGasCost string `json:"actualGasCost"`
}

var (
	UserOpPrimitivesV06 = []abi.ArgumentMarshaling{
		{Name: "sender", InternalType: "Sender", Type: "address"},
		{Name: "nonce", InternalType: "Nonce", Type: "uint256"},
		{Name: "initCode", InternalType: "InitCode", Type: "bytes"},
		{Name: "callData", InternalType: "CallData", Type: "bytes"},
		{Name: "callGasLimit", InternalType: "CallGasLimit", Type: "uint256"},
		{Name: "verificationGasLimit", InternalType: "VerificationGasLimit", Type: "uint256"},
		{Name: "preVerificationGas", InternalType: "PreVerificationGas", Type: "uint256"},
		{Name: "maxFeePerGas", InternalType: "MaxFeePerGas", Type: "uint256"},
		{Name: "maxPriorityFeePerGas", InternalType: "MaxPriorityFeePerGas", Type: "uint256"},
		{Name: "paymasterAndData", InternalType: "PaymasterAndData", Type: "bytes"},
		{Name: "signature", InternalType: "Signature", Type: "bytes"},
	}

	UserOpPrimitivesForHashV07 = []abi.ArgumentMarshaling{
		{Name: "sender", InternalType: "Sender", Type: "address"},
		{Name: "nonce", InternalType: "Nonce", Type: "uint256"},
		{Name: "initCode", InternalType: "InitCode", Type: "bytes"},
		{Name: "callData", InternalType: "CallData", Type: "bytes"},
		{Name: "accountGasLimits", InternalType: "AccountGasLimits", Type: "bytes32"}, // Packed VGL, CGL
		{Name: "preVerificationGas", InternalType: "PreVerificationGas", Type: "uint256"},
		{Name: "gasFees", InternalType: "GasFees", Type: "bytes32"}, // Packed Fees
		{Name: "paymasterAndData", InternalType: "PaymasterAndData", Type: "bytes"},
	}

	UserOpPrimitivesV07 = []abi.ArgumentMarshaling{
		{Name: "sender", InternalType: "Sender", Type: "address"},
		{Name: "nonce", InternalType: "Nonce", Type: "uint256"},
		{Name: "initCode", InternalType: "InitCode", Type: "bytes"},
		{Name: "callData", InternalType: "CallData", Type: "bytes"},
		{Name: "accountGasLimits", InternalType: "AccountGasLimits", Type: "bytes32"}, // Packed VGL, CGL
		{Name: "preVerificationGas", InternalType: "PreVerificationGas", Type: "uint256"},
		{Name: "gasFees", InternalType: "GasFees", Type: "bytes32"}, // Packed Fees
		{Name: "paymasterAndData", InternalType: "PaymasterAndData", Type: "bytes"},
		{Name: "signature", InternalType: "Signature", Type: "bytes"},
	}
)

var (
	UserOpTypeV06, _        = abi.NewType("tuple", "op", UserOpPrimitivesV06)
	UserOpTypeV07, _        = abi.NewType("tuple", "op", UserOpPrimitivesV07)
	UserOpTypeForHashV07, _ = abi.NewType("tuple", "op", UserOpPrimitivesForHashV07)
)

// UserOperationJSON is used only for JSON parsing
type UserOperation struct {
	// Common fields
	Sender               common.Address `json:"sender"`
	Nonce                *big.Int       `json:"nonce"`
	CallData             []byte         `json:"callData"`
	CallGasLimit         *big.Int       `json:"callGasLimit"`
	VerificationGasLimit *big.Int       `json:"verificationGasLimit"`
	PreVerificationGas   *big.Int       `json:"preVerificationGas"`
	MaxFeePerGas         *big.Int       `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *big.Int       `json:"maxPriorityFeePerGas"`
	Signature            []byte         `json:"signature"`

	// V06 fields
	InitCode         []byte `json:"initCode"`
	PaymasterAndData []byte `json:"paymasterAndData"`

	// V07 fields
	Factory                       common.Address `json:"factory"`
	FactoryData                   []byte         `json:"factoryData"`
	Paymaster                     common.Address `json:"paymaster"`
	PaymasterData                 []byte         `json:"paymasterData"`
	PaymasterVerificationGasLimit *big.Int       `json:"paymasterVerificationGasLimit"`
	PaymasterPostOpGasLimit       *big.Int       `json:"paymasterPostOpGasLimit"`

	// Other data
	EntryPointAddress common.Address `json:"entryPointAddress"`
	ChainID           *big.Int       `json:"chainID"`
	V06               *v06.UserOperation
	V07               *v07.PackedUserOperation
}

// ParseUserOperation parses a UserOperation from raw JSON data based on entrypoint version
func ParseUserOperation(data any, entrypointAddress common.Address, chainID *big.Int) (*UserOperation, error) {
	// First parse into intermediate JSON structure
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user operation data: %w", err)
	}

	var opMap map[string]string
	if err := json.Unmarshal(jsonData, &opMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user operation: %w", err)
	}

	// Validate common required fields
	if opMap["sender"] == "" {
		return nil, fmt.Errorf("sender is required")
	}
	if opMap["nonce"] == "" {
		return nil, fmt.Errorf("nonce is required")
	}
	if opMap["callData"] == "" {
		return nil, fmt.Errorf("callData is required")
	}
	if opMap["signature"] == "" {
		return nil, fmt.Errorf("signature is required")
	}

	request := &UserOperation{
		Sender:                        common.HexToAddress(opMap["sender"]),
		Nonce:                         parseNumberOrDefault(opMap["nonce"], common.Big0),
		CallData:                      getHexBytesOrDefault(opMap["callData"], []byte{}),
		CallGasLimit:                  parseNumberOrDefault(opMap["callGasLimit"], common.Big0),
		VerificationGasLimit:          parseNumberOrDefault(opMap["verificationGasLimit"], common.Big0),
		PreVerificationGas:            parseNumberOrDefault(opMap["preVerificationGas"], common.Big0),
		MaxFeePerGas:                  parseNumberOrDefault(opMap["maxFeePerGas"], common.Big0),
		MaxPriorityFeePerGas:          parseNumberOrDefault(opMap["maxPriorityFeePerGas"], common.Big0),
		Signature:                     getHexBytesOrDefault(opMap["signature"], []byte{}),
		InitCode:                      getHexBytesOrDefault(opMap["initCode"], []byte{}),
		PaymasterAndData:              getHexBytesOrDefault(opMap["paymasterAndData"], []byte{}),
		Factory:                       common.HexToAddress(opMap["factory"]),
		FactoryData:                   getHexBytesOrDefault(opMap["factoryData"], []byte{}),
		Paymaster:                     common.HexToAddress(opMap["paymaster"]),
		PaymasterData:                 getHexBytesOrDefault(opMap["paymasterData"], []byte{}),
		PaymasterVerificationGasLimit: parseNumberOrDefault(opMap["paymasterVerificationGasLimit"], common.Big0),
		PaymasterPostOpGasLimit:       parseNumberOrDefault(opMap["paymasterPostOpGasLimit"], common.Big0),
		EntryPointAddress:             entrypointAddress,
		ChainID:                       chainID,
	}

	switch entrypointAddress {
	case ep.EntryPointV06:
		userOp, err := getV06UserOp(request)
		if err != nil {
			return nil, err
		}
		request.V06 = userOp
		return request, nil
	case ep.EntryPointV07:
		userOp, err := getV07UserOp(request)
		if err != nil {
			return nil, err
		}
		request.V07 = userOp
		return request, nil
	default:
		return nil, fmt.Errorf("unsupported entrypoint address: %s", entrypointAddress)
	}
}

func getV06UserOp(opMap *UserOperation) (*v06.UserOperation, error) {
	return &v06.UserOperation{
		Sender:               opMap.Sender,
		Nonce:                opMap.Nonce,
		InitCode:             opMap.InitCode,
		CallData:             opMap.CallData,
		CallGasLimit:         opMap.CallGasLimit,
		VerificationGasLimit: opMap.VerificationGasLimit,
		PreVerificationGas:   opMap.PreVerificationGas,
		MaxFeePerGas:         opMap.MaxFeePerGas,
		MaxPriorityFeePerGas: opMap.MaxPriorityFeePerGas,
		PaymasterAndData:     opMap.PaymasterAndData,
		Signature:            opMap.Signature,
	}, nil
}

func getV07UserOp(req *UserOperation) (*v07.PackedUserOperation, error) {
	// Convert strings to appropriate types with defaults
	callGasLimit := req.CallGasLimit
	verificationGasLimit := req.VerificationGasLimit
	preVerificationGas := req.PreVerificationGas
	maxFeePerGas := req.MaxFeePerGas
	maxPriorityFeePerGas := req.MaxPriorityFeePerGas
	nonce := req.Nonce
	callData := req.CallData
	signature := req.Signature
	sender := req.Sender

	// Process paymaster fields
	var paymasterAndData []byte
	if req.Paymaster != (common.Address{}) {
		paymasterAndData = append(paymasterAndData, req.Paymaster.Bytes()...)
		paymasterAndData = append(paymasterAndData, common.LeftPadBytes(req.PaymasterVerificationGasLimit.Bytes(), 16)...)
		paymasterAndData = append(paymasterAndData, common.LeftPadBytes(req.PaymasterPostOpGasLimit.Bytes(), 16)...)
		paymasterAndData = append(paymasterAndData, req.PaymasterData...)
	}

	// Process factory fields
	factory := req.Factory
	factoryData := req.FactoryData
	initCode := []byte{}
	if factory != (common.Address{}) {
		initCode = append(factory.Bytes(), factoryData...)
	}

	// v0.7 uses packed gas limits and fees
	verificationGasLimitBig := verificationGasLimit
	// Note: These are parsed but not used directly in the packed format
	// They are included in the gas limits bytes32
	_ = req.PaymasterVerificationGasLimit
	_ = req.PaymasterPostOpGasLimit

	// Pack the gas limits into bytes32
	accountGasLimits := [32]byte{}
	callGasLimitBytes := common.LeftPadBytes(callGasLimit.Bytes(), 16)
	verificationGasLimitBytes := common.LeftPadBytes(verificationGasLimitBig.Bytes(), 16)
	copy(accountGasLimits[0:16], verificationGasLimitBytes)
	copy(accountGasLimits[16:32], callGasLimitBytes)

	// Pack the gas fees into bytes32
	gasFees := [32]byte{}
	maxFeePerGasBytes := common.LeftPadBytes(maxFeePerGas.Bytes(), 16)
	maxPriorityFeePerGasBytes := common.LeftPadBytes(maxPriorityFeePerGas.Bytes(), 16)
	copy(gasFees[0:16], maxPriorityFeePerGasBytes)
	copy(gasFees[16:32], maxFeePerGasBytes)

	return &v07.PackedUserOperation{
		Sender:             sender,
		Nonce:              nonce,
		InitCode:           initCode,
		CallData:           callData,
		AccountGasLimits:   accountGasLimits,
		PreVerificationGas: preVerificationGas,
		GasFees:            gasFees,
		PaymasterAndData:   paymasterAndData,
		Signature:          signature,
	}, nil
}

func (op *UserOperation) GetTxSize() uint64 {
	if op.EntryPointAddress == ep.EntryPointV06 {
		// https://github.com/eth-infinitism/account-abstraction/blob/releases/v0.6/contracts/core/EntryPoint.sol#L236
		total := new(big.Int).Add(op.CallGasLimit, new(big.Int).Mul(op.VerificationGasLimit, big.NewInt(2)))
		total = total.Add(total, big.NewInt(20000))

		if total.Cmp(maxGasThreshold) > 0 {
			return maxGasThreshold.Uint64()
		}

		return total.Uint64()
	}

	// https://github.com/eth-infinitism/account-abstraction/blob/releases/v0.7/contracts/core/EntryPoint.sol#L236
	total := new(big.Int).Add(op.CallGasLimit, op.VerificationGasLimit)
	total = total.Add(total, op.PaymasterPostOpGasLimit)
	total = total.Add(total, big.NewInt(20000))

	if total.Cmp(maxGasThreshold) > 0 {
		return maxGasThreshold.Uint64()
	}

	return total.Uint64()
}

package userop

import (
	v06 "github.com/chrishunter/1dler/bindings/v06"
	v07 "github.com/chrishunter/1dler/bindings/v07"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// GetUserOpHash calculates the hash of a UserOperation according to EIP-4337 specification
func (op *UserOperation) GetUserOpHash() string {
	var packed []byte
	if op.V06 != nil {
		packed = PackV06(op.V06)
	} else {
		packed = PackV07(op.V07)
	}

	opHash := crypto.Keccak256Hash(packed)

	// Pack the data for the final hash: opHash + entryPoint + chainID
	packed = []byte{}
	packed = append(packed, opHash.Bytes()...)
	packed = append(packed, common.LeftPadBytes(op.EntryPointAddress.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.ChainID.Bytes(), 32)...)

	// Return the keccak256 hash of the packed data
	return crypto.Keccak256Hash(packed).Hex()
}

func (op *UserOperation) Pack() []byte {
	if op.V06 != nil {
		return PackV06(op.V06)
	}

	return PackV07(op.V07)
}

func PackV06(op *v06.UserOperation) []byte {
	packed := []byte{}

	packed = append(packed, common.LeftPadBytes(op.Sender.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.Nonce.Bytes(), 32)...)

	initCodeHash := crypto.Keccak256Hash(op.InitCode)
	packed = append(packed, initCodeHash.Bytes()...)

	callDataHash := crypto.Keccak256Hash(op.CallData)
	packed = append(packed, callDataHash.Bytes()...)

	packed = append(packed, common.LeftPadBytes(op.CallGasLimit.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.VerificationGasLimit.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.PreVerificationGas.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.MaxFeePerGas.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.MaxPriorityFeePerGas.Bytes(), 32)...)

	paymasterAndDataHash := crypto.Keccak256Hash(op.PaymasterAndData)
	packed = append(packed, paymasterAndDataHash.Bytes()...)

	return packed
}

func PackV07(op *v07.PackedUserOperation) []byte {
	packed := []byte{}

	packed = append(packed, common.LeftPadBytes(op.Sender.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.Nonce.Bytes(), 32)...)

	initCodeHash := crypto.Keccak256Hash(op.InitCode)
	packed = append(packed, initCodeHash.Bytes()...)

	callDataHash := crypto.Keccak256Hash(op.CallData)
	packed = append(packed, callDataHash.Bytes()...)

	packed = append(packed, common.LeftPadBytes(op.AccountGasLimits[:], 32)...)
	packed = append(packed, common.LeftPadBytes(op.PreVerificationGas.Bytes(), 32)...)
	packed = append(packed, common.LeftPadBytes(op.GasFees[:], 32)...)

	paymasterAndDataHash := crypto.Keccak256Hash(op.PaymasterAndData)
	packed = append(packed, paymasterAndDataHash.Bytes()...)

	return packed
}

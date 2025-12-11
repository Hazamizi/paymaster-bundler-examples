package userop

import (
	"context"
	"fmt"

	v06 "github.com/chrishunter/1dler/bindings/v06"
	v07 "github.com/chrishunter/1dler/bindings/v07"
	ep "github.com/chrishunter/1dler/internal/entrypoint"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

func (op *UserOperation) Send(ctx context.Context, client *ethclient.Client, opts *bind.TransactOpts) (*types.Transaction, error) {
	var tx *types.Transaction
	if op.EntryPointAddress == ep.EntryPointV06 {
		entrypointContract, err := v06.NewEntrypoint(op.EntryPointAddress, client)
		if err != nil {
			log.Error("Failed to create entrypoint contract", "error", err)
			return nil, fmt.Errorf("failed to create entrypoint contract: %v", err)
		}
		tx, err = entrypointContract.HandleOps(opts, []v06.UserOperation{*op.V06}, opts.From)
		if err != nil {
			log.Error("Failed to send user operation transaction", "error", err)
			return nil, fmt.Errorf("failed to send transaction: %v", err)
		}
	} else {
		entrypointContract, err := v07.NewEntrypoint(op.EntryPointAddress, client)
		if err != nil {
			log.Error("Failed to create entrypoint contract", "error", err)
			return nil, fmt.Errorf("failed to create entrypoint contract: %v", err)
		}
		tx, err = entrypointContract.HandleOps(opts, []v07.PackedUserOperation{*op.V07}, opts.From)
		if err != nil {
			log.Error("Failed to send user operation transaction", "error", err)
			return nil, fmt.Errorf("failed to send transaction: %v", err)
		}
	}

	return tx, nil
}

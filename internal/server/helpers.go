package server

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

// getCurrentGasConditions gets the current base fee and priority fee from the network
func (s *Server) getCurrentGasConditions(ctx context.Context) (*big.Int, *big.Int, *big.Int, error) {
	var feePerGasHex, priorityFeeHex string
	feePerGasCall := &rpc.BatchElem{
		Method: "eth_gasPrice",
		Result: &feePerGasHex,
	}

	priorityFeeCall := &rpc.BatchElem{
		Method: "eth_maxPriorityFeePerGas",
		Result: &priorityFeeHex,
	}

	// Execute batch call
	err := s.ethClient.Client().BatchCallContext(ctx, []rpc.BatchElem{*feePerGasCall, *priorityFeeCall})
	if err != nil {
		log.Error("Batch call failed", "error", err)
		return nil, nil, nil, fmt.Errorf("failed to get network gas prices: %v", err)
	}

	// Check individual call errors
	if feePerGasCall.Error != nil || priorityFeeCall.Error != nil {
		log.Error("Failed to get suggested gas price", "error", feePerGasCall.Error)
		return nil, nil, nil, fmt.Errorf("failed to get network gas price: %v", feePerGasCall.Error)
	}

	// Convert hex strings to big.Int
	feePerGas, err := hexutil.DecodeBig(feePerGasHex)
	if err != nil {
		log.Error("Failed to decode base fee", "error", err, "hex", feePerGasHex)
		return nil, nil, nil, fmt.Errorf("failed to decode base fee: %v", err)
	}

	priorityFee, err := hexutil.DecodeBig(priorityFeeHex)
	if err != nil {
		log.Error("Failed to decode priority fee", "error", err, "hex", priorityFeeHex)
		return nil, nil, nil, fmt.Errorf("failed to decode priority fee: %v", err)
	}

	// Ensure we have non-zero gas prices from the network
	if feePerGas.Cmp(big.NewInt(0)) == 0 || priorityFee.Cmp(big.NewInt(0)) == 0 {
		log.Error("Received zero gas price from network", "feePerGas", feePerGas, "priorityFee", priorityFee)
		return nil, nil, nil, fmt.Errorf("received invalid zero gas price from network")
	}

	blockBaseFee := new(big.Int).Sub(feePerGas, priorityFee)

	return blockBaseFee, feePerGas, priorityFee, nil
}

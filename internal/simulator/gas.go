package simulator

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

// https://github.com/ethereum-optimism/optimism/blob/develop/packages/contracts-bedrock/src/L2/GasPriceOracle.sol
var (
	optimismGasPriceOracleAddress = common.HexToAddress("0x420000000000000000000000000000000000000F")
)

// CalcOptimismPVGWithEthClient uses Optimism's Gas Price Oracle precompile to get an estimate for
// preVerificationGas that takes into account the L1 gas cmponent.
func (s *Simulator) getOptimismPVG(ctx context.Context, packed []byte) (*big.Int, error) {
	packedData, err := s.optimismGasPriceOracleABI.Pack("getL1Fee", packed)
	if err != nil {
		return nil, fmt.Errorf("failed to pack data: %w", err)
	}

	var baseFeeHex string
	var l1FeeResult string

	batch := []rpc.BatchElem{
		{
			Method: "eth_gasPrice",
			Result: &baseFeeHex,
		},
		{
			Method: "eth_call",
			Args: []any{
				map[string]any{
					"to":   optimismGasPriceOracleAddress.Hex(),
					"data": hexutil.Encode(packedData),
				},
				"latest",
			},
			Result: &l1FeeResult,
		},
	}

	err = s.ethClient.Client().BatchCallContext(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("batch call failed: %w", err)
	}

	// Check for individual call errors
	for i, elem := range batch {
		if elem.Error != nil {
			return nil, fmt.Errorf("batch element %d failed: %w", i, elem.Error)
		}
	}

	// Parse base fee
	baseFee, ok := new(big.Int).SetString(baseFeeHex[2:], 16)
	if !ok {
		return nil, fmt.Errorf("failed to parse base fee hex: %s", baseFeeHex)
	}

	// Parse L1 fee response
	gas, err := s.optimismGasPriceOracleABI.Unpack("getL1Fee", common.FromHex(l1FeeResult))
	if err != nil {
		return nil, fmt.Errorf("failed to unpack data: %w", err)
	}

	gasInt, ok := gas[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("failed to cast gas to big.Int")
	}

	gasInt = gasInt.Div(gasInt, baseFee)

	return gasInt, nil
}

// CalculatePVG calculates the PreVerificationGas based on the configured mode
func (s *Simulator) CalculatePVG(ctx context.Context, packedOp []byte) (*big.Int, error) {
	switch s.pvgMode {
	case "static":
		return s.basePVG, nil
	case "dynamic":
		// todo need to add our standard bundler packed data to packed size as
		opPvg, err := s.getOptimismPVG(ctx, packedOp)
		if err != nil {
			log.Error("Failed to get Optimism PVG", "error", err)
			return nil, fmt.Errorf("failed to calculate PVG: %w", err)
		}

		sum := new(big.Int).Add(opPvg, s.basePVG)

		return sum, nil
	case "feedback":
		// Apply the feedback multiplier based on calldata cost
		s.pvgMutex.RLock()
		multiplier := s.pvgMultiplier // todo note this is not yet updated post op submit
		s.pvgMutex.RUnlock()

		opPvg, err := s.getOptimismPVG(ctx, packedOp)
		if err != nil {
			log.Error("Failed to get Optimism PVG", "error", err)
			return nil, fmt.Errorf("failed to calculate PVG: %w", err)
		}

		sum := new(big.Int).Add(opPvg, s.basePVG)

		// Convert multiplier to big.Int calculation (scaling by 1000 for precision)
		multiplierInt := new(big.Int).SetInt64(int64(multiplier * 1000))

		costComponent := new(big.Int).Mul(sum, multiplierInt)
		costComponent = costComponent.Div(costComponent, big.NewInt(1000))

		pvg := new(big.Int).Add(s.basePVG, costComponent)
		return pvg, nil

	default:
		log.Warn("Unknown PVG mode, using static", "mode", s.pvgMode)
		return s.basePVG, nil
	}
}

package simulator

import (
	"context"
	_ "embed" // Required for //go:embed
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	ep "github.com/chrishunter/1dler/internal/entrypoint"
	"github.com/chrishunter/1dler/internal/errors"

	entrypointv06 "github.com/chrishunter/1dler/bindings/v06"
	entrypointv07 "github.com/chrishunter/1dler/bindings/v07"
	"github.com/chrishunter/1dler/internal/userop"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

var (
	DefaultVerificationGasLimit = big.NewInt(5000000)
	DefaultCallGasLimit         = big.NewInt(15000000)
	DefaultPaymasterStake       = new(big.Int).Mul(big.NewInt(1), big.NewInt(1e17)) // 0.1 ETH
	DefaultMaxPriorityFee       = common.Big1
	DefaultMaxFee               = common.Big1
	DefaultSenderBalance        = new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18)) // 10 ETH
)

//go:embed optimism/gasPriceOracleAbi.json
var optimismGasPriceOracleAbiJSON string

//go:embed entrypoint/v07SimulationBytecode.txt
var v07EntryPointSimulationsBytecodeHex string

type Simulator struct {
	debug                     bool
	basePVG                   *big.Int
	pvgMode                   string
	pvgMultiplier             float64
	pvgMutex                  *sync.RWMutex
	cglMultiplierPercent      *big.Int
	vglMultiplierPercent      *big.Int
	v06EntryPointABI          abi.ABI
	v07EntryPointABI          abi.ABI
	optimismGasPriceOracleABI abi.ABI
	v07SimulationsBytecode    *hexutil.Bytes
	ethClient                 *ethclient.Client
}

func New(ethClient *ethclient.Client, entryPoints []common.Address, debug bool, basePVG *big.Int, pvgMode string, cglMultiplierPercent *big.Int, vglMultiplierPercent *big.Int) (*Simulator, error) {
	v06EntryPointABI, err := abi.JSON(strings.NewReader(entrypointv06.EntrypointABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse EntryPoint ABI: %w", err)
	}

	v07EntryPointABI, err := abi.JSON(strings.NewReader(entrypointv07.EntrypointABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse EntryPoint ABI: %w", err)
	}

	optimismGasPriceOracleABI, err := abi.JSON(strings.NewReader(optimismGasPriceOracleAbiJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse Optimism Gas Price Oracle ABI: %w", err)
	}

	v07SimulationsBytecode, err := hexutil.Decode(v07EntryPointSimulationsBytecodeHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode v0.7 simulations bytecode: %w", err)
	}

	v07SimulationsBytecodeBytes := hexutil.Bytes(v07SimulationsBytecode)

	return &Simulator{
		debug:                     debug,
		basePVG:                   basePVG,
		pvgMode:                   pvgMode,
		pvgMultiplier:             1.0,
		pvgMutex:                  &sync.RWMutex{},
		v06EntryPointABI:          v06EntryPointABI,
		v07EntryPointABI:          v07EntryPointABI,
		optimismGasPriceOracleABI: optimismGasPriceOracleABI,
		cglMultiplierPercent:      cglMultiplierPercent,
		vglMultiplierPercent:      vglMultiplierPercent,
		v07SimulationsBytecode:    &v07SimulationsBytecodeBytes,
		ethClient:                 ethClient,
	}, nil
}

// EstimateUserOperationGas estimates gas costs for a user operation based on the EntryPoint version
func (s *Simulator) EstimateUserOperationGas(ctx context.Context, userOp *userop.UserOperation, overrides *OverrideSet) (*EstimateUserOpGasResult, *SimulatorError) {
	switch userOp.EntryPointAddress {
	case ep.EntryPointV06:
		return s.estimateUserOperationGasV06(ctx, userOp.V06, overrides)
	case ep.EntryPointV07:
		return s.estimateUserOperationGasV07(ctx, userOp.V07, overrides)
	default:
		return nil, &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: "unsupported UserOperation entrypoint version",
		}
	}
}

// CallHandleOps simulates a handleOps call using eth_call to catch reverts
func (s *Simulator) CallHandleOps(ctx context.Context, userOp *userop.UserOperation, sender common.Address, opts *bind.TransactOpts) *SimulatorError {
	switch userOp.EntryPointAddress {
	case ep.EntryPointV06:
		return s.callHandleOpsV06(ctx, userOp.V06, sender, opts)
	case ep.EntryPointV07:
		return s.callHandleOpsV07(ctx, userOp.V07, sender, opts)
	default:
		return &SimulatorError{
			Code:    errors.ErrorCodeInternal,
			Message: "unsupported UserOperation type",
		}
	}
}

// CheckValidation checks if a user operation is valid
func (s *Simulator) CheckValidation(ctx context.Context, userOp *userop.UserOperation, overrides *OverrideSet) *SimulatorError {
	var validationResult *ValidationResult
	var code int
	var err error

	if userOp.EntryPointAddress == ep.EntryPointV06 {
		validationResult, code, err = s.traceSimulateValidationV06(ctx, userOp.V06, overrides)
	} else {
		validationResult, code, err = s.simulateValidationV07(ctx, userOp.V07, overrides)
	}

	if err != nil {
		log.Warn("failed validation", "error", err)
		return &SimulatorError{
			Code:    code,
			Message: fmt.Sprintf("failed validation: %v", err),
		}
	}

	if validationResult.ReturnInfo.SigFailed {
		return &SimulatorError{
			Code:    errors.ErrorCodeInvalidSignature,
			Message: "UserOperation would revert: AA24 invalid signature",
		}
	}

	// todo this stake value should be configurable
	if len(userOp.PaymasterAndData) > 0 && len(validationResult.ReturnInfo.PaymasterContext) > 0 && validationResult.PaymasterInfo.Stake.Cmp(DefaultPaymasterStake) < 0 {
		return &SimulatorError{
			Code:    errors.ErrorCodeInvalidEntityStake,
			Message: "Paymaster context not allowed on unstaked paymaster",
		}
	}

	timestampPlus10 := big.NewInt(time.Now().Unix() + 30)
	if validationResult.ReturnInfo.ValidUntil.Cmp(common.Big0) != 0 && validationResult.ReturnInfo.ValidUntil.Cmp(timestampPlus10) < 0 {
		return &SimulatorError{
			Code:    errors.ErrorCodeShortDeadline,
			Message: "UserOperation would revert: expired",
		}
	}

	now := big.NewInt(time.Now().Unix())
	if validationResult.ReturnInfo.ValidAfter.Cmp(common.Big0) != 0 && validationResult.ReturnInfo.ValidAfter.Cmp(now) > 0 {
		return &SimulatorError{
			Code:    errors.ErrorCodeShortDeadline,
			Message: "UserOperation would revert: not yet valid",
		}
	}
	return nil
}

package keys

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/google/uuid"

	"github.com/chrishunter/1dler/internal/cache"
	"github.com/chrishunter/1dler/internal/metrics"
	"github.com/chrishunter/1dler/internal/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type Bank struct {
	signer      BankSigner
	ethClient   *ethclient.Client
	writeClient *ethclient.Client
	minBalance  *big.Int
	maxBalance  *big.Int
	topUpAmount *big.Int
	cache       cache.Cache
}

const (
	highPriorityQueue = "bank:topup:high"
	lowPriorityQueue  = "bank:topup:low"
	topUpLockKey      = "bank:topup:lock"
	immediateLockKey  = "bank:immediate:lock" // New lock key for immediate funding
	lockTTL           = 30 * time.Second
)

func NewBank(ethClient *ethclient.Client, writeClient *ethclient.Client, minBalanceEth float64, maxBalanceEth float64, topUpAmountEth float64, cache cache.Cache, debug bool, signer BankSigner) (*Bank, error) {
	// Convert min balance to wei (1 ETH = 10^18 wei)
	minBalanceWei := new(big.Int)
	minBalanceFloat := new(big.Float).Mul(big.NewFloat(minBalanceEth), big.NewFloat(1e18))
	minBalanceFloat.Int(minBalanceWei)

	maxBalanceWei := new(big.Int)
	maxBalanceFloat := new(big.Float).Mul(big.NewFloat(maxBalanceEth), big.NewFloat(1e18))
	maxBalanceFloat.Int(maxBalanceWei)

	// Convert top up amount to wei
	topUpAmountWei := new(big.Int)
	topUpAmountFloat := new(big.Float).Mul(big.NewFloat(topUpAmountEth), big.NewFloat(1e18))
	topUpAmountFloat.Int(topUpAmountWei)

	if maxBalanceWei.Cmp(minBalanceWei) <= 0 {
		return nil, fmt.Errorf("max balance must be greater than min balance")
	}

	// Initialize the bank
	bank := &Bank{
		signer:      signer,
		ethClient:   ethClient,
		writeClient: writeClient,
		minBalance:  minBalanceWei,
		maxBalance:  maxBalanceWei,
		topUpAmount: topUpAmountWei,
		cache:       cache,
	}

	return bank, nil
}

// EnsureFundedImmediate immediately funds an address if needed, using a lock for nonce protection
func (b *Bank) EnsureFunded(ctx context.Context, address common.Address, requiredBalance *big.Int) error {
	// Get current balance
	balance, err := b.writeClient.BalanceAt(ctx, address, nil)
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}

	balanceFloat, _ := new(big.Float).SetInt(balance).Float64()
	metrics.RelayerBalance.WithLabelValues(address.Hex()).Set(balanceFloat / 1e18)

	if requiredBalance == nil || requiredBalance.Cmp(b.minBalance) < 0 {
		requiredBalance = b.minBalance
	}

	// If balance is sufficient, return immediately
	if balance.Cmp(requiredBalance) >= 0 {
		return nil
	}

	// Acquire immediate funding lock (separate from worker lock)
	lockID := uuid.New().String()

	// Retry logic with exponential backoff
	maxRetries := 5
	baseDelay := 100 * time.Millisecond
	maxDelay := 2 * time.Second
	lockAcquired := false

	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := b.cache.Lock(immediateLockKey, lockID, lockTTL); err != nil {
			if errors.Is(err, cache.ErrItemLocked) {
				// Calculate delay with exponential backoff
				delay := time.Duration(math.Min(
					float64(baseDelay*time.Duration(math.Pow(2, float64(attempt)))),
					float64(maxDelay),
				))

				log.Info("Lock is held, waiting before retry",
					"attempt", attempt+1,
					"max_attempts", maxRetries,
					"delay", delay)

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
					continue
				}
			}
			return fmt.Errorf("failed to acquire bank lock: %w", err)
		} else {
			lockAcquired = true
			break
		}
	}

	if !lockAcquired {
		return fmt.Errorf("failed to acquire bank lock after %d attempts", maxRetries)
	}

	defer b.cache.Unlock(immediateLockKey, lockID)

	// Create job map for processTopUp
	job := map[string]any{
		"address":   address.Hex(),
		"balance":   balance.String(),
		"createdAt": time.Now().Format(time.RFC3339),
	}

	// Use existing processTopUp function
	return b.processTopUp(ctx, job)
}

func (b *Bank) processTopUp(ctx context.Context, job map[string]any) error {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		metrics.BankFundingDuration.WithLabelValues().Set(duration.Seconds())
	}()

	log.Info("Processing top-up job", "address", job["address"].(string))

	// Use batch calls to get balance and gas price information
	var balanceHex, maxPriorityFeeHex, baseFeeHex, chainIDHex string
	var nonceHex hexutil.Uint64

	batch := []rpc.BatchElem{
		{
			Method: "eth_getBalance",
			Args:   []any{job["address"], "latest"},
			Result: &balanceHex,
		},
		{
			Method: "eth_maxPriorityFeePerGas",
			Result: &maxPriorityFeeHex,
		},
		{
			Method: "eth_gasPrice",
			Result: &baseFeeHex,
		},
		{
			Method: "eth_chainId",
			Result: &chainIDHex,
		},
		{
			Method: "eth_getTransactionCount",
			Args:   []interface{}{b.signer.Address(), "pending"},
			Result: &nonceHex,
		},
	}

	if err := b.ethClient.Client().BatchCallContext(ctx, batch); err != nil {
		return fmt.Errorf("batch call failed: %w", err)
	}

	// Check for individual errors in batch results
	for i, elem := range batch {
		if elem.Error != nil {
			return fmt.Errorf("batch request %d failed: %w", i, elem.Error)
		}
	}

	// Convert hex strings to big.Int
	balance, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return fmt.Errorf("failed to decode balance: %w", err)
	}

	maxPriorityFee, err := hexutil.DecodeBig(maxPriorityFeeHex)
	if err != nil {
		return fmt.Errorf("failed to decode max priority fee: %w", err)
	}

	baseFee, err := hexutil.DecodeBig(baseFeeHex)
	if err != nil {
		return fmt.Errorf("failed to decode base fee: %w", err)
	}

	chainID, err := hexutil.DecodeBig(chainIDHex)
	if err != nil {
		return fmt.Errorf("failed to decode chain ID: %w", err)
	}

	nonce := uint64(nonceHex)

	if balance.Cmp(b.minBalance) >= 0 {
		return nil // Balance is now sufficient
	}

	// Increase fees for faster inclusion
	maxPriorityFee = new(big.Int).Mul(maxPriorityFee, big.NewInt(2)) // 2x priority fee
	maxFeePerGas := new(big.Int).Add(
		baseFee,
		maxPriorityFee,
	)

	address := common.HexToAddress(job["address"].(string))

	log.Info("Calculated gas prices for top-up",
		"max_priority_fee", maxPriorityFee.String(),
		"max_fee_per_gas", maxFeePerGas.String())

	// Create EIP-1559 transaction
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		To:        &address,
		Value:     b.topUpAmount,
		Gas:       21000,
		GasTipCap: maxPriorityFee, // max priority fee per gas
		GasFeeCap: maxFeePerGas,   // max fee per gas
	})

	// Sign transaction
	signedTx, err := b.signer.SignTransaction(ctx, chainID, tx)
	if err != nil {
		return fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send transaction
	if err := b.writeClient.SendTransaction(ctx, signedTx); err != nil {
		errorMsg := err.Error()
		if strings.Contains(errorMsg, "replacement transaction underpriced") {
			// Increase gas fees and retry with same nonce
			maxPriorityFee = new(big.Int).Mul(maxPriorityFee, big.NewInt(2)) // Double the priority fee
			maxFeePerGas = new(big.Int).Add(
				baseFee,
				maxPriorityFee,
			)

			// Create new transaction with increased fees but same nonce
			newTx := types.NewTx(&types.DynamicFeeTx{
				ChainID:   chainID,
				Nonce:     nonce,
				To:        &address,
				Value:     b.topUpAmount,
				Gas:       21000,
				GasTipCap: maxPriorityFee,
				GasFeeCap: maxFeePerGas,
			})

			signedTx, err = b.signer.SignTransaction(ctx, chainID, newTx)
			if err != nil {
				return fmt.Errorf("failed to sign replacement transaction: %w", err)
			}

			if err := b.writeClient.SendTransaction(ctx, signedTx); err != nil {
				return fmt.Errorf("failed to send replacement transaction: %w", err)
			}

			log.Info("Sent replacement transaction with higher fees",
				"hash", signedTx.Hash().Hex(),
				"new_max_priority_fee", maxPriorityFee.String(),
				"new_max_fee_per_gas", maxFeePerGas.String())
		} else if strings.Contains(errorMsg, "nonce too low") {
			nextNonce, err := util.GetNonceFromErrorMessage(errorMsg)
			if err != nil {
				return err
			}

			// Create new transaction with correct nonce
			newTx := types.NewTx(&types.DynamicFeeTx{
				ChainID:   chainID,
				Nonce:     uint64(nextNonce),
				To:        &address,
				Value:     b.topUpAmount,
				Gas:       21000,
				GasTipCap: maxPriorityFee,
				GasFeeCap: maxFeePerGas,
			})

			signedTx, err = b.signer.SignTransaction(ctx, chainID, newTx)
			if err != nil {
				return fmt.Errorf("failed to sign transaction with updated nonce: %w", err)
			}

			if err := b.writeClient.SendTransaction(ctx, signedTx); err != nil {
				return fmt.Errorf("failed to send transaction with updated nonce: %w", err)
			}

			log.Info("Sent transaction with corrected nonce",
				"hash", signedTx.Hash().Hex(),
				"new_nonce", nextNonce)
		} else {
			return fmt.Errorf("failed to send transaction: %w", err)
		}
	} else {
		log.Info("Top-up transaction sent",
			"hash", signedTx.Hash().Hex(),
			"to", address.Hex(),
			"value", b.topUpAmount.String(),
			"max_priority_fee", maxPriorityFee.String(),
			"max_fee_per_gas", maxFeePerGas.String())
	}

	// Wait for transaction to be mined with shorter intervals
	receipt, err := b.waitForTransaction(ctx, signedTx.Hash())
	if err != nil {
		return fmt.Errorf("failed waiting for transaction: %w", err)
	}

	if receipt.Status == 0 {
		return fmt.Errorf("transaction failed: %s", signedTx.Hash().Hex())
	}

	log.Info("Top-up transaction confirmed",
		"hash", signedTx.Hash().Hex(),
		"to", address.Hex())

	metrics.BankFundingOps.Inc()
	return nil
}

func (b *Bank) drainFundsFromKey(ctx context.Context, signer *RelaySigner, chainID *big.Int) error {
	log.Info("Draining funds from key", "address", signer.Address.Hex())

	// Use batch calls to get balance and gas price information
	var balanceHex, maxPriorityFeeHex, baseFeeHex string
	var nonceHex hexutil.Uint64

	batch := []rpc.BatchElem{
		{
			Method: "eth_getBalance",
			Args:   []any{signer.Address, "pending"},
			Result: &balanceHex,
		},
		{
			Method: "eth_maxPriorityFeePerGas",
			Result: &maxPriorityFeeHex,
		},
		{
			Method: "eth_gasPrice",
			Result: &baseFeeHex,
		},
		{
			Method: "eth_getTransactionCount",
			Args:   []interface{}{signer.Address, "pending"},
			Result: &nonceHex,
		},
	}

	if err := b.ethClient.Client().BatchCallContext(ctx, batch); err != nil {
		return fmt.Errorf("batch call failed: %w", err)
	}

	// Check for individual errors in batch results
	for i, elem := range batch {
		if elem.Error != nil {
			return fmt.Errorf("batch request %d failed: %w", i, elem.Error)
		}
	}

	// Convert hex strings to big.Int
	balance, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return fmt.Errorf("failed to decode balance: %w", err)
	}

	maxPriorityFee, err := hexutil.DecodeBig(maxPriorityFeeHex)
	if err != nil {
		return fmt.Errorf("failed to decode max priority fee: %w", err)
	}

	baseFee, err := hexutil.DecodeBig(baseFeeHex)
	if err != nil {
		return fmt.Errorf("failed to decode base fee: %w", err)
	}

	nonce := uint64(nonceHex)

	if balance.Cmp(b.maxBalance) < 0 {
		return nil // Balance is now sufficient
	}
	log.Info("Balance at account: ", "balance", balance.String(), "address", signer.Address.Hex(), "max_balance", b.maxBalance.String())

	// Increase fees for faster inclusion
	maxPriorityFee = new(big.Int).Mul(maxPriorityFee, big.NewInt(2)) // 2x priority fee
	maxFeePerGas := new(big.Int).Add(
		baseFee,
		maxPriorityFee,
	)

	log.Info("Calculated gas prices for top-up",
		"max_priority_fee", maxPriorityFee.String(),
		"max_fee_per_gas", maxFeePerGas.String())

	amount := balance.Sub(balance, b.topUpAmount)
	toAddress := b.signer.Address()

	// Create EIP-1559 transaction
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		To:        &toAddress,
		Value:     amount, // this should be balance - minBalance
		Gas:       21000,
		GasTipCap: maxPriorityFee, // max priority fee per gas
		GasFeeCap: maxFeePerGas,   // max fee per gas
	})
	log.Info("Draining funds from key", "address", signer.Address.Hex(), "amount", amount.String())

	// Sign transaction with relay signer
	signedTx, err := signer.SignTransaction(ctx, chainID, tx)
	if err != nil {
		return fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send transaction
	if err := b.writeClient.SendTransaction(ctx, signedTx); err != nil {
		return fmt.Errorf("failed to send transaction: %w", err)
	} else {
		log.Info("Drain funds transaction sent",
			"hash", signedTx.Hash().Hex(),
			"to", toAddress.Hex(),
			"value", amount.String(),
			"max_priority_fee", maxPriorityFee.String(),
			"max_fee_per_gas", maxFeePerGas.String())
	}

	// wait for transaction to be mined
	receipt, err := b.waitForTransaction(ctx, signedTx.Hash())
	if err != nil {
		return fmt.Errorf("failed waiting for transaction: %w", err)
	}

	if receipt.Status == 0 {
		return fmt.Errorf("transaction failed: %s", signedTx.Hash().Hex())
	}

	log.Info("Drain funds transaction confirmed",
		"hash", signedTx.Hash().Hex(),
		"to", toAddress.Hex())

	metrics.BankFundingOps.Inc()

	return nil
}

func (b *Bank) ManageFunds(ctx context.Context, signer *RelaySigner, chainID *big.Int) error {
	balance, err := b.writeClient.BalanceAt(ctx, signer.Address, nil)
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}

	if balance.Cmp(b.maxBalance) > 0 {
		// Now we will drain funds from the key, checking the balance again before doing so.
		b.drainFundsFromKey(ctx, signer, chainID)
	}

	return nil
}

func (b *Bank) waitForTransaction(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	log.Debug("Waiting for transaction to be mined", "hash", hash.Hex())

	// Start with a short interval and gradually increase it
	interval := 100 * time.Millisecond
	maxInterval := 1 * time.Second
	backoff := 1.5 // Backoff multiplier

	for {
		receipt, err := b.writeClient.TransactionReceipt(ctx, hash)
		if err == nil {
			log.Debug("Transaction mined", "hash", hash.Hex(), "block", receipt.BlockNumber.String())
			return receipt, nil
		}

		// If we got an error other than "not found", return it
		if err.Error() != "not found" {
			return nil, err
		}

		// Check if context was cancelled
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
			// Increase interval for next attempt, but don't exceed maxInterval
			newInterval := time.Duration(float64(interval) * backoff)
			interval = min(newInterval, maxInterval)
			continue
		}
	}
}

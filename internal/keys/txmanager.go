package keys

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/google/uuid"
)

type TransactionStatus string

const (
	Pending   TransactionStatus = "pending"
	Confirmed TransactionStatus = "confirmed"
	Failed    TransactionStatus = "failed"
)

const (
	StuckTimeout = 10 * time.Second // todo configurable
)

type TransactionInfo struct {
	TxHash     string
	UserOpHash string
	Nonce      uint64
	ReservedAt time.Time
	Status     TransactionStatus
	GasFeeCap  uint64
	GasTipCap  uint64
}

func (k *KeyService) UpdateTransactionForSigner(signerIndex uint, txnHash string, userOpHash string, status TransactionStatus, nonce uint64, gasFeeCap uint64, gasTipCap uint64) error {
	err := k.cache.UpdateKeyData(signerIndex, map[string]any{
		"txHash":     txnHash,
		"userOpHash": userOpHash,
		"status":     string(status),
		"nonce":      nonce,
		"gasFeeCap":  gasFeeCap,
		"gasTipCap":  gasTipCap,
	})
	if err != nil {
		log.Error("Failed to update transaction for signer", "error", err)
		return fmt.Errorf("failed to update transaction for signer")
	}
	return nil
}

func (k *KeyService) checkTransactions(ctx context.Context) error {
	reservedKeys, err := k.cache.GetAllReservedKeysWithData()
	if err != nil {
		log.Error("Failed to get all reserved keys with data", "error", err)
		return fmt.Errorf("failed to get all reserved keys with data")
	}

	log.Debug("Checking pending transactions", "count", len(reservedKeys))
	if len(reservedKeys) == 0 {
		return nil
	}

	// Prepare batch call
	batch := make([]rpc.BatchElem, 0, len(reservedKeys))
	txToKeyNum := make(map[common.Hash]uint)

	for keyNum, data := range reservedKeys {
		// Check if the key has a transaction hash
		txHashValue, hasTxHash := data["txHash"]
		if !hasTxHash || txHashValue == nil || txHashValue.(string) == "" {
			continue
		}

		// Add keys with transaction hash to the batch
		txHash := common.HexToHash(txHashValue.(string))
		batch = append(batch, rpc.BatchElem{
			Method: "eth_getTransactionReceipt",
			Args:   []any{txHash},
			Result: &types.Receipt{},
		})
		txToKeyNum[txHash] = keyNum
	}

	// Execute batch call for keys with transactions
	if len(batch) > 0 {
		if err := k.ethClient.Client().BatchCallContext(ctx, batch); err != nil {
			log.Error("Batch receipt check failed", "error", err)
			// return fmt.Errorf("batch receipt check failed") let expiry happen
		}

		// Process results
		for _, elem := range batch {
			receipt := elem.Result.(*types.Receipt)
			keyNum := txToKeyNum[receipt.TxHash]
			if elem.Error != nil || receipt == nil {
				continue
			}

			k.release(keyNum, string(Confirmed))
		}
	}

	// Check expiration for keys without transactions
	for keyNum, keyInfo := range reservedKeys {
		var reservedAt time.Time
		if reservedAtStr, ok := keyInfo["reservedAt"].(string); ok {
			if epoch, err := strconv.ParseInt(reservedAtStr, 10, 64); err == nil {
				reservedAt = time.Unix(epoch, 0)
			}
		}

		if time.Since(reservedAt) > StuckTimeout {
			// Release key without changing status
			k.release(keyNum, "")
		}
	}

	return nil
}

func (k *KeyService) workOnTransactions(ctx context.Context) {
	log.Info("Started working on transactions")

	uuid := uuid.New().String()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping transaction processing due to context cancellation")
			k.running = false
			return
		case <-k.stopChan:
			log.Info("Stopping transaction processing due to stop signal")
			k.running = false
			return
		case <-ticker.C:
			err := k.cache.Lock(uuid, uuid, 4*time.Second)
			if err == nil {
				k.checkTransactions(ctx)
			}
		}
	}
}

// Worker start
func (k *KeyService) StartWorker(ctx context.Context) {
	if k.running {
		log.Info("Key service worker already running")
		return
	}

	// Initialize the stop channel
	k.stopChan = make(chan struct{})
	k.running = true

	log.Info("Starting key service transaction manager worker")
	go k.workOnTransactions(ctx)
}

// Stop txmanager worker
func (k *KeyService) StopWorker() {
	if !k.running {
		return
	}

	close(k.stopChan)
	k.running = false
}

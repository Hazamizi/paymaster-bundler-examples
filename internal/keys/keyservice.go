package keys

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/chrishunter/1dler/internal/cache"
	"github.com/chrishunter/1dler/internal/metrics"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

// Service handles the key reservation system
type KeyService struct {
	cache                 cache.Cache
	maxKeys               uint
	hdWallet              *hdwallet.Wallet
	mnemonic              string
	chainID               *big.Int
	primaryBundlerAddress common.Address
	bank                  *Bank
	ethClient             *ethclient.Client
	running               bool
	stopChan              chan struct{}
}

// New creates a new key reservation service
func New(cache cache.Cache, maxKeys uint, mnemonic string, chainID *big.Int, bank *Bank, ethClient *ethclient.Client) *KeyService {
	if maxKeys <= 0 || maxKeys > 10000 {
		maxKeys = 10000
	}

	k := &KeyService{
		cache:     cache,
		maxKeys:   maxKeys,
		mnemonic:  mnemonic,
		chainID:   chainID,
		bank:      bank,
		ethClient: ethClient,
	}

	return k
}

// Initialize populates the cache with available keys and sets up HD wallet
func (s *KeyService) Initialize(initialKeys uint) error {
	if s.mnemonic == "" {
		return fmt.Errorf("mnemonic is required for key generation")
	}

	if initialKeys > s.maxKeys {
		initialKeys = s.maxKeys
	}

	// Initialize HD wallet
	wallet, err := hdwallet.NewFromMnemonic(s.mnemonic)
	if err != nil {
		return fmt.Errorf("failed to create HD wallet: %w", err)
	}
	s.hdWallet = wallet

	// Derive primary bundler address (used for simulations)
	path, err := hdwallet.ParseDerivationPath("m/44'/60'/0'/0/0")
	if err != nil {
		return fmt.Errorf("failed to parse derivation path: %w", err)
	}
	account, err := s.hdWallet.Derive(path, true)
	if err != nil {
		return fmt.Errorf("failed to derive primary bundler address: %w", err)
	}
	s.primaryBundlerAddress = account.Address

	numKeys, err := s.cache.InitializeKeys(initialKeys)
	if err != nil {
		return fmt.Errorf("failed to initialize keys: %w", err)
	}

	log.Info("Initialized keys", "numKeys", numKeys, "maxKeys", s.maxKeys)

	s.StartWorker(context.Background())

	return nil
}

func (s *KeyService) GetSignerAddresses() ([]common.Address, error) {
	addresses := make([]common.Address, 0, s.maxKeys)
	for i := range s.maxKeys {
		// Derive key from HD wallet
		path, err := hdwallet.ParseDerivationPath(fmt.Sprintf("m/44'/60'/0'/0/%d", i))
		if err != nil {
			return nil, fmt.Errorf("failed to parse derivation path: %w", err)
		}
		account, err := s.hdWallet.Derive(path, true)
		if err != nil {
			return nil, fmt.Errorf("failed to derive address: %w", err)
		}
		addresses = append(addresses, account.Address)
	}
	return addresses, nil
}

// SetChainID sets the chain ID for the service
func (s *KeyService) SetChainID(ctx context.Context, client *ethclient.Client) error {
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get chain ID: %w", err)
	}
	s.chainID = chainID
	return nil
}

// GetChainID returns the stored chain ID or fetches it if not available
func (s *KeyService) GetChainID(ctx context.Context, client *ethclient.Client) (*big.Int, error) {
	if s.chainID != nil {
		return s.chainID, nil
	}

	// Fetch and store if not available
	if err := s.SetChainID(ctx, client); err != nil {
		return nil, err
	}
	return s.chainID, nil
}

// GetNextSigner gets and reserves the next available signer
func (s *KeyService) GetNextSigner() (*RelaySigner, error) {
	keyNum, data, err := s.cache.ReserveNextKey()
	if err != nil {
		return nil, fmt.Errorf("failed to reserve key: %w", err)
	}

	metrics.KeyNumber.WithLabelValues().Set(float64(keyNum))

	// Derive key from HD wallet
	path, err := hdwallet.ParseDerivationPath(fmt.Sprintf("m/44'/60'/0'/0/%d", keyNum))
	if err != nil {
		s.release(keyNum, "")
		return nil, fmt.Errorf("failed to parse derivation path: %w", err)
	}

	account, err := s.hdWallet.Derive(path, true)
	if err != nil {
		s.release(keyNum, "")
		return nil, fmt.Errorf("failed to derive account: %w", err)
	}

	privateKey, err := s.hdWallet.PrivateKey(account)
	if err != nil {
		s.release(keyNum, "")
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	txData, err := getTransactionData(data)
	if err != nil {
		s.release(keyNum, "")
		return nil, fmt.Errorf("failed to get transaction data: %w", err)
	}

	signer := &RelaySigner{
		privateKey: privateKey,
		Address:    account.Address,
		Index:      keyNum,
		ChainID:    s.chainID,
		TxData:     txData,
	}

	if s.bank != nil {
		// This will block until funding is complete or fails
		if err := s.bank.EnsureFunded(context.Background(), signer.Address, nil); err != nil {
			s.release(keyNum, "")
			return nil, fmt.Errorf("failed to fund key: %w", err)
		}
	}

	return signer, nil
}

// ReleaseSigner releases a signer back to the pool
func (s *KeyService) ReleaseSigner(signer *RelaySigner, status string) error {
	if signer == nil {
		return fmt.Errorf("cannot release nil signer")
	}
	log.Info("Starting release for signer", "address", signer.Address.Hex(), "keyNumber", signer.Index, "status", status)

	// Check if we need to manage funds
	if s.bank != nil {
		// Run ManageFunds in a separate goroutine
		go func() {
			if err := s.bank.ManageFunds(context.Background(), signer, s.chainID); err != nil {
				log.Error("Error managing funds for key", "key", signer.Index, "error", err)
			}
			err := s.release(signer.Index, status)
			if err != nil {
				log.Error("Error releasing key", "key", signer.Index, "error", err)
			}
		}()
	} else {
		err := s.release(signer.Index, status)
		if err != nil {
			return fmt.Errorf("error releasing key %d: %w", signer.Index, err)
		}
	}

	return nil
}

// Release releases a locked key (internal use only)
func (s *KeyService) release(keyNum uint, status string) error {
	if keyNum > s.maxKeys {
		return fmt.Errorf("invalid key number: %d", keyNum)
	}

	log.Info("Releasing key", "keyNumber", keyNum, "status", status)

	return s.cache.ReleaseKey(keyNum, status)
}

// GetPrimaryBundlerAddress returns the primary bundler address
func (s *KeyService) GetPrimaryBundlerAddress() common.Address {
	return s.primaryBundlerAddress
}

func (s *KeyService) GetBankAddress() common.Address {
	return s.bank.signer.Address()
}

// PeekNextKey returns the address of the next available key without reserving it
func (s *KeyService) PeekNextKey() (*common.Address, error) {
	// Get the next key number from cache without reserving
	keyNum, err := s.cache.PeekNextKey()
	if err != nil {
		return &common.Address{}, fmt.Errorf("failed to peek next key: %w", err)
	}

	// Derive key from HD wallet
	path, err := hdwallet.ParseDerivationPath(fmt.Sprintf("m/44'/60'/0'/0/%d", keyNum))
	if err != nil {
		return &common.Address{}, fmt.Errorf("failed to parse derivation path: %w", err)
	}

	account, err := s.hdWallet.Derive(path, true)
	if err != nil {
		return &common.Address{}, fmt.Errorf("failed to derive account: %w", err)
	}

	return &account.Address, nil
}

// Cache raw map to TransactionInfo object
func getTransactionData(data map[string]any) (TransactionInfo, error) {
	txData := TransactionInfo{}

	// Handle string fields
	if txHash, ok := data["txHash"].(string); ok {
		txData.TxHash = txHash
	}

	if userOpHash, ok := data["userOpHash"].(string); ok {
		txData.UserOpHash = userOpHash
	}

	// Handle numeric fields - try direct cast first, then string conversion
	if nonce, ok := data["nonce"].(uint64); ok {
		txData.Nonce = nonce
	} else if nonceStr, ok := data["nonce"].(string); ok {
		if parsedNonce, err := strconv.ParseUint(nonceStr, 10, 64); err == nil {
			txData.Nonce = parsedNonce
		}
	}

	if gasFeeCap, ok := data["gasFeeCap"].(uint64); ok {
		txData.GasFeeCap = gasFeeCap
	} else if gasFeeCapStr, ok := data["gasFeeCap"].(string); ok {
		if parsedGasFeeCap, err := strconv.ParseUint(gasFeeCapStr, 10, 64); err == nil {
			txData.GasFeeCap = parsedGasFeeCap
		}
	}

	if gasTipCap, ok := data["gasTipCap"].(uint64); ok {
		txData.GasTipCap = gasTipCap
	} else if gasTipCapStr, ok := data["gasTipCap"].(string); ok {
		if parsedGasTipCap, err := strconv.ParseUint(gasTipCapStr, 10, 64); err == nil {
			txData.GasTipCap = parsedGasTipCap
		}
	}

	// Handle timestamp field
	if reservedAt, ok := data["reservedAt"].(time.Time); ok {
		txData.ReservedAt = reservedAt
	} else if epochTimeStr, ok := data["reservedAt"].(string); ok {
		if epoch, err := strconv.ParseInt(epochTimeStr, 10, 64); err == nil {
			txData.ReservedAt = time.Unix(epoch, 0)
		}
	} else if epochTime, ok := data["reservedAt"].(int64); ok {
		txData.ReservedAt = time.Unix(epochTime, 0)
	} else if epochFloat, ok := data["reservedAt"].(float64); ok {
		txData.ReservedAt = time.Unix(int64(epochFloat), 0)
	}

	// Handle status field
	if status, ok := data["status"].(TransactionStatus); ok {
		txData.Status = status
	} else if statusStr, ok := data["status"].(string); ok {
		txData.Status = TransactionStatus(statusStr)
	}

	return txData, nil
}

package keys

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Signer represents an Ethereum account that can sign transactions
type RelaySigner struct {
	privateKey *ecdsa.PrivateKey
	Address    common.Address
	Index      uint
	ChainID    *big.Int
	TxData     TransactionInfo
}

// GetPrivateKey returns the private key from a signer
func (s *RelaySigner) GetPrivateKey() *ecdsa.PrivateKey {
	return s.privateKey
}

// GetTransactOpts prepares transaction options for the given signer
func (s *RelaySigner) GetTransactOpts(ctx context.Context, client *ethclient.Client, maxFeePerGas *big.Int, maxPriorityFee *big.Int) (*bind.TransactOpts, error) {
	var nonce uint64
	mfpg := new(big.Int).Set(maxFeePerGas)
	mpf := new(big.Int).Set(maxPriorityFee)

	if s.TxData.Status == Pending {
		nonce = s.TxData.Nonce

		// Bump 10%
		mfpg = new(big.Int).Mul(mfpg, big.NewInt(int64(110)))
		mpf = new(big.Int).Mul(mpf, big.NewInt(int64(110)))
		mfpg = new(big.Int).Div(mfpg, big.NewInt(100))
		mpf = new(big.Int).Div(mpf, big.NewInt(100))

	} else {
		// Prepare batch requests - nonce should be handled in cache
		batchElems := []rpc.BatchElem{
			{
				Method: "eth_getTransactionCount",
				Args:   []any{s.Address, "pending"}, // todo this should just be stored in the cache and no pre calls are needed (balance + nonce)
				Result: new(hexutil.Uint64),         // For nonce
			},
		}

		err := client.Client().BatchCallContext(ctx, batchElems)
		if err != nil {
			return nil, fmt.Errorf("batch request failed: %w", err)
		}

		// Check for individual call errors
		for i, elem := range batchElems {
			if elem.Error != nil {
				return nil, fmt.Errorf("batch request %d failed: %w", i, elem.Error)
			}
		}

		// Extract results
		nonce = uint64(*(batchElems[0].Result.(*hexutil.Uint64)))
	}

	// Create the transactor with chain ID
	auth, err := bind.NewKeyedTransactorWithChainID(s.privateKey, s.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create transactor: %w", err)
	}

	// Set transaction parameters
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasFeeCap = mfpg
	auth.GasTipCap = mpf

	return auth, nil
}

// SignTransaction signs a transaction using the relay signer's private key
func (s *RelaySigner) SignTransaction(ctx context.Context, chainID *big.Int, tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), s.privateKey)
}

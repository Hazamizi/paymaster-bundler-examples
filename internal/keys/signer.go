package keys

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
)

// BankSigner interface for different signing implementations
type BankSigner interface {
	SignTransaction(ctx context.Context, chainID *big.Int, tx *types.Transaction) (*types.Transaction, error)
	Address() common.Address
}

// LocalSigner implements Signer using a local private key
type LocalSigner struct {
	privateKey *ecdsa.PrivateKey
	address    common.Address
}

func NewLocalSigner(privateKeyHex string) (*LocalSigner, error) {
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key to ECDSA")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA)

	return &LocalSigner{
		privateKey: privateKey,
		address:    address,
	}, nil
}

func (s *LocalSigner) SignTransaction(ctx context.Context, chainID *big.Int, tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), s.privateKey)
}

func (s *LocalSigner) Address() common.Address {
	return s.address
}

// HTTPSigner implements Signer using a remote HTTP endpoint
type HTTPSigner struct {
	client  *rpc.Client
	address common.Address
}

func NewHTTPSigner(endpoint string, address common.Address, debug bool) (*HTTPSigner, error) {
	client, err := rpc.DialHTTP(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to signer endpoint: %w", err)
	}

	return &HTTPSigner{
		client:  client,
		address: address,
	}, nil
}

func (s *HTTPSigner) SignTransaction(ctx context.Context, chainID *big.Int, tx *types.Transaction) (*types.Transaction, error) {
	// Convert transaction to args format
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to encode transaction: %w", err)
	}

	var result hexutil.Bytes
	if err := s.client.CallContext(ctx, &result, "eth_signTransaction", hexutil.Encode(txBytes), chainID); err != nil {
		return nil, fmt.Errorf("eth_signTransaction failed: %w", err)
	}

	var signed types.Transaction
	if err := signed.UnmarshalBinary(result); err != nil {
		return nil, fmt.Errorf("failed to decode signed transaction: %w", err)
	}

	return &signed, nil
}

func (s *HTTPSigner) Address() common.Address {
	return s.address
}

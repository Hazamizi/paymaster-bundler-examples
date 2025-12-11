package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/log"

	entrypoint "github.com/chrishunter/1dler/bindings/v06"
	"github.com/chrishunter/1dler/internal/cache"
	"github.com/chrishunter/1dler/internal/errors"
	"github.com/chrishunter/1dler/internal/keys"
	"github.com/chrishunter/1dler/internal/logger"
	"github.com/chrishunter/1dler/internal/metrics"
	"github.com/chrishunter/1dler/internal/simulator"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ServerConfig struct {
	Port                      int
	Debug                     bool
	LogLevel                  string
	UseRedis                  bool
	RedisAddr                 string
	RedisPass                 string
	RedisDB                   int
	EntryPoints               []common.Address
	MaxKeys                   uint
	RpcURL                    string
	WriteRpcURL               string // RPC URL for sending transactions
	Mnemonic                  string
	PrefundCount              uint    // Number of keys to prefund
	MinBalance                float64 // Minimum balance in ETH
	MaxBalance                float64 // Maximum balance in ETH
	TopUpAmount               float64 // Amount to top up in ETH
	BankKey                   string  // Bank signer private key (hex)
	BankHTTP                  string  // Bank HTTP signer endpoint
	BankAddress               string  // Bank signer address (required for HTTP signer)
	ReturnTxnHashInSendUserOp bool    // Whether to return transaction hash in sendUserOperation response
	PVGMode                   string
	BasePVG                   *big.Int
	CGLMultiplierPercent      *big.Int
	VGLMultiplierPercent      *big.Int
	MaxTxSize                 uint64 // Maximum userOp gas size
	TrustedTraffic            bool   // Skip bundler protection checks from trusted traffic
}

type Server struct {
	router   *mux.Router
	upgrader websocket.Upgrader
	server   *http.Server

	port     int
	debug    bool
	logLevel string

	entryPoints []common.Address
	ethClient   *ethclient.Client
	writeClient *ethclient.Client // Flashbots
	rpcURL      string
	writeRpcURL string

	basePVG *big.Int

	simulator           *simulator.Simulator
	entryPointContracts map[common.Address]*entrypoint.Entrypoint
	cache               cache.Cache
	keyService          *keys.KeyService
	bank                *keys.Bank

	returnTxnHashInSendUserOp bool   // Whether to return transaction hash in sendUserOperation response
	maxTxSize                 uint64 // Maximum userOp size (sum of callGasLimit and verificationGasLimit)
	chainID                   *big.Int

	trusted bool // skip bundler protection from trusted traffic
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.RpcURL == "" {
		return nil, fmt.Errorf("RPC URL is required")
	}

	// Initialize logging
	if config.Debug {
		logger.ConfigureLogger("debug", true)
	} else {
		logger.ConfigureLogger(config.LogLevel, false)
	}

	// Initialize Ethereum client
	ethClient, err := ethclient.Dial(config.RpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ethereum node: %w", err)
	}

	// Initialize write client (use RpcURL if WriteRpcURL is not set)
	writeRpcURL := config.RpcURL
	if config.WriteRpcURL != "" {
		writeRpcURL = config.WriteRpcURL
	}
	writeClient, err := ethclient.Dial(writeRpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to write Ethereum node: %w", err)
	}

	var c cache.Cache
	if config.UseRedis {
		c, err = cache.NewRedisCache(config.RedisAddr, config.RedisPass, config.RedisDB, config.MaxKeys)
		if err != nil {
			return nil, fmt.Errorf("failed to create Redis cache: %w", err)
		}
	} else {
		c = cache.NewMemoryCache(config.MaxKeys)
	}

	chainID, err := ethClient.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	log.Info("Client chain ID", "chainID", chainID)

	// Initialize bank with appropriate signer
	var signer keys.BankSigner
	if config.BankHTTP != "" {
		if config.BankAddress == "" {
			return nil, fmt.Errorf("bank-address is required when using bank-http")
		}
		bankAddr := common.HexToAddress(config.BankAddress)
		signer, err = keys.NewHTTPSigner(config.BankHTTP, bankAddr, config.Debug)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize HTTP signer: %w", err)
		}
	} else if config.BankKey != "" {
		signer, err = keys.NewLocalSigner(config.BankKey)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize local signer: %w", err)
		}
		log.Info("Using local bank signer", "address", signer.Address().Hex())
	} else {
		return nil, fmt.Errorf("either bank-key or bank-http must be provided")
	}

	// Initialize bank
	bank, err := keys.NewBank(ethClient, writeClient, config.MinBalance, config.MaxBalance, config.TopUpAmount, c, config.Debug, signer)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize bank: %w", err)
	}

	// Initialize key service
	keyService := keys.New(c, config.MaxKeys, config.Mnemonic, chainID, bank, ethClient)
	if err := keyService.Initialize(config.PrefundCount); err != nil {
		return nil, fmt.Errorf("failed to initialize key service: %w", err)
	}

	// Initialize simulator
	simulator, err := simulator.New(ethClient, config.EntryPoints, config.Debug, config.BasePVG, config.PVGMode, config.CGLMultiplierPercent, config.VGLMultiplierPercent)
	if err != nil {
		return nil, fmt.Errorf("failed to create simulator: %w", err)
	}

	// Initialize entrypoint contracts
	entryPointContracts := make(map[common.Address]*entrypoint.Entrypoint)
	for _, addr := range config.EntryPoints {
		contract, err := entrypoint.NewEntrypoint(addr, ethClient)
		if err != nil {
			return nil, fmt.Errorf("failed to create EntryPoint contract instance for %s: %w", addr.Hex(), err)
		}
		entryPointContracts[addr] = contract
	}

	// Prefund initial set of keys if configured
	if config.PrefundCount > 0 {
		log.Info("Prefunding keys", "count", config.PrefundCount, "amount", config.MinBalance, "unit", "ETH")

		// Get all signers at once
		relayers, err := keyService.GetSignerAddresses()
		if err != nil {
			return nil, fmt.Errorf("failed to get signer addresses: %w", err)
		}

		// Fund all signers
		for i := range config.PrefundCount {
			if err := bank.EnsureFunded(context.Background(), relayers[i], big.NewInt(0)); err != nil {
				return nil, fmt.Errorf("failed to prefund relayer %s: %w", relayers[i].Hex(), err)
			}
		}
	}

	s := &Server{
		router: mux.NewRouter(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024 * 1024, // 1MB
			WriteBufferSize: 1024 * 1024, // 1MB
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		port:                      config.Port,
		debug:                     config.Debug,
		logLevel:                  config.LogLevel,
		keyService:                keyService,
		entryPoints:               config.EntryPoints,
		ethClient:                 ethClient,
		writeClient:               writeClient,
		basePVG:                   config.BasePVG,
		rpcURL:                    config.RpcURL,
		writeRpcURL:               config.WriteRpcURL,
		simulator:                 simulator,
		entryPointContracts:       entryPointContracts,
		cache:                     c,
		bank:                      bank,
		returnTxnHashInSendUserOp: config.ReturnTxnHashInSendUserOp,
		chainID:                   chainID,
		maxTxSize:                 config.MaxTxSize,
		trusted:                   config.TrustedTraffic,
	}

	// Setup routes
	s.router.HandleFunc("/", s.handleHTTP).Methods("POST")
	s.router.HandleFunc("/ws", s.handleWebSocket)
	s.router.Handle("/metrics", promhttp.Handler())
	s.router.HandleFunc("/health", s.handleHealthCheck).Methods("GET")

	return s, nil
}

func (s *Server) Start() error {
	log.Info("Starting 1dler", "port", s.port)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.router,
	}

	return s.server.ListenAndServe()
}

// Close cleans up resources used by the server
func (s *Server) Close() error {
	log.Info("Shutting down server...")

	// Close the HTTP server
	if s.server != nil {
		if err := s.server.Close(); err != nil {
			log.Error("Error closing HTTP server", "error", err)
		}
	}

	// pause 60 seconds to ensure all requests are processed / cleaned up
	time.Sleep(60 * time.Second)

	// Close cache
	if s.cache != nil {
		if err := s.cache.Close(); err != nil {
			log.Error("Error closing cache", "error", err)
		}
	}

	// Close ethereum clients
	if s.ethClient != nil {
		s.ethClient.Close()
	}
	if s.writeClient != nil {
		s.writeClient.Close()
	}

	s.keyService.StopWorker()

	log.Info("Server shutdown complete")
	return nil
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, nil, errors.ErrorCodeInvalidRequest, "Failed to read request body")
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, nil, errors.ErrorCodeInvalidRequest, "Invalid JSON")
		return
	}

	if req.JSONRPC != "2.0" {
		s.writeError(w, req.ID, errors.ErrorCodeInvalidRequest, "Invalid JSON-RPC version")
		return
	}

	// Handle the request and write the response
	response := s.handleRequest(r.Context(), &req)
	s.writeResponse(w, response)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Store request context to use for all websocket messages
	ctx := r.Context()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error("WebSocket error", "error", err)
			}
			break
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(message, &req); err != nil {
			s.writeWebSocketError(conn, nil, errors.ErrorCodeInvalidRequest, "Invalid JSON")
			continue
		}

		if req.JSONRPC != "2.0" {
			s.writeWebSocketError(conn, req.ID, errors.ErrorCodeInvalidRequest, "Invalid JSON-RPC version")
			continue
		}

		// Handle the request and write the response
		response := s.handleRequest(ctx, &req)
		conn.WriteJSON(response)
	}
}

func (s *Server) handleRequest(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.RPCMethodDuration.WithLabelValues(req.Method).Observe(duration)
	}()

	var result any
	var data any
	var err error

	switch req.Method {
	case "eth_estimateUserOperationGas":
		result, err = s.handleEstimateUserOperationGas(ctx, req.Params)
		if err != nil {
			log.Error("eth_estimateUserOperationGas failed", "error", err)
		}

	case "eth_sendUserOperation":
		result, data, err = s.handleSendUserOperation(ctx, req.Params)
		if err != nil {
			log.Error("eth_sendUserOperation failed", "error", err)
		}

	case "eth_getUserOperationReceipt":
		result, err = s.handleGetUserOperationReceipt(ctx, req.Params)
		if err != nil {
			log.Error("eth_getUserOperationReceipt failed", "error", err)
		}

	case "eth_getUserOperationByHash":
		result, err = s.handleGetUserOperationByHash(ctx, req.Params)
		if err != nil {
			log.Error("eth_getUserOperationByHash failed", "error", err)
		}

	case "eth_supportedEntryPoints":
		result, err = s.handleSupportedEntryPoints(ctx)
		if err != nil {
			log.Error("eth_supportedEntryPoints failed", "error", err)
		}

	case "debug_key_next":
		log.Debug("Processing debug_key_next request")
		result, err = s.handleGetNextKey(ctx)
		if err != nil {
			log.Error("debug_key_next failed", "error", err)
		}

	case "debug_key_release":
		log.Debug("Processing debug_key_release request")
		result, err = s.handleReleaseKey(ctx, req.Params)
		if err != nil {
			log.Error("debug_key_release failed", "error", err)
		}

	case "debug_key_peak":
		log.Debug("Processing debug_key_peak request")
		result, err = s.handlePeakNextKey(ctx)
		if err != nil {
			log.Error("debug_key_peak failed", "error", err)
		}

	default:
		if s.debug {
			log.Debug("Passing through unknown RPC method", "method", req.Method)
			// In debug mode, passthrough unknown methods to the RPC URL
			result, err = s.passthroughRequest(ctx, req)
			if err != nil {
				log.Error("RPC passthrough failed", "method", req.Method, "error", err)
			}
		} else {
			log.Warn("Unknown method requested", "method", req.Method)
			err = fmt.Errorf("method not found: %s", req.Method)
		}
	}

	// Create response
	response := &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	if err != nil {
		if rpcErr, ok := err.(*RPCError); ok {
			response.Error = rpcErr
			metrics.RPCMethodErrors.WithLabelValues(req.Method, fmt.Sprintf("%d", rpcErr.Code)).Inc()
		} else {
			response.Error = &RPCError{
				Code:    errors.ErrorCodeInternal,
				Message: err.Error(),
			}
			metrics.RPCMethodErrors.WithLabelValues(req.Method, "unknown").Inc()
		}
	} else {
		response.Result = result
		response.Data = data
	}

	return response
}

// passthroughRequest forwards unknown RPC methods to the configured RPC URL
func (s *Server) passthroughRequest(ctx context.Context, req *JSONRPCRequest) (interface{}, error) {
	// Create the request body
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set common Ethereum JSON-RPC headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Connection", "keep-alive")
	httpReq.Header.Set("Cache-Control", "no-cache")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var jsonResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &jsonResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// For RPC errors, we want to pass through the entire error structure
	if jsonResp.Error != nil {
		return nil, &RPCError{
			Code:    jsonResp.Error.Code,
			Message: jsonResp.Error.Message,
			Data:    jsonResp.Error.Data,
		}
	}

	return jsonResp.Result, nil
}

func (s *Server) writeResponse(w http.ResponseWriter, response *JSONRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) writeError(w http.ResponseWriter, id interface{}, code int, message string) {
	response := &JSONRPCResponse{
		JSONRPC: "2.0",
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}
	s.writeResponse(w, response)
}

func (s *Server) writeWebSocketError(conn *websocket.Conn, id interface{}, code int, message string) {
	response := &JSONRPCResponse{
		JSONRPC: "2.0",
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}
	conn.WriteJSON(response)
}

// handleHealthCheck responds to health check requests
func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	// Check if the Ethereum client is connected
	_, err := s.ethClient.ChainID(context.Background())
	if err != nil {
		log.Warn("Health check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Ethereum client connection failed"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

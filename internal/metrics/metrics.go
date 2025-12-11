package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// UserOperation metrics
	UserOpCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "userop_total",
			Help: "Total number of user operations processed",
		},
		[]string{"status"}, // success, failure
	)

	UserOpProcessingTime = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "userop_processing_time_seconds",
			Help:    "Time taken for different phases of user operation processing",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"phase"}, // simulation, submission, total
	)

	UserOpSimulationResult = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "userop_simulation_total",
			Help: "Results of user operation simulations",
		},
		[]string{"result"}, // success, revert
	)

	UserOpLandedSuccess = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "userop_landed_result",
			Help: "Total number of successful user operations",
		},
		[]string{"result"}, // success, failed
	)

	BundleTransactionResult = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bundle_transaction_result",
			Help: "Total number of bundle transaction results",
		},
		[]string{"result"}, // success, failed, not found
	)

	EntryPointVersion = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "entrypoint_version",
			Help: "Version of the entrypoint contract",
		},
		[]string{"version"},
	)

	UserOpSubmissionRetries = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "userop_submission_retries",
			Help:    "Number of retries needed to submit a user operation",
			Buckets: []float64{0, 1, 2, 3, 4, 5, 10},
		},
	)

	// Cache metrics
	CacheMisses = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_misses",
			Help: "Total number of cache misses",
		},
		[]string{"type"}, // userop
	)

	// Bank metrics
	BankBalance = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bank_balance_eth",
			Help: "Current balance of addresses in ETH",
		},
		[]string{"address"},
	)

	RelayerBalance = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "relayer_balance_eth",
			Help: "Current balance of relayer addresses in ETH",
		},
		[]string{"address"},
	)

	BankFundingOps = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "bank_funding_operations_total",
			Help: "Total number of funding operations performed",
		},
	)

	BankFundingDuration = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bank_funding_duration_seconds",
			Help: "Duration of bank funding operations in seconds",
		},
		[]string{},
	)

	KeyNumber = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "key_number",
			Help: "current key number",
		},
		[]string{},
	)

	KeyReleaseDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "key_release_duration_seconds",
			Help:    "Duration of key release operations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{},
	)

	// Transaction metrics
	TxManagerConfirmationTime = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "tx_manager_confirmation_time_seconds",
			Help:    "Time taken for transaction to be confirmed",
			Buckets: prometheus.DefBuckets,
		},
	)

	TxManagerOpExpired = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "tx_manager_op_expired_total",
			Help: "Total number of expired transactions",
		},
	)

	// Gas metrics
	GasMeasurements = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gas_measurements",
			Help: "Measurements of gas",
		},
		[]string{"type"},
	)

	// General metrics
	RPCMethodDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rpc_method_duration_seconds",
			Help:    "Duration of RPC method calls in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)

	RPCMethodErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rpc_method_errors_total",
			Help: "Total number of RPC method errors",
		},
		[]string{"method", "error_code"},
	)

	WebsocketConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "websocket_connections",
			Help: "Current number of active WebSocket connections",
		},
	)
)

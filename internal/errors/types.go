package errors

// Common error codes
const (
	ErrorCodeInvalidRequest = -32600
	ErrorCodeMethodNotFound = -32601
	ErrorCodeInvalidParams  = -32602
	ErrorCodeInternal       = -32603
)

// Bundler specific error codes
const (
	ErrorCodeRejectedByEPOrAccount       = -32500 // The transaction was rejected by the EP or account
	ErrorCodeRejectedByPaymaster         = -32501 // The transaction was rejected by the Paymaster
	ErrorCodeBannedOpcode                = -32502 // The transaction contains a banned opcode
	ErrorCodeShortDeadline               = -32503 // The transaction deadline is too short
	ErrorCodeBannedOrThrottled           = -32504 // The entity is banned or throttled
	ErrorCodeInvalidEntityStake          = -32505 // The entity stake is invalid
	ErrorCodeInvalidAggregator           = -32506 // The aggregator is invalid
	ErrorCodeInvalidSignature            = -32507 // The transaction signature is invalid
	ErrorCodeExecutionReverted           = -32521 // The transaction execution was reverted
	ErrorCodeInvalidMaxFeePerGas         = -32522 // The maxFeePerGas is invalid
	ErrorCodeInvalidMaxPriorityFeePerGas = -32523 // The maxPriorityFeePerGas is invalid
	ErrorCodeInvalidPreVerificationGas   = -32524 // The preVerificationGas is invalid
	ErrorCodeInvalidTxSize               = -32525 // The transaction size is invalid
	ErrorCodeValidationFailed            = -32526 // The transaction validation failed
)

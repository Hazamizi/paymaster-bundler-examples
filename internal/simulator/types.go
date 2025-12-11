package simulator

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// EstimateUserOpGasResult represents the gas estimation result
type EstimateUserOpGasResult struct {
	PreVerificationGas   *hexutil.Big `json:"preVerificationGas"`
	VerificationGasLimit *hexutil.Big `json:"verificationGasLimit"`
	CallGasLimit         *hexutil.Big `json:"callGasLimit"`

	// V07+ fields
	PaymasterVerificationGasLimit *hexutil.Big `json:"paymasterVerificationGasLimit,omitempty"`

	// Non spec
	MaxFeePerGas         *hexutil.Big `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas *hexutil.Big `json:"maxPriorityFeePerGas,omitempty"`
}

// ReturnInfo represents the return info part of the ValidationResult error
type ReturnInfo struct {
	PreOpGas         *big.Int
	Prefund          *big.Int
	SigFailed        bool
	ValidAfter       *big.Int
	ValidUntil       *big.Int
	PaymasterContext []byte
}

// StakeInfo represents stake information in the ValidationResult error
type StakeInfo struct {
	Stake           *big.Int
	UnstakeDelaySec *big.Int
}

// ValidationResult represents the parsed validation result error
type ValidationResult struct {
	ReturnInfo    ReturnInfo
	SenderInfo    StakeInfo
	FactoryInfo   StakeInfo
	PaymasterInfo StakeInfo
}

// StateOverrides represents the state overrides for simulation
type OverrideSet map[common.Address]OverrideAccount
type OverrideAccount struct {
	Nonce     *hexutil.Uint64              `json:"nonce"`
	Code      *hexutil.Bytes               `json:"code"`
	Balance   *hexutil.Big                 `json:"balance"`
	State     *map[common.Hash]common.Hash `json:"state"`
	StateDiff *map[common.Hash]common.Hash `json:"stateDiff"`
}

// SimulateHandleOp result
type V06ExecutionResult struct {
	PreOpGas      *big.Int
	Paid          *big.Int
	ValidAfter    *big.Int
	ValidUntil    *big.Int
	TargetSuccess bool
	TargetResult  []byte
}

type SimulatorError struct {
	Code    int
	Message string
}

// Tracer Types

// TracerOutput represents the output from the debug tracer
type TracerOutput struct {
	Phases                    []Phase                         `json:"phases"`
	RevertData                *string                         `json:"revertData"`
	AccessedContracts         map[common.Address]ContractInfo `json:"accessedContracts"`
	AssociatedSlotsByAddress  AssociatedSlotsByAddress        `json:"associatedSlotsByAddress"`
	FactoryCalledCreate2Twice bool                            `json:"factoryCalledCreate2Twice"`
	ExpectedStorage           ExpectedStorage                 `json:"expectedStorage"`
}

// Phase represents a single phase of execution
type Phase struct {
	ForbiddenOpcodesUsed         []string                      `json:"forbiddenOpcodesUsed"`
	ForbiddenPrecompilesUsed     []string                      `json:"forbiddenPrecompilesUsed"`
	StorageAccesses              map[common.Address]AccessInfo `json:"storageAccesses"`
	CalledBannedEntryPointMethod bool                          `json:"calledBannedEntryPointMethod"`
	CalledNonEntryPointWithValue bool                          `json:"calledNonEntryPointWithValue"`
	RanOutOfGas                  bool                          `json:"ranOutOfGas"`
	UndeployedContractAccesses   []common.Address              `json:"undeployedContractAccesses"`
	ExtCodeAccessInfo            map[common.Address]string     `json:"extCodeAccessInfo"` // Opcode as string
}

// ContractInfo contains information about a contract
type ContractInfo struct {
	Header string `json:"header"`
	Opcode string `json:"opcode"` // Using string instead of custom Opcode type
	Length uint64 `json:"length"`
}

// AccessInfo contains information about storage accesses
type AccessInfo struct {
	// Using string keys since JSON cannot have non-string keys
	Reads  map[string]string `json:"reads"`  // slot -> raw hex value
	Writes map[string]uint64 `json:"writes"` // slot -> count
}

// AssociatedSlotsByAddress maps addresses to their associated slots
type AssociatedSlotsByAddress struct {
	// Using string keys for both address and slots since JSON cannot have non-string keys
	Slots map[string][]string `json:"slots"` // address -> []slot
}

// ExpectedStorage represents expected storage state
type ExpectedStorage struct {
	Storage map[common.Address]map[string]*big.Int `json:"storage"`
}

// EntityType represents the type of entity in the system
type EntityType string

const (
	EntityTypeFactory   EntityType = "Factory"
	EntityTypeAccount   EntityType = "Account"
	EntityTypePaymaster EntityType = "Paymaster"
)

// Entity represents an entity in the system with its address and type
type Entity struct {
	Kind    EntityType     `json:"kind"`
	Address common.Address `json:"address"`
}

// EntityInfo contains information about an entity including its staking status
type EntityInfo struct {
	Entity   Entity `json:"entity"`
	IsStaked bool   `json:"isStaked"`
}

// SimulationViolation represents different types of violations that can occur
type SimulationViolation struct {
	Type        string         `json:"type"`
	Entity      Entity         `json:"entity,omitempty"`
	Address     common.Address `json:"address,omitempty"`
	Contract    common.Address `json:"contract,omitempty"`
	Opcode      string         `json:"opcode,omitempty"`
	Precompile  string         `json:"precompile,omitempty"`
	Description string         `json:"description,omitempty"`
}

// StorageSlot represents a storage slot in the system
type StorageSlot struct {
	Address common.Address `json:"address"`
	Slot    string         `json:"slot"`
}

// ValidationContext contains the context for validation
type ValidationContext struct {
	EntityInfos       map[EntityType]EntityInfo
	TracerOut         TracerOutput
	EntryPointAddress common.Address
	SenderAddress     common.Address
	HasFactory        bool
	AccessedAddresses map[common.Address]bool
}

type StorageRestriction struct {
	Type            string         // "NeedsStake", "AssociatedStorageDuringDeploy", or "Banned"
	NeedsStake      EntityType     // Entity that needs stake
	AccessingEntity Entity         // Entity accessing the storage
	AccessedEntity  Entity         // Entity being accessed
	AccessedAddress common.Address // Address being accessed
	Slot            string         // Storage slot
}

// V0.7 EntryPoint Tracer Types

// BundlerTracerResult represents the output from the V0.7 debug tracer
type BundlerTracerResult struct {
	CallsFromEntryPoint []TopLevelCallInfo           `json:"callsFromEntryPoint"`
	Keccak              []string                     `json:"keccak"`
	Calls               []CallInfo                   `json:"calls"`
	Logs                []LogInfo                    `json:"logs"`
	ExpectedStorage     map[string]map[string]string `json:"expectedStorage"`
	Debug               []string                     `json:"debug"`
}

// TopLevelCallInfo represents information about top-level calls from EntryPoint
type TopLevelCallInfo struct {
	TopLevelMethodSig     string                     `json:"topLevelMethodSig"`
	TopLevelTargetAddress string                     `json:"topLevelTargetAddress"`
	Opcodes               map[string]int             `json:"opcodes"`
	Access                map[string]V07AccessInfo   `json:"access"`
	ContractInfo          map[string]V07ContractInfo `json:"contractInfo"`
	ExtCodeAccessInfo     map[string]string          `json:"extCodeAccessInfo"`
	OOG                   bool                       `json:"oog,omitempty"`
}

type V07ContractInfo struct {
	Opcode string `json:"opcode"`
	Length int    `json:"length"`
	Header string `json:"header"`
}

type V07AccessInfo struct {
	Reads  map[string]string `json:"reads"`  // slot -> raw hex value
	Writes map[string]int    `json:"writes"` // slot -> count
}

type LogInfo struct {
	Topics []string `json:"topics"`
	Data   string   `json:"data"`
}

type CallInfo struct {
	// Common fields
	Type string `json:"type"`

	// Method specific fields (for type = CALL, STATICCALL, etc.)
	From   string      `json:"from,omitempty"`
	To     string      `json:"to,omitempty"`
	Method string      `json:"method,omitempty"` // 4byte method selector
	Value  interface{} `json:"value,omitempty"`
	Gas    int         `json:"gas,omitempty"`

	// Exit specific fields (for type = REVERT or RETURN)
	GasUsed int    `json:"gasUsed,omitempty"`
	Data    string `json:"data,omitempty"` // For simulateValidation, this contains the validation result
}

// Method selectors for V0.7 EntryPoint
const (
	CREATE_SENDER_METHOD              = "0x6d1dd436" // createSender()
	VALIDATE_USER_OP_METHOD           = "0x3a871cdd" // validateUserOp()
	VALIDATE_PAYMASTER_USER_OP_METHOD = "0xf465c77e" // validatePaymasterUserOp()
)

// ParseOverrides parses the state overrides from a JSON object
func ParseOverrides(overridesJson []byte) (*OverrideSet, error) {
	var overrides OverrideSet
	if err := json.Unmarshal(overridesJson, &overrides); err != nil {
		return nil, fmt.Errorf("failed to parse state overrides: %w", err)
	}
	return &overrides, nil
}

package simulator

import "github.com/chrishunter/1dler/internal/errors"

// errorCodeMap maps error strings to their corresponding error codes
var errorCodeMap = map[string]int{
	// Common error codes for both v0.6 and v0.7
	"AA10": errors.ErrorCodeRejectedByEPOrAccount, // sender already constructed
	"AA13": errors.ErrorCodeRejectedByEPOrAccount, // initCode failed or OOG
	"AA14": errors.ErrorCodeRejectedByEPOrAccount, // initCode must return sender
	"AA15": errors.ErrorCodeRejectedByEPOrAccount, // initCode must create sender
	"AA20": errors.ErrorCodeRejectedByEPOrAccount, // account not deployed
	"AA21": errors.ErrorCodeRejectedByEPOrAccount, // didn't pay prefund
	"AA22": errors.ErrorCodeRejectedByEPOrAccount, // expired or not due
	"AA23": errors.ErrorCodeRejectedByEPOrAccount, // reverted (or OOG)
	"AA24": errors.ErrorCodeInvalidSignature,      // signature error
	"AA25": errors.ErrorCodeRejectedByEPOrAccount, // invalid account nonce
	"AA30": errors.ErrorCodeRejectedByPaymaster,   // paymaster not deployed
	"AA31": errors.ErrorCodeRejectedByPaymaster,   // paymaster deposit too low
	"AA32": errors.ErrorCodeRejectedByPaymaster,   // paymaster expired or not due
	"AA33": errors.ErrorCodeRejectedByPaymaster,   // reverted (or OOG)
	"AA34": errors.ErrorCodeRejectedByPaymaster,   // signature error
	"AA40": errors.ErrorCodeValidationFailed,      // over verificationGasLimit
	"AA41": errors.ErrorCodeValidationFailed,      // too little verificationGas
	"AA50": errors.ErrorCodeRejectedByPaymaster,   // postOp reverted
	"AA51": errors.ErrorCodeRejectedByPaymaster,   // prefund below actualGasCost
	"AA90": errors.ErrorCodeRejectedByEPOrAccount, // invalid beneficiary
	"AA91": errors.ErrorCodeRejectedByEPOrAccount, // failed send to beneficiary
	"AA92": errors.ErrorCodeRejectedByEPOrAccount, // internal call only
	"AA93": errors.ErrorCodeRejectedByEPOrAccount, // invalid paymasterAndData
	"AA94": errors.ErrorCodeRejectedByEPOrAccount, // gas values overflow
	"AA95": errors.ErrorCodeRejectedByEPOrAccount, // out of gas
	"AA96": errors.ErrorCodeInvalidAggregator,     // invalid aggregator

	// v0.7 specific error codes
	"AA26": errors.ErrorCodeValidationFailed, // over verificationGasLimit (v0.7)
	"AA36": errors.ErrorCodeValidationFailed, // over paymasterVerificationGasLimit (v0.7)
}

// GetErrorCode returns the corresponding error code for a given error string
func getErrorCode(errStr string) int {
	if len(errStr) > 4 {
		errStr = errStr[:4]
	}
	if code, ok := errorCodeMap[errStr]; ok {
		return code
	}
	return errors.ErrorCodeInternal
}

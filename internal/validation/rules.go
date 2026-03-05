// Package validation provides anti-collusion and validator-independence rules
// for the AetherNet OCS settlement engine.
//
// The primary concern is self-dealing: an agent that is party to a transaction
// must not be allowed to verify that same transaction. Without this rule, two
// colluding agents can verify each other's fraudulent claims with no external
// check.
//
// Large transactions additionally require more validators to make collusion
// attacks proportionally more expensive.
package validation

import "github.com/Aethernet-network/aethernet/internal/crypto"

// MinValidators is the minimum number of independent validators required to
// settle a transaction. Testnet value; increase to 3 for mainnet.
const MinValidators = 1

// CanValidate reports whether a validator may verify a given event.
//
// Returns false when the validator is the sender or the recipient of the
// transaction — both are "parties to" the transaction and have a direct
// financial interest in its outcome. Self-dealing is prohibited because it
// would allow two colluding agents to verify each other's fraudulent claims.
//
// An empty recipientID ("") is treated as "no recipient" and is not compared.
func CanValidate(validatorID crypto.AgentID, senderID crypto.AgentID, recipientID crypto.AgentID) bool {
	if validatorID == senderID {
		return false
	}
	if recipientID != "" && validatorID == recipientID {
		return false
	}
	return true
}

// LargeTransactionThreshold returns the amount above which additional validator
// coverage is required. Set to 50% of the sender's trust limit.
func LargeTransactionThreshold(trustLimit uint64) uint64 {
	return trustLimit / 2
}

// ValidatorsRequired returns the number of independent validators needed for
// a transaction of the given amount relative to the sender's trust limit.
//
// Small transactions (≤ 50% of trust limit) require MinValidators.
// Large transactions (> 50% of trust limit) require 3 validators.
// Zero trust limit is treated as maximum caution (3 validators).
func ValidatorsRequired(amount uint64, trustLimit uint64) uint64 {
	if trustLimit == 0 {
		return 3
	}
	if amount > LargeTransactionThreshold(trustLimit) {
		return 3
	}
	return uint64(MinValidators)
}

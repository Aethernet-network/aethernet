package validation_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/validation"
)

// ---------------------------------------------------------------------------
// CanValidate
// ---------------------------------------------------------------------------

// TestCanValidate_Independent verifies that a validator with a different ID
// from both sender and recipient is allowed.
func TestCanValidate_Independent(t *testing.T) {
	validator := crypto.AgentID("validator")
	sender := crypto.AgentID("sender")
	recipient := crypto.AgentID("recipient")

	if !validation.CanValidate(validator, sender, recipient) {
		t.Error("CanValidate: independent validator should return true")
	}
}

// TestCanValidate_Sender verifies that the sender cannot validate their own
// transaction (self-dealing rejection).
func TestCanValidate_Sender(t *testing.T) {
	sender := crypto.AgentID("alice")
	recipient := crypto.AgentID("bob")

	if validation.CanValidate(sender, sender, recipient) {
		t.Error("CanValidate: sender should not be allowed to validate (want false)")
	}
}

// TestCanValidate_Recipient verifies that the recipient cannot validate the
// transaction they are receiving.
func TestCanValidate_Recipient(t *testing.T) {
	sender := crypto.AgentID("alice")
	recipient := crypto.AgentID("bob")

	if validation.CanValidate(recipient, sender, recipient) {
		t.Error("CanValidate: recipient should not be allowed to validate (want false)")
	}
}

// TestCanValidate_EmptyRecipient verifies that an empty recipient ID is not
// compared (generation events have no explicit recipient).
func TestCanValidate_EmptyRecipient(t *testing.T) {
	validator := crypto.AgentID("validator")
	sender := crypto.AgentID("sender")

	if !validation.CanValidate(validator, sender, "") {
		t.Error("CanValidate: should allow validation when recipient is empty")
	}
}

// ---------------------------------------------------------------------------
// ValidatorsRequired
// ---------------------------------------------------------------------------

// TestValidatorsRequired_Small verifies that small transactions return MinValidators.
func TestValidatorsRequired_Small(t *testing.T) {
	const trustLimit = uint64(100_000)
	// 30% of trust limit — below 50% threshold.
	amount := trustLimit * 30 / 100

	got := validation.ValidatorsRequired(amount, trustLimit)
	if got != uint64(validation.MinValidators) {
		t.Errorf("ValidatorsRequired(small): got %d, want %d", got, validation.MinValidators)
	}
}

// TestValidatorsRequired_Large verifies that transactions above 50% of the
// trust limit require 3 validators.
func TestValidatorsRequired_Large(t *testing.T) {
	const trustLimit = uint64(100_000)
	// 60% of trust limit — above 50% threshold.
	amount := trustLimit * 60 / 100

	got := validation.ValidatorsRequired(amount, trustLimit)
	if got != 3 {
		t.Errorf("ValidatorsRequired(large): got %d, want 3", got)
	}
}

// TestValidatorsRequired_AtThreshold verifies the boundary: exactly 50% of
// trust limit is NOT large (uses >, not >=).
func TestValidatorsRequired_AtThreshold(t *testing.T) {
	const trustLimit = uint64(100_000)
	amount := validation.LargeTransactionThreshold(trustLimit) // exactly 50%

	got := validation.ValidatorsRequired(amount, trustLimit)
	if got != uint64(validation.MinValidators) {
		t.Errorf("ValidatorsRequired(at threshold): got %d, want %d", got, validation.MinValidators)
	}
}

// TestValidatorsRequired_ZeroTrustLimit verifies that zero trust limit returns 3
// (maximum caution).
func TestValidatorsRequired_ZeroTrustLimit(t *testing.T) {
	got := validation.ValidatorsRequired(1000, 0)
	if got != 3 {
		t.Errorf("ValidatorsRequired(trustLimit=0): got %d, want 3", got)
	}
}

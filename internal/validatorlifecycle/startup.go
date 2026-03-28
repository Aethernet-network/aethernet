package validatorlifecycle

import (
	"errors"
	"fmt"
	"log/slog"
)

// ---------------------------------------------------------------------------
// Startup Safety Errors
// ---------------------------------------------------------------------------

var (
	// ErrNoSnapshot is returned when consensus services attempt to start
	// before a validator set snapshot has been established.
	ErrNoSnapshot = errors.New("validatorlifecycle: no validator set snapshot — consensus cannot start")

	// ErrManifestRequired is returned in lifecycle-enabled mode when no
	// genesis manifest is available.
	ErrManifestRequired = errors.New("validatorlifecycle: genesis validator manifest is required")

	// ErrDigestMismatch is returned when the expected snapshot digest does
	// not match the computed digest.
	ErrDigestMismatch = errors.New("validatorlifecycle: snapshot digest does not match expected value")
)

// ---------------------------------------------------------------------------
// Startup Safety Checks
// ---------------------------------------------------------------------------

// StartupCheck encapsulates the pre-consensus safety verification. It
// validates that the Reducer has been seeded and the snapshot is consistent.
type StartupCheck struct {
	// ExpectedDigest is the known-good snapshot digest that all nodes in
	// the network must agree on. Empty string disables the digest check
	// (suitable for testnet where the digest is derived at runtime).
	ExpectedDigest string

	// RequireManifest controls whether a missing manifest causes a fatal
	// error. When true (production), the node refuses to start consensus
	// without a valid manifest. When false (testnet default), the node
	// falls back to DefaultTestnetManifest.
	RequireManifest bool
}

// ValidateReducer checks that the Reducer is non-nil, has been seeded with
// at least one seat, and (if ExpectedDigest is set) produces the expected
// snapshot digest. Returns nil if all checks pass.
//
// This function should be called before any consensus-consuming code starts
// (before engine.Start, before node.Start, before auto-validator.Start).
func (sc *StartupCheck) ValidateReducer(r *Reducer) error {
	if r == nil {
		return ErrNoSnapshot
	}
	if r.SeatCount() == 0 {
		return fmt.Errorf("%w: reducer has zero seats", ErrNoSnapshot)
	}
	if r.Version() == 0 {
		return fmt.Errorf("%w: reducer version is 0 (not seeded)", ErrNoSnapshot)
	}

	snap := r.Snapshot()
	if snap.ActiveSeatCount == 0 {
		slog.Warn("validator lifecycle: snapshot has zero active seats — consensus may stall",
			"version", snap.Version,
			"total_seats", len(snap.Seats),
		)
	}

	if sc.ExpectedDigest != "" {
		actual := snap.ComputeDigest()
		if actual != sc.ExpectedDigest {
			return fmt.Errorf("%w: expected=%s actual=%s",
				ErrDigestMismatch, sc.ExpectedDigest, actual)
		}
		slog.Info("validator lifecycle: snapshot digest verified",
			"digest", actual,
			"version", snap.Version,
		)
	}

	return nil
}

// ValidateManifest checks that a manifest is available and valid. In
// RequireManifest mode, a nil manifest causes a fatal error.
func (sc *StartupCheck) ValidateManifest(m *GenesisManifest) error {
	if m == nil {
		if sc.RequireManifest {
			return ErrManifestRequired
		}
		return nil // testnet: nil is acceptable, DefaultTestnetManifest will be used
	}
	return m.Validate()
}

// ---------------------------------------------------------------------------
// Convenience
// ---------------------------------------------------------------------------

// DefaultStartupCheck returns a StartupCheck suitable for the 3-node testnet.
// Manifest is not required (falls back to DefaultTestnetManifest), and no
// expected digest is enforced (digest is derived at runtime and logged).
func DefaultStartupCheck() *StartupCheck {
	return &StartupCheck{
		RequireManifest: false,
		ExpectedDigest:  "",
	}
}

// ProductionStartupCheck returns a StartupCheck suitable for production
// networks. Manifest is required, and the expected digest must match.
func ProductionStartupCheck(expectedDigest string) *StartupCheck {
	return &StartupCheck{
		RequireManifest: true,
		ExpectedDigest:  expectedDigest,
	}
}

package roundprogress

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ProgressTransport abstracts broadcasting progress updates to peers.
type ProgressTransport interface {
	BroadcastProgressUpdate(payload []byte) error
}

// ProgressEmitter is a convenience helper for emitting signed progress updates.
// Used by the multi-voter (Prompt 5) to broadcast progress state.
type ProgressEmitter struct {
	localValidatorID string
	keyPair          *crypto.KeyPair
	transport        ProgressTransport
	aggregator       *ProgressAggregator
}

// NewProgressEmitter creates an emitter wired to the given transport and aggregator.
func NewProgressEmitter(
	localValidatorID string,
	keyPair *crypto.KeyPair,
	transport ProgressTransport,
	aggregator *ProgressAggregator,
) *ProgressEmitter {
	return &ProgressEmitter{
		localValidatorID: localValidatorID,
		keyPair:          keyPair,
		transport:        transport,
		aggregator:       aggregator,
	}
}

// Emit constructs a ProgressUpdate, signs it, broadcasts to peers, and
// applies it locally via the aggregator. Returns error if signing or
// broadcast fails; aggregator rejections are logged but not returned
// (the local apply is best-effort since our own updates should be valid).
func (e *ProgressEmitter) Emit(
	roundID, analyzerFamily string,
	phase ProgressPhase,
	generation uint64,
	evidence [32]byte,
	eta int64,
	reasonCode uint16,
	diagnostic string,
) error {
	nowUnix := time.Now().Unix()

	update := &ProgressUpdate{
		RoundID:            roundID,
		ValidatorID:        e.localValidatorID,
		AnalyzerFamily:     analyzerFamily,
		Phase:              phase,
		ProgressGeneration: generation,
		ProgressEvidence:   evidence,
		EstimatedReadyUnix: eta,
		ReasonCode:         reasonCode,
		DiagnosticText:     diagnostic,
		TimestampUnix:      nowUnix,
	}

	// Sign the update.
	if e.keyPair != nil {
		canonical, err := canonicalBytes(update)
		if err != nil {
			return fmt.Errorf("roundprogress: canonical bytes: %w", err)
		}
		sig, err := e.keyPair.Sign(canonical)
		if err != nil {
			return fmt.Errorf("roundprogress: sign: %w", err)
		}
		update.Signature = sig
	}

	// Broadcast to peers.
	payload, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("roundprogress: marshal update: %w", err)
	}
	if e.transport != nil {
		if err := e.transport.BroadcastProgressUpdate(payload); err != nil {
			return fmt.Errorf("roundprogress: broadcast: %w", err)
		}
	}

	// Apply locally (best-effort).
	if e.aggregator != nil {
		if err := e.aggregator.Apply(update, nowUnix); err != nil {
			// Log but don't fail — our own updates should be valid.
			fmt.Printf("roundprogress: local apply warning: %v\n", err)
		}
	}

	return nil
}

// canonicalBytes returns the deterministic byte representation of a
// ProgressUpdate for signing. Excludes the Signature field.
func canonicalBytes(update *ProgressUpdate) ([]byte, error) {
	// Create a copy without the signature for canonical serialization.
	canonical := struct {
		RoundID            string        `json:"round_id"`
		ValidatorID        string        `json:"validator_id"`
		AnalyzerFamily     string        `json:"analyzer_family"`
		Phase              ProgressPhase `json:"phase"`
		ProgressGeneration uint64        `json:"progress_generation"`
		ProgressEvidence   [32]byte      `json:"progress_evidence"`
		EstimatedReadyUnix int64         `json:"estimated_ready_unix"`
		ReasonCode         uint16        `json:"reason_code"`
		DiagnosticText     string        `json:"diagnostic_text"`
		TimestampUnix      int64         `json:"timestamp_unix"`
	}{
		RoundID:            update.RoundID,
		ValidatorID:        update.ValidatorID,
		AnalyzerFamily:     update.AnalyzerFamily,
		Phase:              update.Phase,
		ProgressGeneration: update.ProgressGeneration,
		ProgressEvidence:   update.ProgressEvidence,
		EstimatedReadyUnix: update.EstimatedReadyUnix,
		ReasonCode:         update.ReasonCode,
		DiagnosticText:     update.DiagnosticText,
		TimestampUnix:      update.TimestampUnix,
	}
	return json.Marshal(canonical)
}

// VerifySignature checks that the ProgressUpdate's signature is valid
// for the given public key. Used on receipt of remote progress updates.
func VerifySignature(update *ProgressUpdate, publicKey []byte) bool {
	canonical, err := canonicalBytes(update)
	if err != nil {
		return false
	}
	return crypto.Verify(publicKey, canonical, update.Signature)
}

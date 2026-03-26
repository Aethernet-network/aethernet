// Package evidence provides structured proof-of-work models and quality scoring
// for AetherNet task verification.
package evidence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Evidence captures the structured output of AI work submitted for a task.
type Evidence struct {
	Hash          string            `json:"hash"`
	OutputType    string            `json:"output_type"`
	OutputSize    uint64            `json:"output_size"`
	Summary       string            `json:"summary"`
	InputHash     string            `json:"input_hash,omitempty"`
	Metrics       map[string]string `json:"metrics,omitempty"`
	OutputPreview string            `json:"output_preview,omitempty"`
	OutputURL     string            `json:"output_url,omitempty"`

	// GenesisChain lists parent evidence hashes that this output derived from.
	// When an agent consumes verified output from another task as input, that
	// task's evidence hash appears here. Empty for leaf tasks with no upstream
	// dependencies. Entries are evidence hashes (SHA-256), not task/event IDs.
	GenesisChain []string `json:"genesis_chain,omitempty"`

	// Self-contained signature fields for portable evidence verification
	// without DAG access.
	ProducerSignature  string `json:"producer_signature,omitempty"`
	ProducerPublicKey  string `json:"producer_public_key,omitempty"`
	ValidatorSignature string `json:"validator_signature,omitempty"`
	ValidatorPublicKey string `json:"validator_public_key,omitempty"`
}

// Validate checks that required fields are present.
func (e *Evidence) Validate() error {
	if e.Hash == "" {
		return errors.New("evidence hash is required")
	}
	if e.OutputType == "" {
		return errors.New("output_type is required")
	}
	if e.OutputSize == 0 {
		return errors.New("output_size must be > 0")
	}
	if e.Summary == "" {
		return errors.New("summary is required")
	}
	return nil
}

// ComputeHash returns a sha256 hex hash prefixed with "sha256:" for raw output bytes.
func ComputeHash(output []byte) string {
	h := sha256.Sum256(output)
	return "sha256:" + hex.EncodeToString(h[:])
}

// Score holds the three assessment dimensions and the weighted overall.
type Score struct {
	Relevance    float64 `json:"relevance"`
	Completeness float64 `json:"completeness"`
	Quality      float64 `json:"quality"`
	Overall      float64 `json:"overall"`
}

// ComputeOverall recalculates Overall from the three sub-scores using fixed weights:
// Relevance×0.3 + Completeness×0.4 + Quality×0.3.
func (s *Score) ComputeOverall() float64 {
	s.Overall = s.Relevance*0.3 + s.Completeness*0.4 + s.Quality*0.3
	return s.Overall
}

// PassThreshold is the minimum Overall score required for automatic task approval.
const PassThreshold = 0.60

// CanonicalBytes returns the deterministic byte representation of the evidence
// packet for signing. Excludes signature fields. Same canonical form approach
// as AETHERNET-REQUEST-V1.
//
// Format:
//
//	AETHERNET-EVIDENCE-V1
//	hash:<hash>
//	output_type:<output_type>
//	output_size:<output_size>
//	summary:<summary>
//	input_hash:<input_hash>
//	genesis_chain:<comma-separated hashes>
//	metrics:<key=value pairs, sorted by key>
func CanonicalBytes(ev *Evidence) []byte {
	var b strings.Builder
	b.WriteString("AETHERNET-EVIDENCE-V1\n")
	b.WriteString("hash:" + ev.Hash + "\n")
	b.WriteString("output_type:" + ev.OutputType + "\n")
	b.WriteString(fmt.Sprintf("output_size:%d\n", ev.OutputSize))
	b.WriteString("summary:" + ev.Summary + "\n")
	b.WriteString("input_hash:" + ev.InputHash + "\n")
	b.WriteString("genesis_chain:" + strings.Join(ev.GenesisChain, ",") + "\n")

	// Metrics sorted by key for determinism.
	keys := make([]string, 0, len(ev.Metrics))
	for k := range ev.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var metricParts []string
	for _, k := range keys {
		metricParts = append(metricParts, k+"="+ev.Metrics[k])
	}
	b.WriteString("metrics:" + strings.Join(metricParts, ","))
	return []byte(b.String())
}

// VerifyEvidencePacket checks the producer and validator signatures on a
// standalone evidence packet without requiring DAG access. Returns nil if
// both signatures (when present) are valid.
func VerifyEvidencePacket(ev *Evidence) error {
	canonical := CanonicalBytes(ev)

	if ev.ProducerSignature != "" && ev.ProducerPublicKey != "" {
		pubBytes, err := hex.DecodeString(ev.ProducerPublicKey)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("evidence: invalid producer public key")
		}
		sig, err := hex.DecodeString(ev.ProducerSignature)
		if err != nil || len(sig) != ed25519.SignatureSize {
			return fmt.Errorf("evidence: invalid producer signature encoding")
		}
		if !ed25519.Verify(pubBytes, canonical, sig) {
			return fmt.Errorf("evidence: producer signature verification failed")
		}
	}

	if ev.ValidatorSignature != "" && ev.ValidatorPublicKey != "" {
		pubBytes, err := hex.DecodeString(ev.ValidatorPublicKey)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("evidence: invalid validator public key")
		}
		sig, err := hex.DecodeString(ev.ValidatorSignature)
		if err != nil || len(sig) != ed25519.SignatureSize {
			return fmt.Errorf("evidence: invalid validator signature encoding")
		}
		if !ed25519.Verify(pubBytes, canonical, sig) {
			return fmt.Errorf("evidence: validator signature verification failed")
		}
	}

	return nil
}

// SignEvidence signs the canonical evidence bytes with the given Ed25519
// private key and sets the producer signature and public key fields.
func SignEvidence(ev *Evidence, privateKey ed25519.PrivateKey) {
	canonical := CanonicalBytes(ev)
	sig := ed25519.Sign(privateKey, canonical)
	pub := privateKey.Public().(ed25519.PublicKey)
	ev.ProducerSignature = hex.EncodeToString(sig)
	ev.ProducerPublicKey = hex.EncodeToString(pub)
}

// SignEvidenceValidator signs the canonical evidence bytes with the validator's
// key and sets the validator signature and public key fields.
func SignEvidenceValidator(ev *Evidence, privateKey ed25519.PrivateKey) {
	canonical := CanonicalBytes(ev)
	sig := ed25519.Sign(privateKey, canonical)
	pub := privateKey.Public().(ed25519.PublicKey)
	ev.ValidatorSignature = hex.EncodeToString(sig)
	ev.ValidatorPublicKey = hex.EncodeToString(pub)
}

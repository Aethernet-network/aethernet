# Prompt 04 Plan — Bootstrap Analyzer Registry with Four Families

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Add the `AnalyzerRegistry`, `Analyzer` and `Family` types, and four bootstrap analyzer families to the existing `internal/verification/` package. Wrap existing evidence verifiers into the deterministic_heuristic family. Registry is constructed at startup but not yet consumed by the autovalidator.

## Package Layout

The `internal/verification/` package already exists with `service.go`, `inprocess.go`, `deterministic.go`, `subjective.go`, `consensus_check.go`, `replayability.go`. I'll add:

```
internal/verification/
  analyzer.go          — Analyzer, Family, AnalysisInput, AnalysisOutput types
  analyzer_registry.go — Registry implementation
  analyzer_registry_test.go
  analyzer_config.go   — ValidatorAnalyzerConfig + loader
  analyzer_config_test.go
  families/
    doc.go
    llm_semantic.go
    llm_semantic_test.go
    deterministic_heuristic.go
    deterministic_heuristic_test.go
    embedding_similarity.go
    embedding_similarity_test.go
    statistical_structural.go
    statistical_structural_test.go
deploy/validator-configs/
  node-1.yaml through node-5.yaml
```

## Types (analyzer.go)

- `FamilyID string` — 4 constants for bootstrap families
- `AnalyzerID string` — format `"<family>/<name>:<version>"`
- `AnalysisInput` — TaskID, Category, Title, Description, Content, EvidenceHash, SubmittedAt
- `AnalysisOutput` — AnalyzerID, Family, Version, ScoreBP (0-10000), ScoreBreakdown, Verdict, ArtifactHash, DurationMS, Warnings
- `Analyzer` interface — ID(), Family(), Version(), Analyze(ctx, input) (*AnalysisOutput, error), Calibration(category) bool
- `Family` struct — ID, Name, Description, FailureModes

## Registry (analyzer_registry.go)

In-memory `sync.RWMutex`-protected store of families and analyzers.

```go
type AnalyzerRegistry struct { ... }
func NewAnalyzerRegistry() *AnalyzerRegistry
func (r *AnalyzerRegistry) RegisterFamily(f Family) error
func (r *AnalyzerRegistry) RegisterAnalyzer(a Analyzer) error
func (r *AnalyzerRegistry) GetAnalyzer(id AnalyzerID) (Analyzer, error)
func (r *AnalyzerRegistry) ListFamilies() []Family
func (r *AnalyzerRegistry) ListAnalyzersByFamily(id FamilyID) []Analyzer
func (r *AnalyzerRegistry) ValidatorAnalyzers(cfg ValidatorAnalyzerConfig) ([]Analyzer, error)
```

Not an interface — concrete struct. The prompt's `Registry interface` is more indirection than needed at this stage. If we need to mock it later, we can extract an interface then.

## Config (analyzer_config.go)

```go
type ValidatorAnalyzerConfig struct {
    Families []ValidatorFamilyEntry `json:"families"`
}
type ValidatorFamilyEntry struct {
    Family   FamilyID   `json:"family"`
    Analyzer AnalyzerID `json:"analyzer"`
}
```

`LoadValidatorAnalyzerConfig(path string)` reads JSON (not YAML — the codebase uses JSON everywhere, no YAML dependency exists). Default config embedded for bootstrap.

## Four Bootstrap Families

### 1. deterministic_heuristic (heuristic:v1)
Wraps the existing `evidence.VerifierRegistry` (ContentVerifier/DataVerifier/CodeVerifier/KeywordVerifier). Calls `registry.Verify(ev, title, desc, budget, category)` and converts the `evidence.Score` to `AnalysisOutput`. Fully deterministic. No external dependencies.

### 2. statistical_structural (statistical:v1)
Pure math on the submission text:
- Token entropy (Shannon)
- Sentence length variance
- Type-token ratio
- Section heading count
- Citation/reference density

Fully deterministic. No external dependencies. Scores are computed from `AnalysisInput.SubmissionContent`.

### 3. embedding_similarity (embedding:v1)
Bootstrap: bag-of-words TF-IDF cosine similarity between task description and submission content. No external API needed. Deterministic.

Future: injected `EmbeddingClient` interface for real embedding APIs.

### 4. llm_semantic (claude_semantic:v1)
Injected `LLMClient interface { Complete(ctx, prompt string) (string, error) }`. For tests: mock client with canned responses. For production: wraps `anthropic.Client`. If no API key configured, the analyzer returns an error from `Analyze()` — it does NOT silently degrade.

The prompt is a structured evaluation asking for scores on completeness, factual coherence, depth, and category fit. Response is parsed as JSON.

## Artifact Hash

All analyzers compute `ArtifactHash = SHA-256(canonical JSON of intermediate analysis)`. For deterministic families, the same input MUST produce the same hash (testable). For LLM families, the hash varies per call but is recorded for audit.

## cmd/node/main.go Wiring

Construct registry, register 4 families + bootstrap analyzers. Load validator config from `AETHERNET_ANALYZER_CONFIG` env var (default: inline bootstrap config). Resolve analyzers. Log. Do NOT pass to autovalidator (prompt 05).

## Test Strategy

Per family: basic scoring, deterministic artifact hash (for deterministic families), high-quality vs low-quality inputs, correct FamilyID.

Registry: register/lookup, duplicate error, concurrent safety, validator config resolution.

Config: load, validate empty, validate unknown family.

## Dependencies

- No new external Go dependencies
- `internal/evidence` package (for wrapping existing verifiers)
- Existing `internal/verification/` types

## What is NOT in Scope

- Autovalidator integration (prompt 05)
- On-chain analyzer registration (future)
- Slashing for fabricated analysis (prompt 09)
- YAML config (use JSON — no new dependency)

package canary

// DefaultCanaryCorpus returns the built-in set of 20 canary templates, derived
// from the same benchmark cases used in internal/harness/corpus.go.
//
// These are templates — they carry no IDs or timestamps. The Injector calls
// NewCanaryTask() from each template when actually injecting, so each live
// canary gets a unique ID, timestamp, and eventually a protocol TaskID.
//
// The corpus spans three categories (code, research, writing) with four canary
// types per category: known_good, known_bad, adversarial, edge_case.
func DefaultCanaryCorpus() []CanaryTask {
	return []CanaryTask{
		// ── Code category (8 cases) ─────────────────────────────────────────

		{
			Category:         "code",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.65,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 0.90,
			GroundTruthHash:  "sha256:b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.20,
			GroundTruthHash:  "sha256:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.65,
			GroundTruthHash:  "sha256:d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5",
		},
		{
			Category:         "code",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.20,
			ExpectedMaxScore: 0.65,
			GroundTruthHash:  "sha256:e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6",
		},
		{
			Category:         "code",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.35,
			GroundTruthHash:  "sha256:f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1",
		},
		{
			Category:         "code",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.80,
			GroundTruthHash:  "sha256:a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2",
		},
		{
			Category:         "code",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.50,
			ExpectedMaxScore: 0.90,
			GroundTruthHash:  "sha256:b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3",
		},

		// ── Research category (6 cases) ─────────────────────────────────────

		{
			Category:         "research",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.70,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4",
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5",
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.10,
			GroundTruthHash:  "sha256:e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6",
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7",
		},
		{
			Category:         "research",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.35,
			ExpectedMaxScore: 0.80,
			GroundTruthHash:  "sha256:a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8",
		},
		{
			Category:         "research",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.55,
			ExpectedMaxScore: 0.95,
			GroundTruthHash:  "sha256:b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9",
		},

		// ── Writing category (6 cases) ──────────────────────────────────────

		{
			Category:         "writing",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0",
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.45,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1",
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.15,
			GroundTruthHash:  "sha256:e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2",
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.50,
			GroundTruthHash:  "sha256:f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3",
		},
		{
			Category:         "writing",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.15,
			ExpectedMaxScore: 0.60,
			GroundTruthHash:  "sha256:a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4",
		},
		{
			Category:         "writing",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5",
		},
	}
}

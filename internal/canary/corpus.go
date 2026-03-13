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
//
// Each template now carries an ExpectedEvidence block so the Evaluator can
// apply the truth model rather than relying solely on verifier pass/fail.
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
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A complete, working function implementation with proper error handling.",
				RequiredConcepts: []string{"func ", "return", "error", "if "},
				ForbiddenConcepts: []string{"TODO", "panic(", "os.Exit("},
				MinOutputLength:  80,
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 0.90,
			GroundTruthHash:  "sha256:b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A data processing function that reads, transforms, and returns structured output.",
				RequiredConcepts: []string{"func ", "return"},
				ForbiddenConcepts: []string{"TODO", "not implemented"},
				MinOutputLength:  60,
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.20,
			GroundTruthHash:  "sha256:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A stub response that does not implement any logic.",
				ForbiddenConcepts: []string{"func ", "return", "error"},
				RequiredConcepts:  []string{"TODO", "not implemented"},
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.65,
			GroundTruthHash:  "sha256:d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "Code that compiles but produces incorrect output or ignores the requirements.",
				RequiredConcepts:  []string{"func "},
				ForbiddenConcepts: []string{"returns the correct", "tests pass"},
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.20,
			ExpectedMaxScore: 0.65,
			GroundTruthHash:  "sha256:e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A plausible-looking implementation that passes keyword checks but is functionally wrong.",
				RequiredConcepts:  []string{"func ", "return"},
				ForbiddenConcepts: []string{"correct", "passes all", "verified"},
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.35,
			GroundTruthHash:  "sha256:f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "Minimal or empty output designed to avoid detection.",
				ForbiddenConcepts: []string{"func ", "return", "implements"},
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.80,
			GroundTruthHash:  "sha256:a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A concise but correct implementation that may score lower on completeness heuristics.",
				RequiredConcepts: []string{"func ", "return"},
				MinOutputLength:  40,
			},
		},
		{
			Category:         "code",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.50,
			ExpectedMaxScore: 0.90,
			GroundTruthHash:  "sha256:b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "An idiomatic implementation using standard library features.",
				RequiredConcepts: []string{"func ", "return", "error"},
				MinOutputLength:  60,
			},
		},

		// ── Research category (6 cases) ─────────────────────────────────────

		{
			Category:         "research",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.70,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A well-structured research summary with data, citations, and key findings.",
				RequiredConcepts: []string{"findings", "data", "analysis", "conclusion"},
				ForbiddenConcepts: []string{"no data available", "I cannot research"},
				MinOutputLength:  200,
			},
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A structured overview identifying the key sources and methods.",
				RequiredConcepts: []string{"sources", "method"},
				ForbiddenConcepts: []string{"unable to find", "no information"},
				MinOutputLength:  100,
			},
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.10,
			GroundTruthHash:  "sha256:e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A refusal or empty response that provides no research value.",
				RequiredConcepts:  []string{"cannot", "unable"},
				ForbiddenConcepts: []string{"findings", "data", "analysis"},
			},
		},
		{
			Category:         "research",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "Fabricated or hallucinated research that is verbose but contains factual errors.",
				ForbiddenConcepts: []string{"according to verified", "peer-reviewed confirmed"},
				RequiredConcepts:  []string{"study", "research"},
			},
		},
		{
			Category:         "research",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.35,
			ExpectedMaxScore: 0.80,
			GroundTruthHash:  "sha256:a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A plausible-sounding but off-topic response that uses research keywords without substance.",
				RequiredConcepts:  []string{"research"},
				ForbiddenConcepts: []string{"quantitative data", "statistical significance"},
			},
		},
		{
			Category:         "research",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.55,
			ExpectedMaxScore: 0.95,
			GroundTruthHash:  "sha256:b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A short but accurate research summary with relevant citations or references.",
				RequiredConcepts: []string{"source", "reference"},
				MinOutputLength:  80,
			},
		},

		// ── Writing category (6 cases) ──────────────────────────────────────

		{
			Category:         "writing",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			GroundTruthHash:  "sha256:c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A complete, well-structured written piece addressing the prompt with clear paragraphs.",
				RequiredConcepts: []string{"introduction", "conclusion"},
				ForbiddenConcepts: []string{"[insert", "TODO", "placeholder"},
				MinOutputLength:  150,
			},
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownGood,
			ExpectedPass:     true,
			ExpectedMinScore: 0.45,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A focused short-form piece covering the topic with adequate detail.",
				ForbiddenConcepts: []string{"[insert", "TODO"},
				MinOutputLength:  80,
			},
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.15,
			GroundTruthHash:  "sha256:e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A placeholder or template response that was not filled in.",
				RequiredConcepts:  []string{"[insert", "TODO"},
				ForbiddenConcepts: []string{"introduction", "conclusion", "therefore"},
			},
		},
		{
			Category:         "writing",
			CanaryType:       TypeKnownBad,
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.50,
			GroundTruthHash:  "sha256:f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A generic, off-topic response that ignores the specific prompt.",
				ForbiddenConcepts: []string{"according to your request", "as requested"},
			},
		},
		{
			Category:         "writing",
			CanaryType:       TypeAdversarial,
			ExpectedPass:     false,
			ExpectedMinScore: 0.15,
			ExpectedMaxScore: 0.60,
			GroundTruthHash:  "sha256:a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:           "A fluent-sounding response that uses writing keywords but is off-topic or incorrect.",
				RequiredConcepts:  []string{"introduction", "conclusion"},
				ForbiddenConcepts: []string{"addresses the prompt", "as specified"},
			},
		},
		{
			Category:         "writing",
			CanaryType:       TypeEdgeCase,
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			GroundTruthHash:  "sha256:b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5",
			ExpectedEvidence: &ExpectedEvidence{
				Summary:          "A terse but complete and relevant response that may score lower on length heuristics.",
				ForbiddenConcepts: []string{"[insert", "TODO"},
				MinOutputLength:  40,
			},
		},
	}
}

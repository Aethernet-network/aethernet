package evidence

import "testing"

func TestVerify_RelevantOutput(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:          "sha256:abc",
		OutputType:    "text",
		OutputSize:    500,
		Summary:       "Analysed climate change data and produced a detailed report on temperature trends.",
		OutputPreview: "The analysis shows a consistent upward trend in global temperatures over the past century, with a 1.5°C increase recorded since pre-industrial levels.",
	}
	score, passed := v.Verify(ev, "Climate Analysis Report", "Analyse climate change temperature data and produce a written report.", 1_000_000)
	if !passed {
		t.Fatalf("expected relevant output to pass, got score %.3f (overall %.3f)", score.Relevance, score.Overall)
	}
	if score.Relevance < 0.3 {
		t.Fatalf("expected higher relevance for on-topic summary, got %.3f", score.Relevance)
	}
}

func TestVerify_IrrelevantOutput(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:       "sha256:xyz",
		OutputType: "text",
		OutputSize: 200,
		Summary:    "I cooked pasta for dinner and it was delicious.",
	}
	score, passed := v.Verify(ev, "Write a financial model for Q4 revenue forecasting", "Build a detailed Excel-style financial model with revenue projections.", 500_000)
	if passed {
		t.Fatalf("expected irrelevant output to fail, got score %.3f (overall %.3f)", score.Relevance, score.Overall)
	}
	if score.Relevance > 0.2 {
		t.Fatalf("expected low relevance for off-topic summary, got %.3f", score.Relevance)
	}
}

func TestVerify_SubstantialOutput(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:       "sha256:sub",
		OutputType: "text",
		OutputSize: 5000,
		Summary:    "Wrote a detailed analysis with data charts.",
	}
	score, _ := v.Verify(ev, "Analysis task", "Write a thorough analysis.", 1_000_000)
	if score.Completeness < 0.5 {
		t.Fatalf("expected completeness >= 0.5 for large output, got %.3f", score.Completeness)
	}
}

func TestVerify_TinyOutput(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:       "sha256:tiny",
		OutputType: "text",
		OutputSize: 10, // well below 100 minimum
		Summary:    "Done.",
	}
	score, _ := v.Verify(ev, "Task", "Do something.", 1_000_000)
	if score.Completeness > 0.25 {
		t.Fatalf("expected low completeness for tiny output, got %.3f", score.Completeness)
	}
}

func TestVerify_WithMetrics(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:       "sha256:met",
		OutputType: "json",
		OutputSize: 300,
		Summary:    "Generated JSON data with performance metrics.",
		Metrics:    map[string]string{"accuracy": "0.95", "f1": "0.92"},
	}
	score, _ := v.Verify(ev, "Generate metrics", "Produce a JSON performance report.", 500_000)
	if score.Quality < 0.6 {
		t.Fatalf("expected quality bonus for metrics, got %.3f", score.Quality)
	}
}

func TestVerify_PassesThreshold(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:          "sha256:good",
		OutputType:    "text",
		OutputSize:    800,
		Summary:       "Wrote a comprehensive report covering all required topics including research methodology, findings, and recommendations.",
		OutputPreview: "This report presents a thorough analysis of market conditions. Section 1 covers methodology used for data collection. Section 2 analyses trends observed in Q3. Section 3 offers strategic recommendations based on findings.",
		Metrics:       map[string]string{"word_count": "1200", "sources": "15"},
		InputHash:     "sha256:inputabc",
	}
	score, passed := v.Verify(ev, "Market Research Report", "Write a comprehensive market research report covering methodology, findings, and recommendations.", 2_000_000)
	if !passed {
		t.Fatalf("expected high-quality evidence to pass threshold %.2f, got overall %.3f (R=%.3f C=%.3f Q=%.3f)",
			PassThreshold, score.Overall, score.Relevance, score.Completeness, score.Quality)
	}
}

func TestVerify_FailsThreshold(t *testing.T) {
	v := NewVerifier()
	ev := &Evidence{
		Hash:       "sha256:bad",
		OutputType: "text",
		OutputSize: 5, // too small
		Summary:    "",
	}
	score, passed := v.Verify(ev, "Complex Data Analysis", "Perform a multi-step analysis of large datasets.", 5_000_000)
	if passed {
		t.Fatalf("expected minimal evidence to fail threshold %.2f, got overall %.3f", PassThreshold, score.Overall)
	}
}

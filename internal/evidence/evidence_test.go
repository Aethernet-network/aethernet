package evidence

import (
	"strings"
	"testing"
)

func TestValidate_Valid(t *testing.T) {
	ev := &Evidence{
		Hash:       "sha256:abc123",
		OutputType: "text",
		OutputSize: 500,
		Summary:    "I wrote a report about climate change impacts.",
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("expected valid evidence to pass, got: %v", err)
	}
}

func TestValidate_MissingHash(t *testing.T) {
	ev := &Evidence{
		OutputType: "text",
		OutputSize: 100,
		Summary:    "some summary",
	}
	err := ev.Validate()
	if err == nil {
		t.Fatal("expected error for missing hash, got nil")
	}
	if !strings.Contains(err.Error(), "hash") {
		t.Fatalf("expected error about hash, got: %v", err)
	}
}

func TestValidate_MissingType(t *testing.T) {
	ev := &Evidence{
		Hash:       "sha256:abc",
		OutputSize: 100,
		Summary:    "some summary",
	}
	err := ev.Validate()
	if err == nil {
		t.Fatal("expected error for missing output_type, got nil")
	}
	if !strings.Contains(err.Error(), "output_type") {
		t.Fatalf("expected error about output_type, got: %v", err)
	}
}

func TestComputeHash(t *testing.T) {
	data := []byte("hello aethernet")
	h1 := ComputeHash(data)
	h2 := ComputeHash(data)
	if h1 != h2 {
		t.Fatal("ComputeHash is not deterministic")
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("expected sha256: prefix, got: %s", h1)
	}
	if len(h1) != len("sha256:")+64 {
		t.Fatalf("unexpected hash length: %d", len(h1))
	}
	h3 := ComputeHash([]byte("different"))
	if h1 == h3 {
		t.Fatal("different inputs produced same hash")
	}
}

func TestScore_ComputeOverall(t *testing.T) {
	s := &Score{
		Relevance:    0.8,
		Completeness: 0.7,
		Quality:      0.6,
	}
	overall := s.ComputeOverall()
	// 0.8*0.3 + 0.7*0.4 + 0.6*0.3 = 0.24 + 0.28 + 0.18 = 0.70
	expected := 0.70
	if overall < expected-0.001 || overall > expected+0.001 {
		t.Fatalf("expected overall ~%.3f, got %.3f", expected, overall)
	}
	if s.Overall != overall {
		t.Fatal("ComputeOverall did not store result in s.Overall")
	}
}

func TestScore_PassThreshold(t *testing.T) {
	passing := &Score{Relevance: 0.8, Completeness: 0.7, Quality: 0.6}
	passing.ComputeOverall()
	if passing.Overall < PassThreshold {
		t.Fatalf("expected %.3f >= PassThreshold %.3f", passing.Overall, PassThreshold)
	}

	failing := &Score{Relevance: 0.1, Completeness: 0.2, Quality: 0.3}
	failing.ComputeOverall()
	if failing.Overall >= PassThreshold {
		t.Fatalf("expected %.3f < PassThreshold %.3f", failing.Overall, PassThreshold)
	}
}

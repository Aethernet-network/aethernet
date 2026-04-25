package lint

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"unicode"
)

// suppressionPragmaRE matches the per-line pragma:
//
//	// settlement:lint ignore "<reason>"
//
// The pragma must appear on the same physical line as the read site
// (typically as a trailing line comment) OR on the line immediately
// preceding the read site. Both forms are supported.
var suppressionPragmaRE = regexp.MustCompile(`settlement:lint\s+ignore\s+"([^"]*)"`)

// minPragmaChars and minPragmaWords are the validation thresholds for
// a suppression reason. Mirrors the projections/lint D4 rule (≥20
// chars, ≥3 words) so contributors carry one mental model across both
// lints.
const (
	minPragmaChars = 20
	minPragmaWords = 3
)

// matchReadsAgainstManifest produces the lint findings for a single
// (manifest, read-set) pair. Returns the three finding categories:
//
//   - undeclared: read sites in scope whose file:line is NOT declared
//     in the manifest AND not suppressed by a valid pragma.
//   - insufficient: pragmas present but with a reason that fails the
//     ≥20-char / ≥3-word validation.
//   - staleManifest: manifest source_locations whose file:line does not
//     resolve to any extracted read site (drift detection on the
//     manifest side: the manifest references a line that no longer
//     reads anything in the source tree).
//
// The matcher reads pragmas directly from the file on disk so it can
// handle both same-line trailing comments and preceding-line comments
// without depending on the AST visitor preserving comment-association
// metadata.
func matchReadsAgainstManifest(manifest *Manifest, reads []ReadSite, activeLines map[string]bool) (
	undeclared []UndeclaredRead,
	insufficient []InsufficientPragma,
	staleManifest []StaleManifestEntry,
) {
	declared := manifest.DeclaredLocations()

	// Build a per-file pragma cache so we don't re-read each source file
	// per read-site.
	pragmaCache := newPragmaCache()

	// Track which manifest locations we observed during extraction so we
	// can flag stale entries.
	observed := make(map[string]bool)

	for _, r := range reads {
		key := r.Key()
		if _, ok := declared[key]; ok {
			observed[key] = true
			continue
		}

		// Not declared. Check suppression pragma at this read's line.
		status, reason, charCount, wordCount := pragmaCache.Lookup(r.AbsFile, r.Line)
		switch status {
		case pragmaSuppressed:
			continue
		case pragmaInsufficient:
			insufficient = append(insufficient, InsufficientPragma{
				File:      r.File,
				Line:      r.Line,
				Reason:    reason,
				CharCount: charCount,
				WordCount: wordCount,
			})
			continue
		}
		undeclared = append(undeclared, UndeclaredRead{
			File:        r.File,
			Line:        r.Line,
			Kind:        r.Kind,
			Detail:      r.Detail,
			PackagePath: r.PackagePath,
		})
	}

	// Stale-manifest detection: a declared file:line is stale ONLY if
	// the line is not observed as a read AND has no AST activity at
	// all (declarations, types, imports, etc.). The manifest convention
	// permits source_locations to point at definition lines (e.g. const
	// declarations like `workerShareBP = 7300` at line 20), so a line
	// with AST activity but no read shape is NOT stale — it's a
	// declaration-site reference.
	//
	// We also exclude declared lines pointing at files outside the
	// extractor's scope (e.g. dispatcher reads in internal/dispatch/);
	// those stale-checks belong to the dispatcher-side check, not here.
	for key, entryIDs := range declared {
		if observed[key] {
			continue
		}
		file := stripFileFromKey(key)
		if !isScopedFilePath(file) {
			continue
		}
		if activeLines != nil && activeLines[key] {
			// Line has AST activity (likely a declaration site that
			// the manifest legitimately references). Not stale.
			continue
		}
		// Truly orphaned reference — line has no AST node at all.
		for _, id := range entryIDs {
			staleManifest = append(staleManifest, StaleManifestEntry{
				File:    file,
				Line:    lineFromKey(key),
				EntryID: id,
			})
		}
	}

	// Stable ordering for diffable output.
	sort.Slice(undeclared, func(i, j int) bool {
		if undeclared[i].File != undeclared[j].File {
			return undeclared[i].File < undeclared[j].File
		}
		if undeclared[i].Line != undeclared[j].Line {
			return undeclared[i].Line < undeclared[j].Line
		}
		return undeclared[i].Detail < undeclared[j].Detail
	})
	sort.Slice(insufficient, func(i, j int) bool {
		if insufficient[i].File != insufficient[j].File {
			return insufficient[i].File < insufficient[j].File
		}
		return insufficient[i].Line < insufficient[j].Line
	})
	sort.Slice(staleManifest, func(i, j int) bool {
		if staleManifest[i].File != staleManifest[j].File {
			return staleManifest[i].File < staleManifest[j].File
		}
		if staleManifest[i].Line != staleManifest[j].Line {
			return staleManifest[i].Line < staleManifest[j].Line
		}
		return staleManifest[i].EntryID < staleManifest[j].EntryID
	})
	return undeclared, insufficient, staleManifest
}

// stripFileFromKey extracts the file portion of a "file:line" key.
func stripFileFromKey(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}

// lineFromKey extracts the integer line number from a "file:line" key.
// Returns 0 on parse failure (caller surfaces 0 as "line unknown").
func lineFromKey(key string) int {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			n := 0
			for _, r := range key[i+1:] {
				if r < '0' || r > '9' {
					return 0
				}
				n = n*10 + int(r-'0')
			}
			return n
		}
	}
	return 0
}

// isScopedFilePath reports whether a manifest-relative file path falls
// under one of the scoped package prefixes the lint scans.
func isScopedFilePath(file string) bool {
	for _, prefix := range scopedPackagePrefixes {
		// `internal/settlement/foo.go` starts with `internal/settlement/`.
		if len(file) > len(prefix)+1 &&
			file[:len(prefix)] == prefix &&
			file[len(prefix)] == '/' {
			return true
		}
	}
	return false
}

// Pragma parsing.

type pragmaStatus int

const (
	pragmaAbsent       pragmaStatus = iota // no pragma at this line
	pragmaSuppressed                       // valid pragma with sufficient reason
	pragmaInsufficient                     // pragma present, reason too short / too few words
)

// pragmaCache reads source files once and answers per-line pragma
// queries from memory. Each line's pragma status is computed by looking
// at (a) the trailing comment on the read line itself, and (b) the
// trailing comment on the preceding line. This matches the convention
// used by other Go lints that allow contributors to place a `// foo:lint
// ignore` comment immediately above the offending code.
type pragmaCache struct {
	files map[string][]string // absPath → lines (1-indexed: index 0 is sentinel)
}

func newPragmaCache() *pragmaCache {
	return &pragmaCache{files: make(map[string][]string)}
}

// Lookup returns the pragma status at absPath:line.
func (p *pragmaCache) Lookup(absPath string, line int) (pragmaStatus, string, int, int) {
	lines := p.load(absPath)
	if line < 1 || line >= len(lines) {
		return pragmaAbsent, "", 0, 0
	}
	// Same line, then preceding line.
	for _, candidate := range []int{line, line - 1} {
		if candidate < 1 || candidate >= len(lines) {
			continue
		}
		if status, reason, cc, wc := classifyPragma(lines[candidate]); status != pragmaAbsent {
			return status, reason, cc, wc
		}
	}
	return pragmaAbsent, "", 0, 0
}

func (p *pragmaCache) load(absPath string) []string {
	if cached, ok := p.files[absPath]; ok {
		return cached
	}
	f, err := os.Open(absPath)
	if err != nil {
		// Cache the empty result so we don't retry every line.
		p.files[absPath] = []string{""}
		return p.files[absPath]
	}
	defer f.Close()
	out := []string{""} // sentinel so index matches 1-based line numbers
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	p.files[absPath] = out
	return out
}

// classifyPragma scans a single source line for the suppression pragma
// and returns its status.
func classifyPragma(line string) (pragmaStatus, string, int, int) {
	match := suppressionPragmaRE.FindStringSubmatch(line)
	if match == nil {
		return pragmaAbsent, "", 0, 0
	}
	reason := match[1]
	cc := len(reason)
	wc := countWords(reason)
	if cc >= minPragmaChars && wc >= minPragmaWords {
		return pragmaSuppressed, reason, cc, wc
	}
	return pragmaInsufficient, reason, cc, wc
}

// countWords returns the number of whitespace-separated tokens in s.
func countWords(s string) int {
	inWord := false
	count := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			count++
		}
	}
	return count
}

// formatUndeclared renders a single UndeclaredRead diagnostic.
func formatUndeclared(u UndeclaredRead) string {
	return fmt.Sprintf(`settlement/lint: UNDECLARED SETTLEMENT READ
  Location:   %s:%d
  Kind:       %s
  Detail:     %s
  Package:    %s
  Remediation: EITHER
                 (a) declare this read in docs/architecture/settlement-input-manifest.yaml
                     under an appropriate `+"`inputs:`"+` entry's `+"`source_locations:`"+` list.
                     The manifest is the canonical audit of every value that flows into
                     settlement derivation; new reads MUST be classified before merging.
                     See F5 Phase 5A.1 §3.1 for the classification taxonomy.
                 OR
                 (b) suppress the lint at this read site. Add a comment on the same line
                     OR the immediately-preceding line:
                          // settlement:lint ignore "<reason>"
                     The reason must be at least %d characters AND at least %d
                     whitespace-separated words, and must explain why the read does
                     NOT need a manifest entry (e.g. helper-only logic that does not
                     influence canonical payout derivation).
`, u.File, u.Line, u.Kind, u.Detail, u.PackagePath, minPragmaChars, minPragmaWords)
}

// formatInsufficient renders a single InsufficientPragma diagnostic.
func formatInsufficient(s InsufficientPragma) string {
	return fmt.Sprintf(`settlement/lint: INSUFFICIENT SUPPRESSION JUSTIFICATION
  File:       %s
  Line:       %d
  Got reason: %q (%d chars, %d words)
  Required:   at least %d characters AND at least %d whitespace-separated words
  Required:   replace with an actionable justification (e.g. "this read is a
              helper-only logging call that never flows into payout derivation;
              tracked in audit-doc §X.Y")
`, s.File, s.Line, s.Reason, s.CharCount, s.WordCount, minPragmaChars, minPragmaWords)
}

// formatStaleManifest renders a single StaleManifestEntry diagnostic.
func formatStaleManifest(s StaleManifestEntry) string {
	return fmt.Sprintf(`settlement/lint: STALE MANIFEST ENTRY
  Manifest declares: %s:%d (entry %q)
  Diagnosis:         no read-shape AST node was found at this file:line during
                     extraction. The manifest may have drifted from the source
                     tree (the read was deleted, moved, or restructured).
  Remediation:       update the entry's source_locations in
                     docs/architecture/settlement-input-manifest.yaml to point
                     at the current read site, OR remove the entry if the read
                     is gone entirely (and document the removal in the entry's
                     change log per F5 Phase 5A.1 §9.4 — keep with
                     target_classification: removed for 5D regression detection).
`, s.File, s.Line, s.EntryID)
}

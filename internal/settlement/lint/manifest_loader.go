package lint

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ManifestPath is the canonical relative path to the settlement input
// manifest, resolved from the repository root.
const ManifestPath = "docs/architecture/settlement-input-manifest.yaml"

// Manifest is the parsed view of settlement-input-manifest.yaml.
type Manifest struct {
	// Inputs is every entry under the top-level `inputs:` section.
	Inputs []ManifestEntry

	// OutOfScope is every entry under the top-level `out_of_scope:`
	// section. These rows are captured for forward workstreams; their
	// source_locations are NOT enforced by this lint per F5 5A.4.c scope.
	OutOfScope []ManifestEntry

	// SourcePath is the absolute path the manifest was loaded from
	// (diagnostic use).
	SourcePath string
}

// ManifestEntry is a single input row.
type ManifestEntry struct {
	ID              string
	Source          string
	SourceLocations []SourceLocation
}

// SourceLocation is a parsed file:line tuple from a manifest entry's
// `source_locations` list. The trailing `# comment` (if any) is
// preserved in Comment for diagnostic clarity.
type SourceLocation struct {
	// File is the file path AS WRITTEN in the manifest (relative to
	// repo root, e.g. `internal/settlement/verification_consensus_settler.go`).
	File string

	// Line is the 1-based line number.
	Line int

	// Comment is the trailing `# ...` annotation if present, sans `#`.
	Comment string

	// Raw is the original source_locations string (diagnostic use).
	Raw string

	// EntryID is the manifest entry that declared this location
	// (diagnostic use; supports multi-entry coverage of the same line).
	EntryID string
}

// Key returns the canonical "file:line" string used as the lookup key
// against extracted reads.
func (s SourceLocation) Key() string {
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

// LoadManifest reads and parses the manifest file at path. Returns
// (nil, error) if the file is unreadable or malformed in a way that
// prevents extracting the inputs/out_of_scope sections — both halt
// conditions per F5 5A.4.c.
//
// This is a deliberately narrow YAML parser: it only understands the
// subset of YAML the manifest uses (top-level mappings, list-of-mappings
// under `inputs:` / `out_of_scope:`, scalar values, list-of-scalars
// under `source_locations:`). It does NOT attempt to be a general-
// purpose YAML parser; introducing a new module dependency for a single
// hand-written file is rejected per the task brief ("do not add new
// dependencies if existing YAML parsers work" — and none are vendored).
func LoadManifest(path string) (*Manifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("settlement-lint: resolve manifest path %q: %w", path, err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("settlement-lint: open manifest %q: %w", abs, err)
	}
	defer f.Close()

	m := &Manifest{SourcePath: abs}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// State machine:
	//   section  — which top-level section we are currently inside
	//              ("", "inputs", "out_of_scope", "<other>")
	//   entry    — the entry currently being assembled (nil if outside)
	//   inLocs   — whether we are inside a source_locations: list
	//   entryIndent — column at which the current entry's `- id:` started
	//                 (used to know when an entry ends)
	var (
		section     string
		entry       *ManifestEntry
		inLocs      bool
		entryIndent = -1
		lineNum     int
	)

	flushEntry := func() {
		if entry == nil {
			return
		}
		// Stamp EntryID into each location for diagnostic clarity.
		for i := range entry.SourceLocations {
			entry.SourceLocations[i].EntryID = entry.ID
		}
		switch section {
		case "inputs":
			m.Inputs = append(m.Inputs, *entry)
		case "out_of_scope":
			m.OutOfScope = append(m.OutOfScope, *entry)
		}
		entry = nil
		inLocs = false
		entryIndent = -1
	}

	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		trimRight := strings.TrimRight(raw, " \t\r")

		// Skip blank lines and pure-comment lines without losing entry context.
		stripped := strings.TrimSpace(trimRight)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}

		indent := leadingSpaces(trimRight)

		// Top-level section header (zero indent, ends with colon, no value).
		if indent == 0 && strings.HasSuffix(stripped, ":") {
			flushEntry()
			section = strings.TrimSuffix(stripped, ":")
			continue
		}

		// Within inputs / out_of_scope sections.
		if section == "inputs" || section == "out_of_scope" {
			// Inside a source_locations list, child `- ` items are
			// locations, NOT new entries. Decide first by indent: a
			// `- ` at the entry's own indent is a NEW entry; a `- ` at
			// deeper indent is a source_locations child.
			if inLocs && strings.HasPrefix(stripped, "- ") && indent > entryIndent {
				value := strings.TrimSpace(strings.TrimPrefix(stripped, "- "))
				applySourceLocation(entry, value, lineNum, abs)
				continue
			}

			// New entry begins with `- id: <name>` at the section's entry
			// indent (typically 2 spaces under the section header).
			if strings.HasPrefix(stripped, "- ") && (entryIndent < 0 || indent <= entryIndent) {
				// Flush the previous entry, then start a new one.
				flushEntry()
				entry = &ManifestEntry{}
				entryIndent = indent
				inLocs = false

				// Parse the leading key:value on the same line as the dash.
				kv := strings.TrimPrefix(stripped, "- ")
				key, val, ok := splitKeyValue(kv)
				if ok {
					applyEntryKV(entry, key, val)
				}
				continue
			}

			if entry == nil {
				continue
			}

			// Source-locations sub-list header.
			if strings.HasPrefix(stripped, "source_locations:") {
				inLocs = true
				// Same-line tail (e.g. `source_locations: []`) handled below.
				rest := strings.TrimSpace(strings.TrimPrefix(stripped, "source_locations:"))
				if rest != "" && rest != "[]" {
					applySourceLocation(entry, rest, lineNum, abs)
				}
				continue
			}

			// Indent stepped back at or above the entry's own indent
			// without a `- ` — this is a structural break; flush.
			if indent <= entryIndent && !strings.HasPrefix(stripped, "- ") {
				// Could be a sibling field like `out_of_scope:` at zero
				// indent; the top-level section handler above caught
				// that. Otherwise it's a malformed line — skip safely.
				continue
			}

			// Any other key:value at the entry's indent + 2 (or matching
			// the source_locations indent) closes the source_locations list
			// and falls back to scalar parsing.
			if inLocs && indent <= entryIndent+2 {
				inLocs = false
			}

			// Generic key:value within the entry. We only care about a few
			// keys (id, source); everything else is parsed-and-ignored to
			// stay compatible with the manifest's free-form fields.
			if !inLocs {
				if key, val, ok := splitKeyValue(stripped); ok {
					applyEntryKV(entry, key, val)
				}
				continue
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("settlement-lint: read manifest: %w", err)
	}
	flushEntry()

	if len(m.Inputs) == 0 {
		return nil, fmt.Errorf(
			"settlement-lint: manifest at %q parsed but contained zero `inputs:` entries — "+
				"refusing to operate on an empty manifest (halt condition per F5 5A.4.c)",
			abs)
	}
	return m, nil
}

// applyEntryKV writes a recognized key:value pair into entry. Unknown
// keys are silently accepted (manifest carries many descriptive fields
// the lint does not consume).
func applyEntryKV(entry *ManifestEntry, key, val string) {
	switch key {
	case "id":
		entry.ID = unquote(val)
	case "source":
		entry.Source = unquote(val)
	}
}

// applySourceLocation parses a single source_locations list value of
// shape `internal/path/file.go:NN  # optional comment` and appends the
// parsed SourceLocation to entry. Malformed entries are appended with
// File="" so the caller (the lint's manifest-side integrity check) can
// surface them as warnings.
//
// The manifestLine and manifestPath parameters are reserved for future
// per-entry diagnostic enrichment (so a malformed source_locations
// entry can be traced back to the manifest line that declared it).
// Currently unused but retained on the signature so callers don't
// re-thread state when richer diagnostics arrive.
func applySourceLocation(entry *ManifestEntry, value string, manifestLine int, manifestPath string) {
	_ = manifestLine
	_ = manifestPath
	raw := value
	loc := SourceLocation{Raw: raw}

	// Split off the trailing comment if any.
	if i := strings.Index(value, "#"); i >= 0 {
		loc.Comment = strings.TrimSpace(value[i+1:])
		value = strings.TrimSpace(value[:i])
	}

	// Strip surrounding quotes if the manifest happens to use them.
	value = unquote(value)

	colon := strings.LastIndex(value, ":")
	if colon < 0 {
		// Not a file:line — accept as raw, leave File empty.
		entry.SourceLocations = append(entry.SourceLocations, loc)
		return
	}
	filePart := value[:colon]
	linePart := value[colon+1:]

	// Manifest occasionally uses comma-separated multi-line shorthand
	// (e.g. ":47,111"). Expand into multiple SourceLocations.
	// Also handle ":280-289" range shorthand by emitting an entry for
	// each line in the range.
	for _, lineSpec := range strings.Split(linePart, ",") {
		lineSpec = strings.TrimSpace(lineSpec)
		if lineSpec == "" {
			continue
		}
		if dash := strings.Index(lineSpec, "-"); dash > 0 {
			start, errStart := strconv.Atoi(strings.TrimSpace(lineSpec[:dash]))
			end, errEnd := strconv.Atoi(strings.TrimSpace(lineSpec[dash+1:]))
			if errStart != nil || errEnd != nil || end < start {
				// Append unparsed; surfaced later as integrity warning.
				bad := loc
				bad.File = filePart
				bad.Line = 0
				entry.SourceLocations = append(entry.SourceLocations, bad)
				continue
			}
			for ln := start; ln <= end; ln++ {
				entry.SourceLocations = append(entry.SourceLocations, SourceLocation{
					File:    filePart,
					Line:    ln,
					Comment: loc.Comment,
					Raw:     raw,
				})
			}
			continue
		}
		ln, err := strconv.Atoi(lineSpec)
		if err != nil {
			bad := loc
			bad.File = filePart
			entry.SourceLocations = append(entry.SourceLocations, bad)
			continue
		}
		entry.SourceLocations = append(entry.SourceLocations, SourceLocation{
			File:    filePart,
			Line:    ln,
			Comment: loc.Comment,
			Raw:     raw,
		})
	}
}

// DeclaredLocations returns the union of source_locations across all
// `inputs:` entries (out_of_scope rows are NOT included — those are
// captured for forward workstreams and their reads are explicitly out of
// scope per F5 5A.4.c).
//
// The returned map is keyed by `file:line` (the SourceLocation.Key()
// form) and the value is the list of entry IDs that declared the
// location. Multiple entries may declare the same line; this is normal
// (e.g. a single line that reads two payload fields).
func (m *Manifest) DeclaredLocations() map[string][]string {
	out := make(map[string][]string)
	for _, e := range m.Inputs {
		for _, loc := range e.SourceLocations {
			if loc.File == "" || loc.Line == 0 {
				continue
			}
			key := loc.Key()
			out[key] = append(out[key], e.ID)
		}
	}
	return out
}

// AllSourceLocations returns every parsed SourceLocation across all
// `inputs:` entries (with EntryID stamped in). Used by the manifest-
// integrity check to verify each declared file:line resolves to a real
// line in the source tree.
func (m *Manifest) AllSourceLocations() []SourceLocation {
	var out []SourceLocation
	for _, e := range m.Inputs {
		out = append(out, e.SourceLocations...)
	}
	return out
}

// splitKeyValue parses "key: value" or "key:" into (key, value, ok).
// A line that doesn't contain a colon returns ok=false.
func splitKeyValue(s string) (string, string, bool) {
	colon := strings.Index(s, ":")
	if colon < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:colon])
	val := strings.TrimSpace(s[colon+1:])
	return key, val, true
}

// unquote strips a single layer of surrounding `"..."` or `'...'`
// quoting if present. The manifest mostly uses unquoted scalars, so
// this is mostly a no-op.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// leadingSpaces returns the number of leading space characters in s.
// Tabs are counted as 1 each (the manifest is space-indented).
func leadingSpaces(s string) int {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return i
		}
	}
	return len(s)
}

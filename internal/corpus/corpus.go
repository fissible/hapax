// Package corpus builds deterministic snapshots of prose files.
package corpus

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

const (
	admissionSchemaVersion = "admission-v1"
	dedupeAlgorithmVersion = "exact-sha256-v1"
	splitAlgorithmVersion  = "content-sha256-v1"
)

// CheckStates returns the closed qualification-verdict vocabulary.
func CheckStates() []CheckState {
	return []CheckState{CheckNotPerformed, CheckPassed, CheckFailed, CheckSkippedByPolicy}
}

// Admissions returns the closed admission vocabulary.
func Admissions() []Admission {
	return []Admission{Eligible, RejectedTooShort, RejectedNotUTF8, RejectedDuplicate, RejectedToolOutput}
}

// OverlapAlgorithm identifies the exact-hash overlap screen and its version.
const OverlapAlgorithm = "overlap-exact-hash-v1"

// Role states how a snapshot will participate in a calibration comparison.
type Role string

const (
	RoleAuthor     Role = "author"
	RoleDistractor Role = "distractor"
)

// Splits returns the closed split vocabulary.
func Splits() []Split { return []Split{Train, Calibrate, Test, Draft} }

// CheckState records whether an optional qualification check has run.
type CheckState string

const (
	CheckNotPerformed    CheckState = "not-performed"
	CheckPassed          CheckState = "passed"
	CheckFailed          CheckState = "failed"
	CheckSkippedByPolicy CheckState = "skipped-by-policy"
)

// CheckStatus describes the state and version of a qualification check.
type CheckStatus struct {
	State   CheckState
	Reason  string
	Version string
}

// Admission is the outcome of the mechanical corpus gates.
type Admission string

const (
	Eligible          Admission = "eligible"
	RejectedTooShort  Admission = "rejected-too-short"
	RejectedNotUTF8   Admission = "rejected-not-utf8"
	RejectedDuplicate Admission = "rejected-duplicate"
	// RejectedToolOutput holds a paragraph this tool published. Not corpus
	// material: indexing it would make this tool's own prose the author's
	// measured style on the next run.
	RejectedToolOutput Admission = "rejected-tool-output"
)

// Split is a stable, content-derived dataset partition.
type Split string

const (
	Train     Split = "train"
	Calibrate Split = "calibrate"
	Test      Split = "test"
	Draft     Split = "draft"
)

// SplitWeights control the relative probability of each split.
type SplitWeights struct {
	Train, Calibrate, Test int
}

// Policy configures a corpus snapshot.
type Policy struct {
	Register         string
	Role             Role
	MinLexicalTokens int
	SplitSeed        string
	Splits           SplitWeights
	// PublishedParagraphs are the hashes of paragraphs this tool published.
	// NIL means no screen: the check stays not-performed, nothing new is
	// rejected, and no document is parsed for it. Non-nil and EMPTY is still a
	// screen — the state of every first index, where nothing has been published
	// yet — and reports that it ran.
	PublishedParagraphs map[string]bool
}

// ToolOutputCheckVersion identifies the tool-output screen and its version. It
// is an input to the snapshot identity, so changing it re-indexes every corpus.
const ToolOutputCheckVersion = "tool-output-exact-hash-v1"

// DefaultPolicy returns the v1 corpus admission policy for register.
func DefaultPolicy(register string) Policy {
	return Policy{Register: register, Role: RoleAuthor, MinLexicalTokens: 1, SplitSeed: "hapax-v1", Splits: SplitWeights{Train: 8, Calibrate: 1, Test: 1}}
}

// Document records a source file and its mechanical admission outcome.
type Document struct {
	Path, ContentHash                  string
	Bytes, Tokens, LexicalTokens       int
	ModTime                            time.Time
	Register                           string
	Split                              Split
	Admission                          Admission
	RejectionDetail                    string
	RejectionOffset                    int
	DuplicateOf                        string
	Contamination, Language, Structure CheckStatus
}

// Snapshot is a deterministic corpus membership record.
type Snapshot struct {
	ID                                                                        string
	Policy                                                                    Policy
	Documents                                                                 []Document
	Contamination, Language, Structure, GitProvenance, NearDuplicateDetection CheckStatus
	// ToolOutput records the screen for paragraphs this tool published. Unlike
	// its five siblings it is implemented, so not-performed here means no screen
	// was supplied rather than no screen exists.
	ToolOutput CheckStatus
	overlaps   map[string]OverlapReport
}

// SharedDocument identifies an eligible document that appears in both sides
// of an overlap screen.
type SharedDocument struct {
	ContentHash, AuthorPath, DistractorPath string
}

// OverlapReport is the per-author attestation from an exact-hash screen.
type OverlapReport struct {
	AuthorSnapshotID, DistractorSnapshotID string
	Algorithm                              string
	State                                  CheckState
	AuthorEligible, DistractorEligible     int
	Shared                                 []SharedDocument
}

var notPerformed = CheckStatus{
	State:   CheckNotPerformed,
	Reason:  "not implemented in corpus v1",
	Version: "",
}

// Walk reads .md and .txt files beneath root and builds a snapshot.
func Walk(root string, p Policy) (*Snapshot, error) {
	if err := validatePolicy(p); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("lstat corpus root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("corpus root must be a directory, not a file or symlink")
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type().IsRegular() && (strings.HasSuffix(entry.Name(), ".md") || strings.HasSuffix(entry.Name(), ".txt")) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk corpus root: %w", err)
	}
	sort.Slice(paths, func(i, j int) bool {
		return slashPath(root, paths[i]) < slashPath(root, paths[j])
	})

	documents := make([]Document, 0, len(paths))
	// Carried beside the documents rather than on them: the verdict is decided
	// while the admitted bytes are still in scope, but it may only be APPLIED
	// after dedupe and the length gate have had their say.
	holdsPublished := make([]bool, 0, len(paths))
	for _, path := range paths {
		doc, published, err := readDocument(root, path, p)
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
		holdsPublished = append(holdsPublished, published)
	}

	screened := 0
	seen := make(map[string]string, len(documents))
	for i := range documents {
		doc := &documents[i]
		if doc.Admission == RejectedNotUTF8 {
			continue
		}
		if winner, duplicate := seen[doc.ContentHash]; duplicate {
			doc.Admission = RejectedDuplicate
			doc.DuplicateOf = winner
			continue
		}
		seen[doc.ContentHash] = doc.Path
		if doc.LexicalTokens < p.MinLexicalTokens {
			doc.Admission = RejectedTooShort
			continue
		}
		// Last, so an earlier rejection keeps the evidence membership() hashes.
		if holdsPublished[i] {
			doc.Admission = RejectedToolOutput
			screened++
			continue
		}
		doc.Admission = Eligible
		doc.Split = splitFor(doc.ContentHash, p)
	}

	s := &Snapshot{
		Policy:                 p,
		Documents:              documents,
		Contamination:          notPerformed,
		Language:               notPerformed,
		Structure:              notPerformed,
		GitProvenance:          notPerformed,
		NearDuplicateDetection: notPerformed,
		ToolOutput:             toolOutputStatus(p, screened),
	}
	s.ID = identity.HashInputs(s.IdentityInputs())
	return s, nil
}

func validatePolicy(p Policy) error {
	if p.Register == "" {
		return errors.New("corpus register must not be empty")
	}
	if p.Role != RoleAuthor && p.Role != RoleDistractor {
		return fmt.Errorf("corpus role must be %q or %q", RoleAuthor, RoleDistractor)
	}
	if p.MinLexicalTokens < 0 {
		return errors.New("minimum lexical tokens must not be negative")
	}
	if p.SplitSeed == "" {
		return errors.New("split seed must not be empty")
	}
	if p.Splits.Train <= 0 || p.Splits.Calibrate <= 0 || p.Splits.Test <= 0 {
		return errors.New("split weights must be positive")
	}
	return nil
}

// readDocument reads one file and reports whether any of its paragraphs is one
// this tool published. The second value is false whenever no screen was
// supplied, and for a file that could not be admitted at all — such a file has
// no paragraphs to screen.
func readDocument(root, path string, p Policy) (Document, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return Document{}, false, fmt.Errorf("open corpus file %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Document{}, false, fmt.Errorf("stat open corpus file %q: %w", path, err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return Document{}, false, fmt.Errorf("read corpus file %q: %w", path, err)
	}

	doc := Document{
		Path:            slashPath(root, path),
		ModTime:         info.ModTime(),
		Register:        p.Register,
		RejectionOffset: -1,
		Contamination:   notPerformed,
		Language:        notPerformed,
		Structure:       notPerformed,
	}
	admitted, err := text.Admit(raw)
	if err != nil {
		doc.ContentHash = identity.HashBytes(raw)
		doc.Bytes = len(raw)
		var admissionErr *text.AdmissionError
		if errors.As(err, &admissionErr) {
			doc.Admission = RejectedNotUTF8
			doc.RejectionOffset = admissionErr.Offset
			doc.RejectionDetail = admissionErr.Error()
			return doc, false, nil
		}
		return Document{}, false, fmt.Errorf("admit corpus file %q: %w", path, err)
	}
	analysis := admitted.Raw()
	doc.ContentHash = identity.HashBytes(analysis)
	doc.Bytes = len(analysis)
	tokens := admitted.Tokens()
	doc.Tokens = len(tokens)
	for _, token := range tokens {
		if token.Lexical {
			doc.LexicalTokens++
		}
	}
	return doc, holdsPublishedParagraph(admitted, p.PublishedParagraphs), nil
}

// holdsPublishedParagraph reports whether any paragraph of admitted is one the
// store recorded as published.
//
// The hash is over the ADMITTED bytes, the same span rewrite.Loop records a
// candidate against — a file whose only difference from a rewrite is a byte
// order mark would otherwise pass. Every included leaf is screened, with no
// length floor: the store's hashes come from ParagraphLeaves under the fitted
// MinParagraphLexicalTokens, which this package cannot see, and Policy's own
// floor counts a whole document rather than a paragraph.
func holdsPublishedParagraph(admitted *text.Document, publishedParagraphs map[string]bool) bool {
	if publishedParagraphs == nil {
		return false
	}
	raw := admitted.Raw()
	for _, leaf := range admitted.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		if publishedParagraphs[identity.HashBytes(raw[leaf.Span.Offset:leaf.Span.Offset+leaf.Span.Length])] {
			return true
		}
	}
	return false
}

// toolOutputStatus reports the screen. A nil set is no screen at all, which is
// not the same claim as a screen that found nothing — and the difference is the
// whole point of the field: `not-performed` is what this said before the screen
// existed.
func toolOutputStatus(p Policy, screened int) CheckStatus {
	if p.PublishedParagraphs == nil {
		return CheckStatus{State: CheckNotPerformed, Reason: "no published-paragraph set was supplied"}
	}
	if screened == 0 {
		return CheckStatus{State: CheckPassed, Reason: "no document holds a paragraph this tool published", Version: ToolOutputCheckVersion}
	}
	return CheckStatus{State: CheckFailed, Reason: fmt.Sprintf("%d document(s) hold a paragraph this tool published", screened), Version: ToolOutputCheckVersion}
}

func slashPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func splitFor(contentHash string, p Policy) Split {
	// Explicit length framing makes the domain-separated inputs unambiguous.
	digest := sha256.Sum256(identity.Frame(splitAlgorithmVersion, p.SplitSeed, contentHash))
	// The first 64 bits are enough for a uniform bucket while avoiding integer
	// conversion of the entire digest or a fallible hex round trip.
	bucket := binary.BigEndian.Uint64(digest[:8])
	total := uint64(p.Splits.Train + p.Splits.Calibrate + p.Splits.Test)
	point := bucket % total
	if point < uint64(p.Splits.Train) {
		return Train
	}
	if point < uint64(p.Splits.Train+p.Splits.Calibrate) {
		return Calibrate
	}
	return Test
}

// Eligible returns the mechanically eligible documents in snapshot order.
func (s *Snapshot) Eligible() []Document {
	documents := make([]Document, 0, len(s.Documents))
	for _, doc := range s.Documents {
		if doc.Admission == Eligible {
			documents = append(documents, doc)
		}
	}
	return documents
}

// RequiresChecksBeforeUse reports whether unimplemented qualification checks
// still prevent the snapshot from being used as qualified prose.
func (s *Snapshot) RequiresChecksBeforeUse() bool {
	return s.Contamination.State == CheckNotPerformed ||
		s.Language.State == CheckNotPerformed ||
		s.Structure.State == CheckNotPerformed ||
		s.GitProvenance.State == CheckNotPerformed ||
		s.NearDuplicateDetection.State == CheckNotPerformed
}

// IdentityInputs returns the complete, reviewable inputs to the snapshot ID.
func (s *Snapshot) IdentityInputs() map[string]string {
	// Always present, empty when no screen ran: a screened walk and an
	// unscreened one admitting the same documents are still different corpora,
	// and an enumeration whose members come and go is not reviewable.
	toolOutputCheckVersion := ""
	if s.Policy.PublishedParagraphs != nil {
		toolOutputCheckVersion = ToolOutputCheckVersion
	}
	return map[string]string{
		"admission-schema-version":  admissionSchemaVersion,
		"dedupe-algorithm-version":  dedupeAlgorithmVersion,
		"extensions":                ".md,.txt",
		"hidden-file-policy":        "skip-dot-prefixed-files-and-directories-v1",
		"membership":                membership(s.Documents),
		"min-lexical-tokens":        strconv.Itoa(s.Policy.MinLexicalTokens),
		"register":                  s.Policy.Register,
		"role":                      string(s.Policy.Role),
		"split-algorithm-version":   splitAlgorithmVersion,
		"split-seed":                s.Policy.SplitSeed,
		"split-weights":             fmt.Sprintf("%d,%d,%d", s.Policy.Splits.Train, s.Policy.Splits.Calibrate, s.Policy.Splits.Test),
		"text-contract-version":     text.ContractVersion,
		"tool-output-check-version": toolOutputCheckVersion,
	}
}

func membership(documents []Document) string {
	parts := make([]string, 0, len(documents))
	for _, doc := range documents {
		parts = append(parts, string(identity.Frame(doc.Path, doc.ContentHash, string(doc.Admission), doc.DuplicateOf, strconv.Itoa(doc.RejectionOffset))))
	}
	return string(identity.Frame(parts...))
}

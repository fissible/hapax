// Package eval builds held-out, provenance-stamped segment populations.
package eval

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/text"
)

// Class identifies the source side of an evaluation segment.
type Class string

const (
	ClassAuthor     Class = "author"
	ClassDistractor Class = "distractor"
)

// Segment is one admitted paragraph and the document that supplied it.
type Segment struct {
	Class                      Class
	DocumentHash, DocumentPath string
	// Index is the zero-based position among paragraphs admitted by this set's
	// lexical-token floor, not a stable document paragraph locator.
	Index, LexicalTokens int
	Vector               features.Vector
}

// Requirements select one held-out split and set the minimum class sizes.
type Requirements struct {
	Split                                    corpus.Split
	MinAuthorSegments, MinDistractorSegments int
}

// Set is the complete labelled population for a future discrimination measure.
type Set struct {
	ID, ProfileID, AuthorSnapshotID, DistractorSnapshotID string
	Split                                                 corpus.Split
	FeatureSetVersion                                     int
	FeatureManifestDigest                                 string
	MinParagraphLexicalTokens                             int
	Segments                                              []Segment
	AuthorSegments, DistractorSegments                    int
}

// SetIdentity declares the immutable populations from which a Set is made.
type SetIdentity struct {
	Fitted            profile.Fitted
	AuthorSnapshotID  string
	AuthorMembers     []string
	DistractorPoolID  string
	DistractorMembers []string
}

// NewSet validates caller-supplied held-out segments under the same rules as
// Extract, plus membership checks required when the segments were not read here.
func NewSet(declared SetIdentity, author, distractor []Segment, req Requirements) (*Set, error) {
	if err := validateRequirements(req); err != nil {
		return nil, err
	}
	if declared.Fitted.ID == "" || declared.AuthorSnapshotID == "" || declared.DistractorPoolID == "" {
		return nil, ErrMissingInput
	}
	if declared.Fitted.Unit != profile.UnitParagraph || declared.Fitted.FeatureSetVersion != features.SetVersion || declared.Fitted.FeatureManifestDigest != features.ManifestDigest() || declared.Fitted.MinParagraphLexicalTokens <= 0 || len(declared.Fitted.Stats) == 0 {
		return nil, ErrMissingInput
	}
	authors, err := memberSet(declared.AuthorMembers)
	if err != nil {
		return nil, err
	}
	distractors, err := memberSet(declared.DistractorMembers)
	if err != nil {
		return nil, err
	}
	if len(author) < req.MinAuthorSegments {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewAuthorSegments, len(author), req.MinAuthorSegments)
	}
	if len(distractor) < req.MinDistractorSegments {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewDistractorSegments, len(distractor), req.MinDistractorSegments)
	}
	for _, s := range author {
		if s.Class != ClassAuthor || s.DocumentHash == "" || !authors[s.DocumentHash] {
			return nil, ErrMissingInput
		}
	}
	for _, s := range distractor {
		if s.Class != ClassDistractor || s.DocumentHash == "" || !distractors[s.DocumentHash] {
			return nil, ErrMissingInput
		}
	}
	all := append(append([]Segment(nil), author...), distractor...)
	set := &Set{ProfileID: declared.Fitted.ID, AuthorSnapshotID: declared.AuthorSnapshotID, DistractorSnapshotID: declared.DistractorPoolID, Split: req.Split, FeatureSetVersion: declared.Fitted.FeatureSetVersion, FeatureManifestDigest: declared.Fitted.FeatureManifestDigest, MinParagraphLexicalTokens: declared.Fitted.MinParagraphLexicalTokens, Segments: all, AuthorSegments: len(author), DistractorSegments: len(distractor)}
	set.ID = identity.HashInputs(map[string]string{"author-snapshot-id": set.AuthorSnapshotID, "distractor-snapshot-id": set.DistractorSnapshotID, "feature-manifest-digest": set.FeatureManifestDigest, "feature-set-version": strconv.Itoa(set.FeatureSetVersion), "min-paragraph-lexical-tokens": strconv.Itoa(set.MinParagraphLexicalTokens), "profile-id": set.ProfileID, "split": string(req.Split)})
	return set, nil
}

func memberSet(members []string) (map[string]bool, error) {
	if len(members) == 0 {
		return nil, ErrMissingInput
	}
	out := make(map[string]bool, len(members))
	for _, member := range members {
		if member == "" || out[member] {
			return nil, ErrMissingInput
		}
		out[member] = true
	}
	return out, nil
}

var (
	ErrMissingInput             = errors.New("eval missing input")
	ErrInvalidRequirements      = errors.New("eval invalid requirements")
	ErrSplitNotHeldOut          = errors.New("eval split is not held out")
	ErrProfileMismatch          = errors.New("eval profile does not match author snapshot")
	ErrAuthorRole               = errors.New("eval author snapshot has wrong role")
	ErrDistractorRole           = errors.New("eval distractor snapshot has wrong role")
	ErrNotOverlapScreened       = errors.New("eval distractor is not overlap screened")
	ErrSourceChanged            = errors.New("eval source changed since snapshot")
	ErrTooFewAuthorSegments     = errors.New("eval has too few author segments")
	ErrTooFewDistractorSegments = errors.New("eval has too few distractor segments")
)

// Extract produces one complete held-out population. It deliberately builds
// locally until every guard has passed so callers never receive partial data.
func Extract(authorRoot string, author *corpus.Snapshot, distractorRoot string, distractor *corpus.Snapshot, prof *profile.Profile, req Requirements) (*Set, error) {
	if err := validateRequirements(req); err != nil {
		return nil, err
	}
	if author == nil || distractor == nil || prof == nil {
		return nil, ErrMissingInput
	}
	if author.Policy.Role != corpus.RoleAuthor {
		return nil, fmt.Errorf("%w: got %q", ErrAuthorRole, author.Policy.Role)
	}
	if distractor.Policy.Role != corpus.RoleDistractor {
		return nil, fmt.Errorf("%w: got %q", ErrDistractorRole, distractor.Policy.Role)
	}
	if prof.SnapshotID != author.ID {
		return nil, fmt.Errorf("%w: profile snapshot %q, author snapshot %q", ErrProfileMismatch, prof.SnapshotID, author.ID)
	}
	if err := distractor.NonOverlappingWith(author.ID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotOverlapScreened, err)
	}

	authorSegments, err := segments(authorRoot, author, req.Split, ClassAuthor, prof.Requirements.MinParagraphLexicalTokens)
	if err != nil {
		return nil, err
	}
	distractorSegments, err := segments(distractorRoot, distractor, req.Split, ClassDistractor, prof.Requirements.MinParagraphLexicalTokens)
	if err != nil {
		return nil, err
	}
	if len(authorSegments) < req.MinAuthorSegments {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewAuthorSegments, len(authorSegments), req.MinAuthorSegments)
	}
	if len(distractorSegments) < req.MinDistractorSegments {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewDistractorSegments, len(distractorSegments), req.MinDistractorSegments)
	}

	fitted, err := prof.Fitted()
	if err != nil {
		return nil, err
	}
	authorMembers, distractorMembers := []string{}, []string{}
	for _, d := range author.Eligible() {
		if d.Split == req.Split {
			authorMembers = append(authorMembers, d.ContentHash)
		}
	}
	for _, d := range distractor.Eligible() {
		if d.Split == req.Split {
			distractorMembers = append(distractorMembers, d.ContentHash)
		}
	}
	return NewSet(SetIdentity{Fitted: fitted, AuthorSnapshotID: author.ID, AuthorMembers: authorMembers, DistractorPoolID: distractor.ID, DistractorMembers: distractorMembers}, authorSegments, distractorSegments, req)
}

func validateRequirements(req Requirements) error {
	if req.MinAuthorSegments <= 0 || req.MinDistractorSegments <= 0 {
		return ErrInvalidRequirements
	}
	switch req.Split {
	case corpus.Train:
		return ErrSplitNotHeldOut
	case corpus.Calibrate, corpus.Test:
		return nil
	default:
		return ErrInvalidRequirements
	}
}

func segments(root string, snap *corpus.Snapshot, split corpus.Split, class Class, floor int) ([]Segment, error) {
	var out []Segment
	for _, document := range snap.Eligible() {
		if document.Split != split {
			continue
		}
		doc, err := readVerified(root, document)
		if err != nil {
			return nil, err
		}
		paragraphs, err := profile.ParagraphVectors(doc, floor)
		if err != nil {
			return nil, fmt.Errorf("extract paragraphs from snapshot document %q: %w", document.Path, err)
		}
		for index, vector := range paragraphs.Vectors {
			out = append(out, Segment{Class: class, DocumentHash: document.ContentHash, DocumentPath: document.Path, Index: index, LexicalTokens: vector.LexicalTokens, Vector: vector})
		}
	}
	return out, nil
}

func readVerified(root string, document corpus.Document) (*text.Document, error) {
	doc, err := snapshot.ReadVerified(root, document.Path, document.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceChanged, err)
	}
	return doc, nil
}

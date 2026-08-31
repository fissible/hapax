// Package ingest translates a verified corpus snapshot into the persisted graph.
package ingest

import (
	"fmt"
	"os"
	"reflect"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

type deps struct {
	ReadFile  func(string) ([]byte, error)
	Structure func(*text.Document) *text.Node
}

func realDeps() deps {
	return deps{ReadFile: os.ReadFile, Structure: func(d *text.Document) *text.Node { return d.Structure(text.DefaultStructureOptions()) }}
}

func Snapshot(root string, snap *corpus.Snapshot) (store.SnapshotWrite, error) {
	return snapshotWith(root, snap, realDeps())
}
func snapshotWith(root string, snap *corpus.Snapshot, d deps) (store.SnapshotWrite, error) {
	if snap == nil {
		return store.SnapshotWrite{}, fmt.Errorf("ingest snapshot is nil")
	}
	w := store.SnapshotWrite{PolicyDigest: identity.HashInputs(snap.IdentityInputs())}
	for _, source := range snap.Documents {
		doc := store.Document{Path: source.Path, ContentHash: source.ContentHash, Register: source.Register, Split: source.Split, Admission: source.Admission, Language: source.Language.State}
		if source.Admission == corpus.Eligible {
			admitted, err := readVerified(root, source, d)
			if err != nil {
				return store.SnapshotWrite{}, err
			}
			rootNode := structure(d, admitted)
			leaves, _, err := profile.ParagraphLeaves(admitted, rootNode, profile.DefaultRequirements().MinParagraphLexicalTokens)
			if err != nil {
				return store.SnapshotWrite{}, err
			}
			vectors := map[*text.Node]profile.ParagraphLeaf{}
			for _, leaf := range leaves {
				vectors[leaf.Node] = leaf
			}
			for ordinal, node := range rootNode.Leaves() {
				stored := store.Node{Ordinal: ordinal, Kind: node.Kind, Role: node.Role, Containers: node.Containers, Offset: node.Span.Offset, Length: node.Span.Length, Included: node.Included, Exclusion: node.Exclusion}
				if leaf, ok := vectors[node]; ok {
					vector := leaf.Vector
					stored.Vector = &vector
				}
				doc.Nodes = append(doc.Nodes, stored)
			}
		}
		w.Documents = append(w.Documents, doc)
	}
	members := make([]string, 0, len(w.Documents))
	for _, doc := range w.Documents {
		members = append(members, doc.Path+"="+doc.ContentHash)
	}
	w.ID = identity.HashInputs(map[string]string{"policy": w.PolicyDigest, "documents": string(identity.Frame(members...))})
	return w, nil
}
func readVerified(root string, document corpus.Document, d deps) (*text.Document, error) {
	// Read through snapshot first so a caller cannot persist a graph for bytes
	// different from the snapshot. The seam read is deliberately retained for tests.
	_ = d.ReadFile
	return snapshot.ReadVerified(root, document.Path, document.ContentHash)
}
func structure(d deps, doc *text.Document) *text.Node {
	return reflect.ValueOf(d).FieldByName("Structure").Interface().(func(*text.Document) *text.Node)(doc)
}

func CalibrateStandardizations(root string, snap *corpus.Snapshot, p *profile.Profile) ([]deviation.Standardization, error) {
	return calibrateStandardizationsWith(root, snap, p, realDeps())
}
func calibrateStandardizationsWith(root string, snap *corpus.Snapshot, p *profile.Profile, d deps) ([]deviation.Standardization, error) {
	if snap == nil {
		return nil, fmt.Errorf("ingest snapshot is nil")
	}
	var out []deviation.Standardization
	for _, source := range snap.Eligible() {
		if source.Split != corpus.Calibrate {
			continue
		}
		admitted, err := readVerified(root, source, d)
		if err != nil {
			return nil, err
		}
		leaves, _, err := profile.ParagraphLeaves(admitted, structure(d, admitted), p.Requirements.MinParagraphLexicalTokens)
		if err != nil {
			return nil, err
		}
		for _, leaf := range leaves {
			standardized, err := deviation.Standardize(leaf.Vector, p, corpus.Calibrate)
			if err != nil {
				return nil, err
			}
			out = append(out, standardized)
		}
	}
	return out, nil
}

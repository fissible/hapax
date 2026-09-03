package store_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/store"
)

// A rejected document carries no split, and the schema says so.
//
// This shipped broken. `split` was required to be one of train, calibrate or
// test, and a document that was rejected — too short, not UTF-8, or a
// near-duplicate — is in none of them, so it carried the empty string and the
// write was refused. The effect was that `hapax index` failed OUTRIGHT on any
// corpus containing one.
//
// Every fixture in this repository is generated and admits every file, so
// nothing caught it until a real archive did: three revisions of one resume are
// near-duplicates of each other, which is what a real archive looks like. The
// same class as #69, where every fixture was plain top-level paragraphs and the
// container grammar was unstorable for twelve slices.
func TestARejectedDocumentIsStoredWithoutASplit(t *testing.T) {
	for _, admission := range []corpus.Admission{
		corpus.RejectedDuplicate, corpus.RejectedTooShort, corpus.RejectedNotUTF8,
	} {
		t.Run(string(admission), func(t *testing.T) {
			opened := newStore(t)
			write := store.SnapshotWrite{
				PolicyDigest: strings.Repeat("b", 64),
				Documents: []store.Document{
					{
						Path: "kept.md", ContentHash: strings.Repeat("c", 64),
						Register: "essays", Split: corpus.Train,
						Admission: corpus.Eligible, Language: corpus.CheckNotPerformed,
					},
					{
						Path: "rejected.md", ContentHash: strings.Repeat("d", 64),
						Register: "essays", Split: "",
						Admission: admission, Language: corpus.CheckNotPerformed,
					},
				},
			}
			write.ID = snapshotIdentity(write)
			if err := opened.PutSnapshot(ctx(), write); err != nil {
				t.Fatalf("a corpus containing a %s document could not be stored: %v", admission, err)
			}

			read, err := opened.Snapshot(ctx(), write.ID)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(read.Documents) != 2 {
				t.Fatalf("%d documents came back, want both; a rejected one is still a record "+
					"of a file that was seen", len(read.Documents))
			}
			for _, d := range read.Documents {
				if (d.Admission == corpus.Eligible) != (d.Split != "") {
					t.Errorf("%s is %s with split %q; a split belongs to an eligible document "+
						"and only to one", d.Path, d.Admission, d.Split)
				}
			}
		})
	}
}

// And the pairing holds in both directions: an eligible document without a
// split is as wrong as a rejected one with it, and neither may be written.
func TestTheSplitAndTheAdmissionMustAgree(t *testing.T) {
	for _, c := range []struct {
		name      string
		split     corpus.Split
		admission corpus.Admission
	}{
		{"eligible with no split", "", corpus.Eligible},
		{"rejected with a split", corpus.Train, corpus.RejectedDuplicate},
	} {
		t.Run(c.name, func(t *testing.T) {
			write := store.SnapshotWrite{
				PolicyDigest: strings.Repeat("b", 64),
				Documents: []store.Document{{
					Path: "one.md", ContentHash: strings.Repeat("c", 64),
					Register: "essays", Split: c.split, Admission: c.admission,
					Language: corpus.CheckNotPerformed,
				}},
			}
			write.ID = snapshotIdentity(write)
			err := newStore(t).PutSnapshot(ctx(), write)
			if err == nil {
				t.Error("the write was accepted")
			}
		})
	}
}

// snapshotIdentity is the content-derived id PutSnapshot requires, so these
// fixtures do not have to hard-code a hash that would go stale.
func snapshotIdentity(w store.SnapshotWrite) string {
	members := make([]string, 0, len(w.Documents))
	for _, d := range w.Documents {
		members = append(members, d.Path+"="+d.ContentHash)
	}
	sort.Strings(members)
	return identity.HashInputs(map[string]string{
		"policy":    w.PolicyDigest,
		"documents": string(identity.Frame(members...)),
	})
}

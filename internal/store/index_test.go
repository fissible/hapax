package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// The language verdict
// ---------------------------------------------------------------------------

// The column holds the VERDICT corpus produces, not a language tag. corpus
// performs no detection, so a tag is a value it can never supply — and slice 1
// accepting one is why index could not write a real corpus.
func TestOnlyAVerdictCorpusCanProduceIsAcceptedAsALanguage(t *testing.T) {
	for _, c := range []struct {
		value      string
		acceptable bool
	}{
		{"not-performed", true},
		{"passed", true},
		{"failed", true},
		{"skipped-by-policy", true},
		{"en", false},
		{"en-GB", false},
		{"english", false},
		{"", false},
		{"Not-Performed", false},
	} {
		t.Run(c.value, func(t *testing.T) {
			s := newStore(t)
			document := document("essays/a.md", hashA, node(0, 0, 4))
			document.Language = corpus.CheckState(c.value)
			write := snapshotWrite(document)
			err := s.PutSnapshot(ctx(), write)
			if c.acceptable && err != nil {
				t.Errorf("refused %q: %v", c.value, err)
			}
			if !c.acceptable && err == nil {
				t.Errorf("accepted %q", c.value)
			}
		})
	}
}

func TestAStoredLanguageThatIsNotAVerdictIsCorrupt(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 4)))
	mustPutSnapshot(t, s, write)
	if _, err := openRaw(t, s).Exec("UPDATE document SET language = 'en'"); err == nil {
		// The schema refuses it, which is the stronger answer.
		return
	}
	if _, err := s.Snapshot(ctx(), write.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// ---------------------------------------------------------------------------
// Readiness
// ---------------------------------------------------------------------------

// hapax profile reports whether the profile is production ready, so the store
// has to hold it. The reason is a closed set profile declares.
func TestProfileReadinessRoundTrips(t *testing.T) {
	for _, c := range []struct {
		name     string
		ready    bool
		declared bool
	}{
		{"ready and silent", true, false},
		{"not ready, with its declared reason", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot := storedGraph(t, s)
			want := profileFixture(snapshot.ID)
			reason := ""
			if c.declared {
				reason = aNotReadyReason(t)
			}
			want.ProductionReady, want.NotReadyReason = c.ready, reason
			mustPutProfile(t, s, want)

			got, err := s.LoadProfile(ctx(), want.ID)
			if err != nil {
				t.Fatalf("LoadProfile: %v", err)
			}
			if got.ProductionReady != c.ready || got.NotReadyReason != reason {
				t.Errorf("readiness = %v/%q, want %v/%q",
					got.ProductionReady, got.NotReadyReason, c.ready, reason)
			}
		})
	}
}

// Ready exactly when there is no reason. A profile that is both would say two
// things at once, and hapax profile has to pick one to report.
func TestReadinessIsCoupledToItsReason(t *testing.T) {
	for _, c := range []struct {
		name     string
		ready    bool
		reason   string
		declared bool
	}{
		{"ready with a reason", true, "", true},
		{"not ready with no reason", false, "", false},
		{"a reason outside the declared set", false, "i have my doubts", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			reason := c.reason
			if c.declared {
				reason = aNotReadyReason(t)
			}
			prof.ProductionReady, prof.NotReadyReason = c.ready, reason
			if err := s.PutProfile(ctx(), prof, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAStoredReadinessThatContradictsItselfIsCorrupt(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	prof.ProductionReady, prof.NotReadyReason = true, ""
	mustPutProfile(t, s, prof)

	if _, err := openRaw(t, s).Exec("UPDATE profile SET production_ready = 0"); err == nil {
		return // refused by the schema, which is stronger
	}
	if _, err := s.LoadProfile(ctx(), prof.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// aNotReadyReason is one declared reason, failing loudly rather than panicking
// if the vocabulary is empty.
func aNotReadyReason(t *testing.T) string {
	t.Helper()
	reasons := profile.NotReadyReasons()
	if len(reasons) == 0 {
		t.Fatal("profile declares no not-ready reasons")
	}
	return reasons[0]
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

func indexWrite(t *testing.T, s *store.Store, mode store.IndexMode) store.IndexWrite {
	t.Helper()
	root := t.TempDir()
	write := snapshotWrite(
		corpusDocument(t, root, "essays/a.md", bodyA,
			text.Span{Offset: 0, Length: 48}, text.Span{Offset: 50, Length: 60}),
	)
	prof := profileFixture(write.ID)
	ref := referenceFixture(prof.ID)
	return store.IndexWrite{Mode: mode, Snapshot: write, Profile: prof, Reference: ref}
}

// The three modes are the three things an index can commit, declared rather
// than expressed as nils someone might forget.
func TestIndexCommitsExactlyWhatItsModeDeclares(t *testing.T) {
	for _, c := range []struct {
		mode         store.IndexMode
		hasProfile   bool
		hasReference bool
	}{
		{store.IndexSnapshotOnly, false, false},
		{store.IndexProfile, true, false},
		{store.IndexProfileAndReference, true, true},
	} {
		t.Run(string(c.mode), func(t *testing.T) {
			s := newStore(t)
			write := indexWrite(t, s, c.mode)
			if _, err := s.Index(ctx(), write); err != nil {
				t.Fatalf("Index: %v", err)
			}

			if _, err := s.Snapshot(ctx(), write.Snapshot.ID); err != nil {
				t.Errorf("the snapshot was not committed: %v", err)
			}
			_, err := s.LoadProfile(ctx(), write.Profile.ID)
			if c.hasProfile && err != nil {
				t.Errorf("the profile was not committed: %v", err)
			}
			if !c.hasProfile && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a profile was committed in %s mode: %v", c.mode, err)
			}
			_, err = s.LoadReference(ctx(), write.Reference.ID)
			if c.hasReference && err != nil {
				t.Errorf("the reference was not committed: %v", err)
			}
			if !c.hasReference && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a reference was committed in %s mode: %v", c.mode, err)
			}
		})
	}
}

// A mode carrying a part it does not declare is a caller that has not decided,
// which is exactly what modes exist to prevent.
func TestIndexRefusesPartsItsModeDoesNotDeclare(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*store.IndexWrite)
	}{
		{"snapshot-only carrying a profile", func(w *store.IndexWrite) {
			w.Mode = store.IndexSnapshotOnly
		}},
		{"profile mode carrying a reference", func(w *store.IndexWrite) {
			w.Mode = store.IndexProfile
		}},
		{"profile-and-reference with no reference", func(w *store.IndexWrite) {
			w.Reference = store.Reference{}
		}},
		{"an undeclared mode", func(w *store.IndexWrite) { w.Mode = "everything" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			write := indexWrite(t, s, store.IndexProfileAndReference)
			c.alter(&write)
			if _, err := s.Index(ctx(), write); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An index that fits no profile does not prune: pruning would delete the
// snapshot it just recorded, and 'indexed, adverse, no profile' has to leave
// something to look at.
func TestAnIndexThatFitsNoProfileDoesNotPrune(t *testing.T) {
	s := newStore(t)
	// Something prunable already exists: a profile no root will reach.
	orphanSnapshot := storedGraph(t, s)
	orphan := profileFixture(orphanSnapshot.ID)
	orphan.ID, orphan.Register = fakeID("profile", "orphan"), "orphan"
	mustPutProfile(t, s, orphan)

	write := indexWrite(t, s, store.IndexSnapshotOnly)
	indexed, err := s.Index(ctx(), write)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if indexed.Pruned != (store.Pruned{}) {
		t.Errorf("a snapshot-only index pruned %+v", indexed.Pruned)
	}
	if _, err := s.LoadProfile(ctx(), orphan.ID); err != nil {
		t.Errorf("the orphan was pruned by an index that advanced no head: %v", err)
	}
	if _, err := s.Snapshot(ctx(), write.Snapshot.ID); err != nil {
		t.Errorf("the snapshot it just recorded is gone: %v", err)
	}
}

// And the modes that advance a head prune to the heads AS THEY STAND AFTER the
// advance — not as they stood before it, which would prune the profile the same
// call just wrote.
func TestAnIndexThatAdvancesAHeadPrunesToThePostWriteHeads(t *testing.T) {
	s := newStore(t)
	orphanSnapshot := storedGraph(t, s)
	orphan := profileFixture(orphanSnapshot.ID)
	orphan.ID, orphan.Register = fakeID("profile", "orphan"), "orphan"
	mustPutProfile(t, s, orphan)

	write := indexWrite(t, s, store.IndexProfileAndReference)
	indexed, err := s.Index(ctx(), write)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if indexed.Pruned.Profiles == 0 {
		t.Error("nothing was pruned; the orphan should have been")
	}
	if _, err := s.LoadProfile(ctx(), orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan survived: %v", err)
	}
	if _, err := s.LoadProfile(ctx(), write.Profile.ID); err != nil {
		t.Errorf("the index pruned the profile it had just written: %v", err)
	}
	head, err := s.ProfileHead(ctx(), write.Profile.Register)
	if err != nil || head != write.Profile.ID {
		t.Errorf("head = %q, %v, want the profile just written", head, err)
	}
}

// ---------------------------------------------------------------------------
// Migration 2
// ---------------------------------------------------------------------------

// A database written before the verdict existed carries language TAGS, which
// were never verdicts. Rewriting them to not-performed is the truth — no
// language check has ever run — and it keeps the corpus index, which unlike a
// release is not cheaply rebuilt from nothing.
func TestMigrationTwoRewritesLanguageTagsAndBackfillsReadiness(t *testing.T) {
	migrations := store.Migrations()
	if len(migrations) < 3 {
		t.Fatalf("%d migrations; the verdict is appended, not amended in", len(migrations))
	}

	path := filepath.Join(t.TempDir(), "hapax.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for version := 0; version < 2; version++ {
		if _, err := db.Exec(migrations[version]); err != nil {
			t.Fatalf("applying migration %d: %v", version, err)
		}
		if _, err := db.Exec(
			"INSERT INTO migration (version, checksum, applied_at) VALUES (?, ?, '2026-01-01T00:00:00Z')",
			version, identity.HashBytes([]byte(migrations[version]))); err != nil {
			t.Fatalf("ledger %d: %v", version, err)
		}
	}
	snapshotID := identity.HashBytes([]byte("legacy snapshot"))
	if _, err := db.Exec("INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-01-01T00:00:00Z')",
		snapshotID, identity.HashBytes([]byte("policy"))); err != nil {
		t.Fatalf("seeding a snapshot: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO document
		(document_id,snapshot_id,path,content_hash,register,split,admission,language,unavailable_at)
		VALUES (?,?,'essays/a.md',?,'essays','train','eligible','en',NULL)`,
		identity.HashBytes([]byte("legacy document")), snapshotID, identity.HashBytes([]byte("content"))); err != nil {
		t.Fatalf("seeding a document with a tag: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	raw := openRaw(t, s)
	var language string
	if err := raw.QueryRow("SELECT language FROM document").Scan(&language); err != nil {
		t.Fatalf("reading the migrated document: %v", err)
	}
	if language != string(corpus.CheckNotPerformed) {
		t.Errorf("language = %q, want %q — no check has ever run",
			language, corpus.CheckNotPerformed)
	}
	if rowsIn(t, raw, "document") != 1 {
		t.Error("the corpus index was deleted rather than corrected")
	}
}

// The stored profile survives, with the readiness it always meant.
func TestMigrationTwoBackfillsTheReadinessProfilesAlwaysHad(t *testing.T) {
	migrations := store.Migrations()
	path := filepath.Join(t.TempDir(), "hapax.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for version := 0; version < 2; version++ {
		if _, err := db.Exec(migrations[version]); err != nil {
			t.Fatalf("applying migration %d: %v", version, err)
		}
		if _, err := db.Exec(
			"INSERT INTO migration (version, checksum, applied_at) VALUES (?, ?, '2026-01-01T00:00:00Z')",
			version, identity.HashBytes([]byte(migrations[version]))); err != nil {
			t.Fatalf("ledger %d: %v", version, err)
		}
	}
	snapshotID := identity.HashBytes([]byte("legacy snapshot"))
	profileID := identity.HashBytes([]byte("legacy profile"))
	if _, err := db.Exec("INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-01-01T00:00:00Z')",
		snapshotID, identity.HashBytes([]byte("policy"))); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profile
		(id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens)
		VALUES (?,?,'essays','paragraph','sample',?,0,40)`,
		profileID, snapshotID, identity.HashBytes([]byte("manifest"))); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	raw := openRaw(t, s)
	var ready int
	var reason string
	if err := raw.QueryRow("SELECT production_ready, not_ready_reason FROM profile").Scan(&ready, &reason); err != nil {
		t.Fatalf("reading the migrated profile: %v", err)
	}
	if ready != 0 {
		t.Error("a backfilled profile claims to be production ready; Build has never produced one")
	}
	declared := false
	for _, candidate := range profile.NotReadyReasons() {
		if reason == candidate {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the backfilled reason %q is not one profile declares", reason)
	}
	// And the ID did not change: readiness is not an identity input.
	var id string
	if err := raw.QueryRow("SELECT id FROM profile").Scan(&id); err != nil {
		t.Fatalf("reading the id: %v", err)
	}
	if id != profileID {
		t.Errorf("the migration changed a profile's identity: %q", id)
	}
}

// The profile mode advances a head too, so it prunes as well — the distinction
// that matters is whether a head moved, not whether a reference was written.
func TestTheProfileModeAlsoPrunes(t *testing.T) {
	s := newStore(t)
	orphanSnapshot := storedGraph(t, s)
	orphan := profileFixture(orphanSnapshot.ID)
	orphan.ID, orphan.Register = fakeID("profile", "orphan"), "orphan"
	mustPutProfile(t, s, orphan)

	write := indexWrite(t, s, store.IndexProfile)
	indexed, err := s.Index(ctx(), write)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if indexed.Pruned.Profiles == 0 {
		t.Error("a profile-mode index pruned nothing")
	}
	if _, err := s.LoadProfile(ctx(), orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan survived: %v", err)
	}
}

// Migration 2 corrects two columns. It must not lose the rest of the graph:
// a table rebuild that dropped children, heads or the release would satisfy
// every assertion about language and readiness while destroying the store.
func TestMigrationTwoPreservesEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ids := seedEveryArtifact(t, first)
	before := map[string]int{}
	raw := openRaw(t, first)
	for _, table := range []string{
		"snapshot", "document", "node", "feature_vector", "feature_value",
		"profile", "profile_stat", "profile_head", "reference", "reference_value",
		"threshold", "eval_result", "calibration_band", "release_head",
		"exemplar_selection", "exemplar_member", "rewrite_attempt", "rewrite_attempt_identifier",
	} {
		before[table] = rowsIn(t, raw, table)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	reopened := openRaw(t, second)
	for table, want := range before {
		if got := rowsIn(t, reopened, table); got != want {
			t.Errorf("%s has %d rows after reopening, had %d", table, got, want)
		}
	}
	if _, err := second.Snapshot(ctx(), ids.Snapshot); err != nil {
		t.Errorf("the snapshot no longer reads: %v", err)
	}
	if _, err := second.LoadEvalResult(ctx(), ids.EvalResult); err != nil {
		t.Errorf("the release no longer reads: %v", err)
	}
}

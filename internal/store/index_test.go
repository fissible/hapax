package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
	if _, err := openRaw(t, s).Exec("UPDATE document SET language = 'en'"); err != nil {
		return // The schema refused it, which is the stronger answer.
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

	if _, err := openRaw(t, s).Exec("UPDATE profile SET production_ready = 0"); err != nil {
		return // The schema refused it, which is stronger.
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
	// Exactly what the mode declares and nothing else — carrying a part the
	// mode does not name is the thing modes exist to refuse, and a fixture
	// that always carried all three would make the two tests below
	// contradict each other.
	out := store.IndexWrite{Mode: mode, Snapshot: write}
	switch mode {
	case store.IndexProfile:
		out.Profile = prof
	case store.IndexProfileAndReference:
		out.Profile, out.Reference = prof, ref
	}
	return out
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
			// Profile and Reference are still populated from the fixture below.
		}},
		{"profile mode carrying a reference", func(w *store.IndexWrite) {
			w.Mode = store.IndexProfile
		}},
		{"profile-and-reference with no reference", func(w *store.IndexWrite) {
			w.Reference = store.Reference{}
		}},
		{"profile mode with no profile", func(w *store.IndexWrite) {
			w.Mode, w.Profile, w.Reference = store.IndexProfile, store.Profile{}, store.Reference{}
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

// The prune inside an index has to BE Prune, not a smaller sweep that happens
// to remove a profile. Two stores are built identically; one is indexed, the
// other has the same writes made through the public writers and is then pruned
// to the heads that result. The counts must agree — and the descendant counts
// must not agree at zero, because a sweep that reclaimed only the orphan
// profile would satisfy an equality check while leaving its snapshot, its
// documents and its nodes in the database forever.
func TestAnIndexPrunesExactlyAsPruneDoes(t *testing.T) {
	seed := func(t *testing.T) (*store.Store, store.IndexWrite) {
		t.Helper()
		s := newStore(t)
		orphanSnapshot := storedGraph(t, s)
		orphan := profileFixture(orphanSnapshot.ID)
		orphan.ID, orphan.Register = fakeID("profile", "orphan"), "orphan"
		mustPutProfile(t, s, orphan)
		return s, indexWrite(t, s, store.IndexProfileAndReference)
	}

	indexedStore, write := seed(t)
	indexed, err := indexedStore.Index(ctx(), write)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}

	// The same writes, made separately, then pruned to the heads they leave.
	separateStore, separateWrite := seed(t)
	mustPutSnapshot(t, separateStore, separateWrite.Snapshot)
	if err := separateStore.PutProfile(ctx(), separateWrite.Profile, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	mustPutReference(t, separateStore, separateWrite.Reference)
	heads, err := separateStore.ProfileHeads(ctx())
	if err != nil {
		t.Fatalf("ProfileHeads: %v", err)
	}
	keep := make([]string, 0, len(heads))
	for _, id := range heads {
		keep = append(keep, id)
	}
	sort.Strings(keep)
	separate, err := separateStore.Prune(ctx(), keep)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if indexed.Pruned != separate {
		t.Errorf("Index pruned %+v, Prune pruned %+v", indexed.Pruned, separate)
	}
	if indexed.Pruned.Snapshots == 0 || indexed.Pruned.Documents == 0 || indexed.Pruned.Nodes == 0 {
		t.Errorf("the orphan profile went but its graph stayed: %+v", indexed.Pruned)
	}
	// The counts are the report; the rows are the fact. An index that reported
	// a descendant sweep it did not perform would pass everything above.
	indexedRaw, separateRaw := openRaw(t, indexedStore), openRaw(t, separateStore)
	for _, table := range []string{"snapshot", "document", "node"} {
		if got, want := rowsIn(t, indexedRaw, table), rowsIn(t, separateRaw, table); got != want {
			t.Errorf("%s has %d rows after an index and %d after a prune", table, got, want)
		}
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

// Migration 2 corrects two columns. It must not lose the rest of the graph: a
// table rebuild that dropped children, heads or the release would satisfy every
// assertion about language and readiness while destroying the store.
//
// The database has to be seeded at migration 1 and then opened THROUGH 2.
// Opening a current-schema store and reopening it runs no migration at all, and
// a destructive migration 2 would pass.
func TestMigrationTwoPreservesEverythingElse(t *testing.T) {
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
	// A graph with a row in every table, written directly at version 1.
	seedVersionOneGraph(t, db)
	tables := preservedTables(t, db)
	before := map[string][]string{}
	for _, table := range tables {
		before[table] = dumpTable(t, db, table, migrationExclusions(table))
		if len(before[table]) == 0 {
			t.Fatalf("%s is empty at version 1; its preservation would be vacuous", table)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open through migration 2: %v", err)
	}
	defer s.Close()
	raw := openRaw(t, s)
	// The ROWS, not their count: a migration that replaced every row with a
	// different one — corrupting identities, edges, heads or audit payloads —
	// keeps the counts exactly.
	for _, table := range tables {
		if got := dumpTable(t, raw, table, migrationExclusions(table)); !reflect.DeepEqual(got, before[table]) {
			t.Errorf("%s changed across migration 2:\n%v\nwant\n%v", table, got, before[table])
		}
	}
}

// dumpTable renders EVERY column of every row, sorted, so two states can be
// compared without depending on row order.
func dumpTable(t *testing.T, db *sql.DB, table string, exclude map[string]bool) []string {
	t.Helper()
	// The columns come from the database itself rather than a hand-written
	// list: a list is a place for a column to go unwatched, which is how a
	// migration mutates one unnoticed.
	names, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	var columns []string
	for names.Next() {
		var name string
		if err := names.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if !exclude[name] {
			columns = append(columns, name)
		}
	}
	names.Close()
	if len(columns) == 0 {
		t.Fatalf("%s has no comparable columns", table)
	}

	rows, err := db.Query("SELECT " + strings.Join(columns, ",") + " FROM " + table)
	if err != nil {
		t.Fatalf("dumping %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scanning %s: %v", table, err)
		}
		rendered := make([]string, len(cells))
		for i, cell := range cells {
			// NULL and the empty string are different values, and
			// NullString.String collapses them: unavailable_at rewritten from
			// NULL to '' would pass an assertion that claims to preserve
			// every column.
			if value := cell.(*sql.NullString); value.Valid {
				rendered[i] = "s:" + value.String
			} else {
				rendered[i] = "null"
			}
		}
		out = append(out, strings.Join(rendered, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %s: %v", table, err)
	}
	sort.Strings(out)
	return out
}

// The columns migration 2 must leave alone. document.language is absent because
// correcting it is the migration's job; profile's readiness columns are absent
// because they do not exist at version 1.
// Every table a version-1 database has, read from the database rather than
// listed: a list is a place for a future table to go unseeded and unchecked
// while the comment still claims every one is covered. The migration ledger is
// excluded because migration 2 necessarily adds a row to it.
func preservedTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name != "migration" {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("a version-1 database has no tables; this comparison would be vacuous")
	}
	return out
}

func migrationExclusions(table string) map[string]bool {
	switch table {
	case "document":
		return map[string]bool{"language": true}
	case "profile":
		// These do not exist at version 1, so there is nothing to compare.
		return map[string]bool{"production_ready": true, "not_ready_reason": true}
	}
	return nil
}

// seedVersionOneGraph writes one row into every table a version-1 database has,
// by raw SQL, so migration 2 has something in each to preserve. Foreign keys
// are off on this connection, so the identities only have to be well formed.
func seedVersionOneGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	id := func(name string) string { return identity.HashBytes([]byte(name)) }
	snapshotID, documentID, nodeID := id("snapshot"), id("document"), id("node")
	profileID, referenceID, evalID := id("profile"), id("reference"), id("eval")
	selectionID, invocationID := id("selection"), id("invocation")
	digest, when := id("manifest"), "2026-01-01T00:00:00Z"

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,?)", []any{snapshotID, id("policy"), when}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language,unavailable_at)
		  VALUES (?,?,'essays/a.md',?,'essays','train','eligible','en',NULL)`, []any{documentID, snapshotID, id("content")}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
		  VALUES (?,?,0,'leaf','paragraph','document',0,12,1,'')`, []any{nodeID, documentID}},
		{`INSERT INTO feature_vector (node_id,manifest_digest,set_version,tokens,lexical_tokens)
		  VALUES (?,?,0,9,7)`, []any{nodeID, digest}},
		{`INSERT INTO feature_value (node_id,manifest_digest,feature,value,defined,sampling_variance,sampling_variance_defined)
		  VALUES (?,?,'word_length_mean',1.0,1,0.25,1)`, []any{nodeID, digest}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens)
		  VALUES (?,?,'essays','paragraph','sample',?,0,40)`, []any{profileID, snapshotID, digest}},
		{`INSERT INTO profile_stat (profile_id,feature,n,mean,variance,defined,variance_defined,min_observations)
		  VALUES (?,'word_length_mean',40,1.0,0.5,1,1,30)`, []any{profileID}},
		{"INSERT INTO profile_head (register,profile_id,updated_at) VALUES ('essays',?,?)", []any{profileID, when}},
		{`INSERT INTO reference (id,profile_id,split,min_segments,manifest_digest) VALUES (?,?,'calibrate',30,?)`,
			[]any{referenceID, profileID, digest}},
		{"INSERT INTO reference_value (reference_id,feature,ordinal,value) VALUES (?,'word_length_mean',0,0.5)", []any{referenceID}},
		{`INSERT INTO threshold (id,profile_id,reference_id,population_id,t_low,t_high,achieved_author,achieved_distractor,
		  interval_low_lower,interval_low_upper,interval_high_lower,interval_high_upper,verdict)
		  VALUES (?,?,?,?,0.4,0.9,0.05,0.1,0.35,0.45,0.85,0.95,'separated')`,
			[]any{id("threshold"), profileID, referenceID, id("population")}},
		{`INSERT INTO eval_result (id,profile_id,reference_id,shippable,reason,
		  discrimination_id,discrimination_population_id,discrimination_manifest_digest,discrimination_weight_scheme,
		  discrimination_distance_algorithm,discrimination_scored_tiers,discrimination_split,discrimination_algorithm,
		  discrimination_clustering,discrimination_floor,discrimination_confidence,discrimination_resamples,discrimination_seed,
		  auc,lower_bound,cap,author_segments,distractor_segments,author_clusters,distractor_clusters,min_clusters,
		  discriminates,discrimination_reason,
		  calibration_id,calibration_thresholds_id,calibration_population_id,calibration_manifest_digest,
		  calibration_weight_scheme,calibration_distance_algorithm,calibration_scored_tiers,calibration_split,
		  calibration_algorithm,calibration_low,calibration_high,calibration_confidence,calibration_resamples,
		  calibration_seed,calibrated,calibration_reason)
		  VALUES (?,?,?,1,'',?,?,?,'uniform-v1','distance-uniform-mean-v1','A','test','clustered-auc-lower-bound-v1',
		  'document',0.65,0.95,2000,7,0.82,0.71,2.5,120,140,12,14,10,1,'',
		  ?,?,?,?,'uniform-v1','distance-uniform-mean-v1','A','test','band-error-bound-v1',0.4,0.9,0.95,2000,11,1,'')`,
			[]any{evalID, profileID, referenceID, id("discrimination"), id("population"), digest,
				id("calibration"), id("threshold"), id("population"), digest}},
		{`INSERT INTO calibration_band (eval_result_id,band,claims,target,error_rate,error_bound,
		  class_segments,class_clusters,min_class_clusters,author_segments,distractor_segments,emitted,reason)
		  VALUES (?,'drifting','',0,0,0,0,0,0,20,40,1,'')`, []any{evalID}},
		{"INSERT INTO release_head (profile_id,eval_result_id,updated_at) VALUES (?,?,?)", []any{profileID, evalID, when}},
		{"INSERT INTO exemplar_selection (id,profile_id,n,certificate_id) VALUES (?,?,1,?)",
			[]any{selectionID, profileID, id("certificate")}},
		{"INSERT INTO exemplar_member (selection_id,ordinal,node_id) VALUES (?,0,?)", []any{selectionID, nodeID}},
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,current_hash,candidate_hash,
		  current_distance,candidate_distance,current_band,candidate_band,preserved,tells_comparison,tells_comparable,accepted,rejection)
		  VALUES (?,0,?,'ollama',?,?,?,1.2,1.4,'drifting','not-you',0,2,1,0,'not-preserved')`,
			[]any{invocationID, profileID, nodeID, id("current"), id("candidate")}},
		{`INSERT INTO rewrite_attempt_identifier (invocation_id,attempt_index,ordinal,identifier)
		  VALUES (?,0,0,'preserve-v1:number:lost:0123456789abcdef')`, []any{invocationID}},
	} {
		if _, err := db.Exec(statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding: %v\n%s", err, statement.sql)
		}
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// A brand-new database spends 92% of its open replaying a migration chain whose
// later steps exist to move data that is not there — 11.7ms of 12.7ms measured,
// against 0.97ms to reopen an existing one. The store test package opens fresh
// databases thousands of times, which is why internal/store is the critical path
// for the whole suite's wall clock under -race.
//
// So a fresh store is now a copy of a template that the chain itself produced,
// rather than a replay of the chain. That is deliberately the smaller claim:
// replaying sqlite_master would assert that a projection of the chain's output is
// faithful, and after a table rebuild the catalogue holds text no migration
// contains. Copying asserts nothing, because the bytes ARE the output.
//
// These tests are in-package because the seam that makes them honest is
// unexported: without deps.ForceMigrationChain, a test that "compares against the
// chain" builds both sides through the same materialised path and agrees with
// itself about any defect.

// The load-bearing test of this slice. Everything else is detail.
func TestAMaterialisedStoreAndAChainedStoreAreTheSameDatabase(t *testing.T) {
	materialised := filepath.Join(t.TempDir(), "materialised.db")
	chained := filepath.Join(t.TempDir(), "chained.db")

	freshDeps, freshRecord := recordingDeps()
	fresh, err := open(materialised, "sqlite", freshDeps)
	if err != nil {
		t.Fatalf("Open materialised: %v", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The comparison store goes through open() with the chain forced, so it
	// cannot reach the branch under test. A test that built both sides the
	// production way would be a mirror.
	forcedDeps, forcedRecord := chainDeps()
	forced, err := open(chained, "sqlite", forcedDeps)
	if err != nil {
		t.Fatalf("Open chained: %v", err)
	}
	if err := forced.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	requireOnly(t, freshRecord, 1, 0)
	requireOnly(t, forcedRecord, 0, 1)

	got, want := databaseState(t, materialised), databaseState(t, chained)
	for _, key := range sortedKeys(want) {
		if got[key] != want[key] {
			t.Errorf("%s differs\n materialised: %s\n chained:      %s", key, got[key], want[key])
		}
	}
	for _, key := range sortedKeys(got) {
		if _, expected := want[key]; !expected {
			t.Errorf("the materialised store has %s, which the chained store does not", key)
		}
	}
}

// The process's own cached template is the thing every fresh store is copied
// from, and it is built once. If it were built some other way than the chain —
// or cached from a run that had already been materialised — every store in the
// process would be wrong identically, and the test above would not see it
// because it builds its own.
func TestTheProcessTemplateIsWhatTheChainProduces(t *testing.T) {
	template, err := freshTemplate()
	if err != nil {
		t.Fatalf("freshTemplate: %v", err)
	}

	chained := filepath.Join(t.TempDir(), "chained.db")
	forcedDeps, forcedRecord := chainDeps()
	forced, err := open(chained, "sqlite", forcedDeps)
	if err != nil {
		t.Fatalf("Open chained: %v", err)
	}
	_ = forced.Close()
	requireOnly(t, forcedRecord, 0, 1)

	if got, want := ledgerIdentity(t, template), ledgerIdentity(t, chained); !reflect.DeepEqual(got, want) {
		t.Errorf("the template's ledger is\n%v\nand the chain produces\n%v", got, want)
	}
	if got, want := catalogue(t, template), catalogue(t, chained); !reflect.DeepEqual(got, want) {
		t.Errorf("the template's catalogue differs from the chain's")
	}
}

// Every fresh store gets its own establishment time. Carrying the template's
// timestamps into every copy would make applied_at mean "when this process built
// its template", which is not what the column says.
func TestEachFreshStoreStampsItsOwnLedger(t *testing.T) {
	dir := t.TempDir()
	var stamps []string
	for i, when := range []string{"2026-01-01T00:00:00Z", "2026-06-15T12:30:00Z"} {
		path := filepath.Join(dir, fmt.Sprintf("store%d.db", i))
		d, record := fixedTimeDeps(when)
		s, err := open(path, "sqlite", d)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_ = s.Close()
		requireOnly(t, record, 1, 0)
		// EVERY row, not the first. An implementation that restamps version 0
		// and leaves the rest at template-build time passes any check that reads
		// one row, and databaseState deliberately ignores timestamps entirely.
		for version, got := range appliedAt(t, path) {
			if got != when {
				t.Errorf("store %d, ledger version %d stamped %q, want %q — it carried "+
					"the template's time", i, version, got, when)
			}
		}
		stamps = append(stamps, appliedAt(t, path)[0])
	}

	if stamps[0] == stamps[1] {
		t.Error("both stores share one timestamp, so the ledger records when the template was built")
	}
}

// Publication is atomic and refuses to clobber. Copying into the destination, or
// renaming over it, would make a crash leave a partial database that the next
// open mistakes for a foreign schema — and would widen the window between the
// stat and the open rather than closing it.
func TestPublishingRefusesToOverwriteAFileThatAppeared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "taken.db")
	foreign := []byte("this is not a database, and it was here first\n")
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("opened a file that is not a hapax store")
	}
	if !errors.Is(err, ErrSchemaForeign) {
		t.Errorf("error is %v, want ErrSchemaForeign", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != string(foreign) {
		t.Errorf("a refused open rewrote the file:\n%q", after)
	}
	if leftovers := stagingFiles(t, dir); len(leftovers) != 0 {
		t.Errorf("staging files were left behind: %v", leftovers)
	}
}

// An existing SQLite file with no tables is not an absent path. It keeps today's
// classification and is initialised in place through the chain, because
// reclassifying it as ours to publish over is how a file someone else created
// gets replaced.
func TestAnExistingEmptyDatabaseIsInitialisedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	empty, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := empty.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	d, record := recordingDeps()
	s, err := open(path, "sqlite", d)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != len(migrations)-1 {
		t.Errorf("schema version %d, want %d", version, len(migrations)-1)
	}
	requireOnly(t, record, 0, 1)

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("the existing file was replaced rather than initialised in place")
	}
}

// An environment with no writable temp space cannot build a template. That is a
// reason to be slow, not a reason to refuse to open a store: the chain is still
// correct and the only thing lost is the optimisation.
func TestATemplateThatCannotBeBuiltFallsBackToTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback.db")
	d, record := recordingDeps()
	d.TemplateDir = func() (string, error) { return "", errors.New("no temp space") }
	var reported []error
	d.TemplateUnavailable = func(err error) { reported = append(reported, err) }

	s, err := open(path, "sqlite", d)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != len(migrations)-1 {
		t.Errorf("schema version %d, want %d", version, len(migrations)-1)
	}
	requireOnly(t, record, 0, 1)

	if len(reported) == 0 {
		t.Error("the fallback was silent; a store that quietly stops being fast is a " +
			"performance cliff whose only symptom is a slow suite")
	}
}

// A migration that fails is not an environmental problem. Falling back would
// turn a broken schema into a slow one and hide it.
func TestATemplateThatFailsToMigrateDoesNotFallBack(t *testing.T) {
	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = append(append([]string(nil), full...), "CREATE TABLE this is not sql")

	path := filepath.Join(t.TempDir(), "broken.db")
	var reported []error
	d, _ := recordingDeps()
	d.TemplateUnavailable = func(err error) { reported = append(reported, err) }

	if _, err := open(path, "sqlite", d); err == nil {
		t.Fatal("a store opened against a migration chain that does not apply")
	}
	if len(reported) != 0 {
		t.Errorf("a failing migration was reported as an unavailable template: %v", reported)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed open left %s behind", path)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// initialisation records which route each open took. A test cannot otherwise
// tell a store published from the template from one chained in place — they are
// deliberately indistinguishable once built, which is the whole point — so the
// route is reported through the same dependency seam as the fallback.
type initialisation struct {
	materialised, chained []string
	// links is keyed by destination, not counted, because a count alone lets an
	// implementation link somewhere irrelevant, chain the destination, and
	// report it materialised.
	links     map[string]int
	templates []string
	// migrations counts what the chain actually executed. Initialised is a claim;
	// an implementation can run the chain into the staging file, announce the
	// cached template, link it, and pass every assertion about the resulting
	// database while missing the entire mechanism. A materialised open executes
	// no migrations.
	migrations int
	// steps is the ordered log of durability-relevant operations, because
	// "synced before published" is a claim about order and nothing else here
	// would notice a sync that never happened.
	steps []string
	// synced records the inode behind each sync, and published the inode behind
	// each link. Comparing pathnames is not enough: syncing the file at
	// <staging>, replacing that pathname, and linking the replacement leaves an
	// adjacent sync/link pair in the log over an inode that was never synced.
	synced, published []os.FileInfo
	// templateBytes is how much of the template each publish actually read. A
	// callback saying which template was used is a report; bytes moving through
	// a reader is the copy itself.
	templateBytes []int64
	readers       []*countingReader
}

func (r *initialisation) record(path string, materialised bool) {
	if materialised {
		r.materialised = append(r.materialised, path)
	} else {
		r.chained = append(r.chained, path)
	}
}

func recordingDeps() (deps, *initialisation) {
	record := &initialisation{}
	d := realDeps()
	d.Initialised = record.record
	// Initialised is self-reported: an implementation could chain and then claim
	// it materialised. Link is an effect rather than a claim — a materialised
	// store is published by exactly one link, and a chained one performs none —
	// so requireOnly checks the two against each other.
	record.links = map[string]int{}
	link := d.Link
	d.Link = func(staging, destination string) error {
		record.links[destination]++
		record.steps = append(record.steps, "link "+staging)
		info, err := os.Stat(staging)
		if err != nil {
			return err
		}
		record.published = append(record.published, info)
		return link(staging, destination)
	}
	sync := d.Sync
	d.Sync = func(f *os.File) error {
		record.steps = append(record.steps, "sync "+f.Name())
		info, err := f.Stat()
		if err != nil {
			return err
		}
		record.synced = append(record.synced, info)
		return sync(f)
	}
	d.MigrationApplied = func(int) { record.migrations++ }
	// The template arrives as a reader rather than as a notification, so what is
	// recorded is bytes moving out of it. An implementation that builds the
	// staging database some other way reads nothing, whatever it announces.
	source := d.TemplateSource
	d.TemplateSource = func() (io.ReadCloser, string, error) {
		reader, path, err := source()
		if err != nil {
			return nil, path, err
		}
		record.templates = append(record.templates, path)
		counter := &countingReader{ReadCloser: reader}
		record.readers = append(record.readers, counter)
		return counter, path, nil
	}
	return d, record
}

type countingReader struct {
	io.ReadCloser
	n       int64
	drained bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	read, err := c.ReadCloser.Read(p)
	c.n += int64(read)
	if errors.Is(err, io.EOF) {
		c.drained = true
	}
	return read, err
}

// chainDeps forces the genuine migration chain, which is the only way a
// comparison against it can be independent of the branch under test.
func chainDeps() (deps, *initialisation) {
	d, record := recordingDeps()
	d.ForceMigrationChain = true
	return d, record
}

func fixedTimeDeps(rfc3339 string) (deps, *initialisation) {
	d, record := recordingDeps()
	when, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	d.Now = func() time.Time { return when }
	return d, record
}

// Every other test here drives open() directly, so nothing would notice if the
// public Open hard-coded the chain or ignored its dependencies altogether. It
// takes its dependencies from one factory, and this swaps that factory to watch
// what Open actually does.
func TestThePublicOpenMaterialises(t *testing.T) {
	d, record := recordingDeps()
	previous := defaultDeps
	t.Cleanup(func() { defaultDeps = previous })
	defaultDeps = func() deps { return d }

	s, err := Open(filepath.Join(t.TempDir(), "public.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireOnly(t, record, 1, 0)

	cached, err := freshTemplate()
	if err != nil {
		t.Fatalf("freshTemplate: %v", err)
	}
	if len(record.templates) != 1 || record.templates[0] != cached {
		t.Errorf("the public Open copied from %v, and the cached template is %q",
			record.templates, cached)
	}
}

// requireOnly fails unless exactly the expected paths were taken. Without it
// these tests pass whichever route ran, which is the failure they exist to catch.
func requireOnly(t *testing.T, record *initialisation, materialised, chained int) {
	t.Helper()
	if len(record.materialised) != materialised || len(record.chained) != chained {
		t.Fatalf("%d materialised and %d chained initialisations, want %d and %d",
			len(record.materialised), len(record.chained), materialised, chained)
	}
	// The reported route and the observable effect must agree, and agree per
	// destination: the report is a string the implementation chose, the link is
	// something it did.
	published := 0
	for _, count := range record.links {
		published += count
	}
	if published != materialised {
		t.Fatalf("%d initialisations reported as materialised but %d links were published",
			materialised, published)
	}
	for _, path := range record.materialised {
		if record.links[path] != 1 {
			t.Fatalf("%s was reported materialised but was linked %d times", path, record.links[path])
		}
	}
	for _, path := range record.chained {
		if record.links[path] != 0 {
			t.Fatalf("%s was chained and also linked %d times", path, record.links[path])
		}
	}
	// The mechanism, not the report. A materialised store is a copy, so no
	// migration runs for it; a chained one runs the whole chain. This is what
	// stops an implementation chaining into the staging file and calling the
	// result materialised.
	if materialised > 0 && chained == 0 && record.migrations != 0 {
		t.Fatalf("%d migrations were executed for a store reported as a copy of the template",
			record.migrations)
	}
	if chained > 0 && record.migrations == 0 {
		t.Fatalf("a store was reported as chained and executed no migrations")
	}
	// And a publication is durable before it is visible: a staging file linked
	// into place without being synced can persist as a directory entry over
	// unwritten data, which is the one thing the stated boundary rules out. The
	// sync has to be of the file being published — the pairing is the assertion,
	// not the ordering.
	for i, step := range record.steps {
		staging, isLink := strings.CutPrefix(step, "link ")
		if !isLink {
			continue
		}
		if i == 0 {
			t.Fatalf("step %d published before anything was synced: %v", i, record.steps)
		}
		if want := "sync " + staging; record.steps[i-1] != want {
			t.Fatalf("step %d is %q and follows %q, want %q — the published file was not "+
				"the file that was synced: %v", i, step, record.steps[i-1], want, record.steps)
		}
	}
	// By inode, not by pathname. Syncing the file at <staging>, replacing that
	// pathname and linking the replacement leaves the log above perfectly
	// paired over an inode that was never synced.
	if len(record.synced) != len(record.published) {
		t.Fatalf("%d syncs against %d publications", len(record.synced), len(record.published))
	}
	for i := range record.published {
		if !os.SameFile(record.synced[i], record.published[i]) {
			t.Fatalf("publication %d synced %v and published %v — different files",
				i, record.synced[i].Name(), record.published[i].Name())
		}
	}
	// And the template was actually read. Every published store is a copy, so
	// every publication drains the whole template.
	if len(record.readers) != materialised {
		t.Fatalf("%d template readers opened for %d materialised stores",
			len(record.readers), materialised)
	}
	for i, reader := range record.readers {
		if reader.n == 0 {
			t.Fatalf("publication %d opened the template and read nothing from it", i)
		}
		// To EOF, and the whole file. Reading one byte from the supplied reader
		// and then reopening the template by its pathname to copy a separate
		// handle satisfies a byte count, and means the reader this test is
		// watching was never the source of the copy.
		if !reader.drained {
			t.Fatalf("publication %d read %d bytes from the template and never reached the "+
				"end of it; the copy came from somewhere else", i, reader.n)
		}
		if size := templateSize(t, record.templates[i]); reader.n != size {
			t.Fatalf("publication %d read %d bytes of a %d byte template", i, reader.n, size)
		}
	}
}

func templateSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat template %s: %v", path, err)
	}
	return info.Size()
}

// databaseState is everything two stores built by different routes must agree
// on: the catalogue, the ledger's identity, every non-ledger row, the integrity
// checks, and the persistent settings that live in the file.
//
// Not compared: rootpage and file bytes, which are layout rather than schema,
// and applied_at, which is a time and differs between any two stores.
func databaseState(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, entry := range catalogue(t, path) {
		out[fmt.Sprintf("catalogue[%02d]", i)] = entry
	}
	for i, entry := range ledgerIdentity(t, path) {
		out[fmt.Sprintf("ledger[%02d]", i)] = entry
	}
	db := openRawAt(t, path)
	for _, table := range tableNames(t, db) {
		if table == "migration" {
			continue
		}
		rows := allRows(t, db, table)
		// A non-empty table here is a migration that seeds data. That is not
		// forbidden — it is the case a catalogue-only comparison would miss, so
		// it is compared rather than assumed absent.
		out["rows."+table] = strings.Join(rows, "\n")
	}
	// sqlite_sequence carries AUTOINCREMENT allocation state and is excluded by
	// the sqlite_% filter above, so it is read by name or it is never compared.
	if hasTable(t, db, "sqlite_sequence") {
		out["rows.sqlite_sequence"] = strings.Join(allRows(t, db, "sqlite_sequence"), "\n")
	}
	for _, pragma := range []string{
		"page_size", "auto_vacuum", "application_id", "user_version", "encoding",
		"journal_mode", "default_cache_size",
	} {
		out["pragma."+pragma] = scalar(t, db, "PRAGMA "+pragma)
	}
	out["integrity_check"] = scalar(t, db, "PRAGMA integrity_check")
	out["foreign_key_check"] = strings.Join(allRows(t, db, "pragma_foreign_key_check"), "\n")
	return out
}

// catalogue is every object SQLite records, in the order it records them, with
// rootpage excluded. Autoindexes carry a NULL sql and are included by name, or a
// missing constraint index would not show up.
func catalogue(t *testing.T, path string) []string {
	t.Helper()
	db := openRawAt(t, path)
	rows, err := db.Query("SELECT type, name, tbl_name, COALESCE(sql, '<none>') FROM sqlite_master ORDER BY rowid")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, name, table, ddl string
		if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, strings.Join([]string{kind, name, table, ddl}, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("%s has no catalogue; comparing it proves nothing", path)
	}
	return out
}

// ledgerIdentity is version and checksum, never applied_at: two stores built at
// different moments differ there whatever route they took.
func ledgerIdentity(t *testing.T, path string) []string {
	t.Helper()
	db := openRawAt(t, path)
	rows, err := db.Query("SELECT version, checksum FROM migration ORDER BY version")
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, fmt.Sprintf("%d=%s", version, checksum))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(out) != len(migrations) {
		t.Fatalf("%s has %d ledger rows over %d migrations", path, len(out), len(migrations))
	}
	return out
}

// appliedAt returns every ledger row's timestamp, keyed by version.
func appliedAt(t *testing.T, path string) map[int]string {
	t.Helper()
	rows, err := openRawAt(t, path).Query("SELECT version, applied_at FROM migration ORDER BY version")
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var version int
		var when string
		if err := rows.Scan(&version, &when); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[version] = when
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(out) != len(migrations) {
		t.Fatalf("%s has %d ledger rows over %d migrations", path, len(out), len(migrations))
	}
	return out
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", name).Scan(&count); err != nil {
		t.Fatalf("look for %s: %v", name, err)
	}
	return count != 0
}

func openRawAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
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
		out = append(out, name)
	}
	return out
}

func allRows(t *testing.T, db *sql.DB, from string) []string {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + from)
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns of %s: %v", from, err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		into := make([]any, len(columns))
		for i := range values {
			into[i] = &values[i]
		}
		if err := rows.Scan(into...); err != nil {
			t.Fatalf("scan %s: %v", from, err)
		}
		parts := make([]string, len(values))
		for i, value := range values {
			// Type and quoted value: %v alone lets a text column containing the
			// delimiter, a newline or the literal "<nil>" collide with a
			// different row, which is the sort of encoding that makes a
			// comparison quietly weaker than it reads.
			parts[i] = fmt.Sprintf("%T=%q", value, fmt.Sprint(value))
		}
		out = append(out, strings.Join(parts, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	sort.Strings(out)
	return out
}

func scalar(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var value any
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return fmt.Sprintf("%v", value)
}

func stagingFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "hapax-staging") {
			out = append(out, entry.Name())
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// The template is built once and reused. Building it per open would keep the
// chain's cost exactly where this slice is trying to remove it, and every test
// above would still pass because a per-open template produces identical stores.
func TestTheTemplateIsBuiltOncePerProcess(t *testing.T) {
	before := templateBuilds()
	dir := t.TempDir()
	var templatesUsed []string
	for i := 0; i < 4; i++ {
		d, record := recordingDeps()
		s, err := open(filepath.Join(dir, fmt.Sprintf("store%d.db", i)), "sqlite", d)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		_ = s.Close()
		requireOnly(t, record, 1, 0)
		templatesUsed = append(templatesUsed, record.templates...)
	}
	if built := templateBuilds() - before; built > 1 {
		t.Errorf("four fresh stores built the template %d times", built)
	}
	// And they all copied the SAME template, which is the claim the build count
	// alone does not make: an implementation could build once and then publish
	// from somewhere else entirely.
	cached, err := freshTemplate()
	if err != nil {
		t.Fatalf("freshTemplate: %v", err)
	}
	for i, used := range templatesUsed {
		if used != cached {
			t.Errorf("store %d was copied from %q, and the cached template is %q", i, used, cached)
		}
	}
	if len(templatesUsed) != 4 {
		t.Fatalf("%d templates reported over four fresh stores", len(templatesUsed))
	}
}

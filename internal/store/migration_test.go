package store_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
)

// The ledger is the only authority on version. A version integer alone cannot
// distinguish an interrupted migration from a completed one, and cannot tell a
// database this binary wrote from one something else did.

func TestOpeningTwiceIsAppliedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	version, err := first.SchemaVersion(ctx())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	again, err := second.SchemaVersion(ctx())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if again != version {
		t.Errorf("version %d after reopening, want %d", again, version)
	}
}

// Versions are contiguous from zero, so a gap is a ledger this binary did not
// write.
func TestTheLedgerIsContiguousFromZero(t *testing.T) {
	s := newStore(t)
	db := openRaw(t, s)

	rows, err := db.Query("SELECT version FROM migration ORDER BY version")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	next := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if version != next {
			t.Errorf("version %d where %d was expected; the ledger has a gap", version, next)
		}
		next++
	}
	if next == 0 {
		t.Error("the ledger is empty")
	}
}

func TestOpeningRefusesADatabaseThisBinaryCannotAccountFor(t *testing.T) {
	for _, c := range []struct {
		name    string
		corrupt func(t *testing.T, db *sql.DB)
		want    error
	}{
		{"a version newer than this binary", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec("INSERT INTO migration (version, checksum, applied_at) VALUES (9999, 'ff', '2026-01-01T00:00:00Z')"); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}, store.ErrSchemaAhead},

		{"a checksum that disagrees", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec("UPDATE migration SET checksum = 'deadbeef' WHERE version = 0"); err != nil {
				t.Fatalf("update: %v", err)
			}
		}, store.ErrSchemaChecksum},

		{"a gap in the ledger", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec("DELETE FROM migration WHERE version = 0"); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}, store.ErrSchemaIncomplete},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hapax.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open raw: %v", err)
			}
			c.corrupt(t, db)
			if err := db.Close(); err != nil {
				t.Fatalf("close raw: %v", err)
			}

			reopened, err := store.Open(path)
			if err == nil {
				_ = reopened.Close()
				t.Fatal("opened a database this binary cannot account for")
			}
			if !errors.Is(err, c.want) {
				t.Errorf("error = %v, want %v", err, c.want)
			}
		})
	}
}

// A file that exists but holds no ledger is a pre-ledger or externally created
// database. Refused rather than adopted — a database is the user's evidence,
// and adopting one means guessing what is in it.
func TestOpeningRefusesADatabaseWithNoLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE something (id TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := store.Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("adopted a database with no ledger")
	}
	if !errors.Is(err, store.ErrSchemaForeign) {
		t.Errorf("error = %v, want ErrSchemaForeign", err)
	}
}

// Exactly one ledger row per migration, however many times it is opened. Two
// rows would mean the DDL ran twice, which for anything but CREATE IF NOT
// EXISTS is a different schema.
func TestTheLedgerHasExactlyOneRowPerMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	for i := 0; i < 3; i++ {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close()

	var rows, distinct int
	if err := db.QueryRow("SELECT count(*), count(DISTINCT version) FROM migration").Scan(&rows, &distinct); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != distinct {
		t.Errorf("%d ledger rows for %d versions", rows, distinct)
	}
}

// Consistency between two databases would be satisfied by a constant. The
// ledger is compared EXACTLY to the migration payload the binary exports —
// every version, every checksum, and no extra rows.
func TestTheLedgerIsExactlyTheMigrationsThisBinaryCarries(t *testing.T) {
	db := openRaw(t, newStore(t))
	migrations := store.Migrations()
	if len(migrations) == 0 {
		t.Fatal("the binary carries no migrations")
	}

	rows, err := db.Query("SELECT version, checksum FROM migration ORDER BY version")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[version] = checksum
	}
	want := map[int]string{}
	for version, sqlText := range migrations {
		want[version] = identity.HashBytes([]byte(sqlText))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ledger =\n%v\nwant\n%v", got, want)
	}
}

// The immediately-next version, not an absurd one: a store that compared
// against a hard-coded ceiling rather than against what it carries would pass
// with 9999 and fail here.
func TestTheNextUnknownVersionIsAhead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	seed(t, path, "INSERT INTO migration (version, checksum, applied_at) VALUES ("+
		strconv.Itoa(len(store.Migrations()))+", '"+identity.HashBytes([]byte("future"))+"', '2026-01-01T00:00:00Z')")

	s, err := store.Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("opened a database one version ahead of this binary")
	}
	if !errors.Is(err, store.ErrSchemaAhead) {
		t.Errorf("error = %v, want ErrSchemaAhead", err)
	}
}

// And it is the same in two independently created databases.
func TestTheChecksumIsStableAcrossDatabases(t *testing.T) {
	first := openRaw(t, newStore(t))
	second := openRaw(t, newStore(t))

	read := func(db *sql.DB) map[int]string {
		out := map[int]string{}
		rows, err := db.Query("SELECT version, checksum FROM migration")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var version int
			var checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[version] = checksum
		}
		return out
	}
	a, b := read(first), read(second)
	if len(a) == 0 {
		t.Fatal("no ledger rows")
	}
	for version, checksum := range a {
		if b[version] != checksum {
			t.Errorf("version %d checksum %q in one database and %q in another", version, checksum, b[version])
		}
	}
}

// Refusing must not have written anything, in ANY of the four modes. A refused
// open that migrated first would have changed the evidence it declined to read,
// and the WAL and shm sidecars are part of that evidence.
func TestARefusedOpenChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(t *testing.T, path string)
	}{
		{"no ledger", func(t *testing.T, path string) {
			db, _ := sql.Open("sqlite", path)
			if _, err := db.Exec("CREATE TABLE something (id TEXT)"); err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = db.Close()
		}},
		{"version ahead", func(t *testing.T, path string) {
			seed(t, path, "INSERT INTO migration (version, checksum, applied_at) VALUES (9999, 'ff', '2026-01-01T00:00:00Z')")
		}},
		{"checksum differs", func(t *testing.T, path string) {
			seed(t, path, "UPDATE migration SET checksum = 'deadbeef' WHERE version = 0")
		}},
		{"gap", func(t *testing.T, path string) {
			seed(t, path, "DELETE FROM migration WHERE version = 0")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hapax.db")
			c.build(t, path)
			before := fingerprint(t, path)

			if s, err := store.Open(path); err == nil {
				_ = s.Close()
				t.Fatal("adopted it")
			}
			if after := fingerprint(t, path); !reflect.DeepEqual(after, before) {
				t.Errorf("a refused open modified the database or a sidecar:\n%v\nwas\n%v", after, before)
			}
		})
	}
}

// seed builds a valid database, closes it, then applies one statement to make
// it something this binary must refuse.
func seed(t *testing.T, path, statement string) {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
}

// fingerprint covers the database and its sidecars: a refused open that only
// left the main file alone would still have changed the evidence.
func fingerprint(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		body, err := os.ReadFile(path + suffix)
		if os.IsNotExist(err) {
			out[suffix] = "absent"
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", path+suffix, err)
		}
		out[suffix] = identity.HashBytes(body)
	}
	return out
}

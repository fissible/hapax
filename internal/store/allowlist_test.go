package store_test

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/features"
)

// The privacy invariant is a prohibition on any reversible prose representation.
// "No prose is written" cannot be checked by scanning for strings — that misses
// normalized, fragmented, encoded and indexed copies. So the invariant is
// enforced at the shape: the schema is declared here column by column, and the
// database must match it exactly. Adding a column means editing this file, which
// is the point — the allowlist widens only on purpose.
var declaredSchema = map[string][]string{
	"snapshot": {"id", "policy_digest", "created_at"},
	"document": {
		"document_id", "snapshot_id", "path", "content_hash",
		"register", "split", "admission", "language", "unavailable_at",
	},
	"node": {
		"node_id", "document_id", "ordinal", "kind", "role", "containers",
		"offset", "length", "included", "exclusion",
	},
	"feature_vector": {"node_id", "manifest_digest", "set_version", "tokens", "lexical_tokens"},
	"feature_value": {
		"node_id", "manifest_digest", "feature", "value", "defined",
		"sampling_variance", "sampling_variance_defined",
	},
	"profile": {
		"id", "snapshot_id", "register", "unit", "variance_convention",
		"manifest_digest", "feature_set_version", "min_paragraph_lexical_tokens",
	},
	"profile_stat": {
		"profile_id", "feature", "n", "mean", "variance",
		"defined", "variance_defined", "min_observations",
	},
	"profile_head":    {"register", "profile_id", "updated_at"},
	"reference":       {"id", "profile_id", "split", "min_segments", "manifest_digest"},
	"reference_value": {"reference_id", "feature", "ordinal", "value"},
	"threshold": {
		"id", "profile_id", "reference_id", "population_id",
		"t_low", "t_high", "achieved_low", "achieved_high",
		"interval_low", "interval_high", "verdict",
	},
	"eval_result": {
		"id", "profile_id", "reference_id", "auc", "lower_bound", "cap",
		"author_segments", "distractor_segments", "author_clusters", "distractor_clusters",
		"discriminates", "calibrated", "shippable", "reason",
	},
	"exemplar_selection": {"id", "profile_id", "n", "certificate_id"},
	"exemplar_member":    {"selection_id", "ordinal", "node_id"},
	"rewrite_attempt": {
		"invocation_id", "attempt_index", "profile_id", "provider_id", "node_id",
		"current_hash", "candidate_hash", "current_distance", "candidate_distance",
		"current_band", "candidate_band", "preserved", "tells_comparison",
		"tells_comparable", "accepted", "rejection",
	},
	"rewrite_attempt_identifier": {"invocation_id", "attempt_index", "ordinal", "identifier"},
	"migration":                  {"version", "checksum", "applied_at"},
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
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
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(out)
	return out
}

// The schema is exactly what is declared — no extra table, no extra column.
func TestTheSchemaIsExactlyTheAllowlist(t *testing.T) {
	db := openRaw(t, newStore(t))

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, name)
	}
	sort.Strings(tables)

	want := make([]string, 0, len(declaredSchema))
	for table := range declaredSchema {
		want = append(want, table)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(tables, want) {
		t.Errorf("tables =\n%v\nwant\n%v", tables, want)
	}

	for table, columns := range declaredSchema {
		sorted := append([]string(nil), columns...)
		sort.Strings(sorted)
		if got := tableColumns(t, db, table); !reflect.DeepEqual(got, sorted) {
			t.Errorf("%s columns =\n%v\nwant\n%v", table, got, sorted)
		}
	}
}

// A TRIPWIRE, not a proof, and named as one. A substring scan cannot rule out
// every reversible derivative — hex, printf, replace, JSON functions and a
// user-defined function would all evade it. The honest controls are the column
// allowlist above, the codec field-set tests, and review. This catches the
// obvious regression.
func TestNoIndexViewOrTriggerDerivesText(t *testing.T) {
	db := openRaw(t, newStore(t))
	rows, err := db.Query("SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('index', 'view', 'trigger')")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatalf("scan: %v", err)
		}
		lowered := strings.ToLower(ddl)
		for _, forbidden := range []string{
			"fts", "match", "||", "substr", "lower(", "upper(", "hex(", "printf(",
			"replace(", "json_", "group_concat", "trim(",
		} {
			if strings.Contains(lowered, forbidden) {
				t.Errorf("%s uses %q: %s", name, forbidden, ddl)
			}
		}
	}
}

// The textual columns THIS SLICE writes have a declared grammar. The deferred
// artifacts' columns are covered when their operations land; this test is
// scoped to what can be written today rather than named for what it will
// eventually cover.
func TestTheTextualColumnsThisSliceWritesHaveGrammars(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(t *testing.T) error
	}{
		{"a path that is not corpus-root-relative", writeDocumentPath("/absolute/essay.md")},
		{"a path that escapes the root", writeDocumentPath("../outside.md")},
		{"a path with backslashes", writeDocumentPath(`sub\essay.md`)},
		{"a content hash that is not hex", writeDocumentHash("not-a-hash")},
		{"a content hash of the wrong length", writeDocumentHash("abc123")},
		{"an upper-case content hash", writeDocumentHash(strings.ToUpper(hashA))},
		{"an unknown register", writeDocumentRegister("diary")},
		{"an unknown split", writeDocumentSplit("holdout")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.write(t); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Column names would pass a schema whose content_hash was an unconstrained
// TEXT. The shape is part of the allowlist: declared types, NOT NULL, foreign
// keys, uniqueness, and no virtual tables.
func TestTheSchemaShapeIsConstrained(t *testing.T) {
	db := openRaw(t, newStore(t))

	t.Run("no virtual tables", func(t *testing.T) {
		var count int
		if err := db.QueryRow(
			"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND sql LIKE '%VIRTUAL%'",
		).Scan(&count); err != nil {
			t.Fatalf("query: %v", err)
		}
		if count != 0 {
			t.Errorf("%d virtual tables", count)
		}
	})

	t.Run("every column is typed and not null", func(t *testing.T) {
		// unavailable_at is the one nullable column: absence is its meaning.
		nullable := map[string]bool{"document.unavailable_at": true}
		for table, columns := range declaredSchema {
			for _, column := range columns {
				var declaredType string
				var notNull int
				err := db.QueryRow(
					"SELECT type, [notnull] FROM pragma_table_info(?) WHERE name = ?", table, column,
				).Scan(&declaredType, &notNull)
				if err != nil {
					t.Errorf("%s.%s: %v", table, column, err)
					continue
				}
				if declaredType == "" {
					t.Errorf("%s.%s has no declared type", table, column)
				}
				if notNull == 0 && !nullable[table+"."+column] {
					t.Errorf("%s.%s is nullable", table, column)
				}
			}
		}
	})

	t.Run("foreign keys are exactly the declared graph", func(t *testing.T) {
		// Grouped by FK id, so a composite edge cannot pass as two unrelated
		// one-column keys, and an extra key is rejected rather than ignored.
		type column struct{ From, To string }
		type fk struct {
			Parent   string
			Columns  []column
			OnDelete string
		}
		want := map[string][]fk{
			"snapshot":       nil,
			"migration":      nil,
			"document":       {{Parent: "snapshot", Columns: []column{{"snapshot_id", "id"}}, OnDelete: "CASCADE"}},
			"node":           {{Parent: "document", Columns: []column{{"document_id", "document_id"}}, OnDelete: "CASCADE"}},
			"feature_vector": {{Parent: "node", Columns: []column{{"node_id", "node_id"}}, OnDelete: "CASCADE"}},
			"feature_value": {{
				Parent:  "feature_vector",
				Columns: []column{{"node_id", "node_id"}, {"manifest_digest", "manifest_digest"}}, OnDelete: "CASCADE",
			}},
			"profile":            {{Parent: "snapshot", Columns: []column{{"snapshot_id", "id"}}, OnDelete: "CASCADE"}},
			"profile_stat":       {{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"}},
			"profile_head":       {{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"}},
			"reference":          {{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"}},
			"reference_value":    {{Parent: "reference", Columns: []column{{"reference_id", "id"}}, OnDelete: "CASCADE"}},
			"exemplar_selection": {{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"}},
			"threshold": {
				{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"},
				{Parent: "reference", Columns: []column{{"reference_id", "id"}}, OnDelete: "CASCADE"},
			},
			"eval_result": {
				{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"},
				{Parent: "reference", Columns: []column{{"reference_id", "id"}}, OnDelete: "CASCADE"},
			},
			"exemplar_member": {
				{Parent: "exemplar_selection", Columns: []column{{"selection_id", "id"}}, OnDelete: "CASCADE"},
				{Parent: "node", Columns: []column{{"node_id", "node_id"}}, OnDelete: "CASCADE"},
			},
			"rewrite_attempt": {
				{Parent: "profile", Columns: []column{{"profile_id", "id"}}, OnDelete: "CASCADE"},
				{Parent: "node", Columns: []column{{"node_id", "node_id"}}, OnDelete: "CASCADE"},
			},
			"rewrite_attempt_identifier": {{
				Parent:  "rewrite_attempt",
				Columns: []column{{"invocation_id", "invocation_id"}, {"attempt_index", "attempt_index"}}, OnDelete: "CASCADE",
			}},
		}

		for table, expected := range want {
			rows, err := db.Query(
				"SELECT id, seq, [table], [from], [to], on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq", table)
			if err != nil {
				t.Fatalf("pragma_foreign_key_list(%s): %v", table, err)
			}
			grouped := map[int]*fk{}
			var order []int
			for rows.Next() {
				var id, seq int
				var parent, from, to, onDelete string
				if err := rows.Scan(&id, &seq, &parent, &from, &to, &onDelete); err != nil {
					t.Fatalf("scan: %v", err)
				}
				if _, seen := grouped[id]; !seen {
					grouped[id] = &fk{Parent: parent, OnDelete: onDelete}
					order = append(order, id)
				}
				grouped[id].Columns = append(grouped[id].Columns, column{from, to})
			}
			rows.Close()

			got := make([]fk, 0, len(order))
			for _, id := range order {
				got = append(got, *grouped[id])
			}
			sortFKs(got)
			sortFKs(expected)
			if len(got) == 0 && len(expected) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, expected) {
				t.Errorf("%s foreign keys =\n%+v\nwant\n%+v", table, got, expected)
			}
		}
	})

	// The lexical scan below is a placeholder for the DEFERRED tables, which no
	// operation can populate yet. For the tables this slice writes, the check is
	// semantic: a valid row is written, then each constrained column is set to a
	// value its grammar forbids and the database must refuse it.
	t.Run("constrained columns reject invalid values", func(t *testing.T) {
		s := newStore(t)
		leaf := node(0, 0, 12)
		leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues()}
		write := snapshotWrite(document("essays/a.md", hashA, leaf))
		mustPutSnapshot(t, s, write)
		raw := openRaw(t, s)

		for _, c := range []struct{ table, column, value string }{
			{"snapshot", "id", "not-a-hash"},
			{"snapshot", "policy_digest", "nope"},
			{"document", "content_hash", "0123"},
			{"document", "path", "/absolute.md"},
			{"document", "register", "Not A Register"},
			{"document", "split", "holdout"},
			{"document", "admission", "maybe"},
			{"node", "kind", "stanza"},
			{"node", "role", "sonnet"},
			{"node", "offset", "-1"},
			{"node", "length", "0"},
			{"node", "ordinal", "-1"},
			{"feature_vector", "manifest_digest", "zz"},
			{"feature_vector", "set_version", "-1"},
			{"feature_vector", "tokens", "-1"},
			{"feature_value", "feature", "invented"},
			{"migration", "checksum", "not-hex"},
			{"migration", "version", "-1"},
		} {
			t.Run(c.table+"."+c.column, func(t *testing.T) {
				_, err := raw.Exec("UPDATE "+c.table+" SET "+c.column+" = ?", c.value)
				if err == nil {
					t.Errorf("accepted %q in %s.%s", c.value, c.table, c.column)
					// Put it back so later cases still have a valid row.
					t.Fatalf("schema does not constrain %s.%s", c.table, c.column)
				}
			})
		}
	})

	t.Run("deferred tables carry a CHECK naming each constrained column", func(t *testing.T) {
		// Counting CHECKs globally lets a redundant one on a trivial column mask
		// an unconstrained hash. Each column is looked for by name.
		constrained := map[string][]string{
			"snapshot":        {"id", "policy_digest"},
			"document":        {"document_id", "snapshot_id", "content_hash", "path", "register", "split", "admission", "language"},
			"node":            {"node_id", "document_id", "kind", "role", "exclusion", "offset", "length", "ordinal"},
			"feature_vector":  {"node_id", "manifest_digest", "set_version", "tokens", "lexical_tokens"},
			"feature_value":   {"node_id", "manifest_digest", "feature"},
			"profile":         {"id", "snapshot_id", "register", "unit", "variance_convention", "manifest_digest", "feature_set_version", "min_paragraph_lexical_tokens"},
			"profile_stat":    {"profile_id", "feature", "n", "min_observations"},
			"profile_head":    {"register", "profile_id"},
			"reference":       {"id", "profile_id", "split", "manifest_digest", "min_segments"},
			"reference_value": {"reference_id", "feature", "ordinal"},
			"threshold":       {"id", "profile_id", "reference_id", "population_id", "verdict"},
			"eval_result": {
				"id", "profile_id", "reference_id", "reason",
				"author_segments", "distractor_segments", "author_clusters", "distractor_clusters",
			},
			"exemplar_selection": {"id", "profile_id", "certificate_id", "n"},
			"exemplar_member":    {"selection_id", "node_id", "ordinal"},
			"rewrite_attempt": {
				"invocation_id", "profile_id", "provider_id", "node_id",
				"current_hash", "candidate_hash", "current_band", "candidate_band", "rejection", "attempt_index",
			},
			"rewrite_attempt_identifier": {"invocation_id", "identifier", "attempt_index", "ordinal"},
			"migration":                  {"checksum", "applied_at", "version"},
		}
		for table, columns := range constrained {
			var ddl string
			if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&ddl); err != nil {
				t.Fatalf("sql for %s: %v", table, err)
			}
			upper := strings.ToUpper(ddl)
			// Split on the CHECK KEYWORD, not the substring: "checksum" begins
			// with it, so splitting on "CHECK" removes that prefix from every
			// occurrence and the column can never be found.
			fragments := checkKeyword.Split(upper, -1)
			for _, column := range columns {
				var constrainedHere bool
				for _, fragment := range fragments[1:] {
					if strings.Contains(fragment, strings.ToUpper(column)) {
						constrainedHere = true
						break
					}
				}
				if !constrainedHere {
					t.Errorf("%s.%s has no CHECK naming it", table, column)
				}
			}
		}
	})

	t.Run("every primary key is the declared identity", func(t *testing.T) {
		// "Has a primary key" would pass a table keyed on a surrogate or on half
		// a composite identity, so the exact column sequence is declared.
		keys := map[string][]string{
			"snapshot": {"id"}, "document": {"document_id"}, "node": {"node_id"},
			"feature_vector": {"node_id", "manifest_digest"},
			"feature_value":  {"node_id", "manifest_digest", "feature"},
			"profile":        {"id"}, "profile_stat": {"profile_id", "feature"},
			"profile_head": {"register"}, "reference": {"id"},
			"reference_value": {"reference_id", "feature", "ordinal"},
			"threshold":       {"id"}, "eval_result": {"id"},
			"exemplar_selection": {"id"}, "exemplar_member": {"selection_id", "ordinal"},
			"rewrite_attempt":            {"invocation_id", "attempt_index"},
			"rewrite_attempt_identifier": {"invocation_id", "attempt_index", "ordinal"},
			"migration":                  {"version"},
		}
		for table, want := range keys {
			rows, err := db.Query("SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk", table)
			if err != nil {
				t.Fatalf("pragma: %v", err)
			}
			var got []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatalf("scan: %v", err)
				}
				got = append(got, name)
			}
			rows.Close()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s primary key = %v, want %v", table, got, want)
			}
		}
	})
}

// A tripwire beside TestConcurrentWritersInSeparateProcesses, not a substitute
// for it: a semaphore channel, an aliased import or a globally limited sql.DB
// would all evade this scan. It catches the obvious regression cheaply.
func TestThePackageHasNoGlobalLock(t *testing.T) {
	set := token.NewFileSet()
	pkgs, err := parser.ParseDir(set, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	var files int
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files++
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					rendered := new(strings.Builder)
					if value.Type != nil {
						_ = printer.Fprint(rendered, set, value.Type)
					}
					for _, v := range value.Values {
						_ = printer.Fprint(rendered, set, v)
					}
					if text := rendered.String(); strings.Contains(text, "sync.Mutex") || strings.Contains(text, "sync.RWMutex") {
						t.Errorf("%s declares a package-level lock (%s); concurrent writers would serialise on it rather than on SQLite", name, text)
					}
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("no non-test source was scanned; this guard is vacuous")
	}
}

// checkKeyword matches the CHECK keyword, not the letters inside "checksum".
var checkKeyword = regexp.MustCompile(`\bCHECK\s*\(`)

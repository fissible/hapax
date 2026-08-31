package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// Every enum column is closed over a set the OWNING package declares. Naming the
// values here instead would let the two drift apart silently: a value added to
// eval or rewrite would be refused by the database with nothing to say why.
func declaredVocabularies() map[string][]string {
	return map[string][]string{
		"document.split":                        stringsOf(corpus.Splits()),
		"document.admission":                    stringsOf(corpus.Admissions()),
		"document.language":                     stringsOf(corpus.CheckStates()),
		"profile.not_ready_reason":              readinessReasons(),
		"node.kind":                             stringsOf(text.Kinds()),
		"node.role":                             stringsOf(text.Roles()),
		"node.exclusion":                        stringsOf(text.ExclusionReasons()),
		"profile.unit":                          stringsOf(profile.Units()),
		"profile.variance_convention":           stringsOf(profile.VarianceConventions()),
		"reference.split":                       stringsOf(corpus.Splits()),
		"threshold.verdict":                     stringsOf(eval.ThresholdVerdicts()),
		"eval_result.reason":                    stringsOf(eval.ReleaseReasons()),
		"eval_result.discrimination_split":      stringsOf(corpus.Splits()),
		"eval_result.discrimination_clustering": stringsOf(eval.Clusterings()),
		"eval_result.discrimination_reason":     stringsOf(eval.DiscriminationReasons()),
		"eval_result.calibration_split":         stringsOf(corpus.Splits()),
		"eval_result.calibration_reason":        stringsOf(eval.CalibrationReasons()),
		// A band report is always ONE of the three; the empty band belongs to
		// an attempt that was never scored, not to a report.
		"calibration_band.band":          labelledBands(),
		"calibration_band.claims":        claimingClasses(),
		"calibration_band.reason":        stringsOf(eval.BandReportReasons()),
		"rewrite_attempt.provider_id":    stringsOf(llm.Providers()),
		"rewrite_attempt.current_band":   stringsOf(eval.Bands()),
		"rewrite_attempt.candidate_band": stringsOf(eval.Bands()),
		"rewrite_attempt.rejection":      stringsOf(rewrite.RejectionCodes()),
	}
}

func stringsOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	sort.Strings(out)
	return out
}

// enumUpdate writes one value into one enum column, moving with it whatever
// companion column a cross-column constraint ties it to. Without that the case
// would be measuring the constraint next door rather than the vocabulary.
func enumUpdate(table, column, value string) string {
	set := func(pairs ...string) string {
		return "UPDATE " + table + " SET " + strings.Join(pairs, ", ")
	}
	quoted := "'" + strings.ReplaceAll(value, "'", "''") + "'"
	switch table + "." + column {
	case "eval_result.reason":
		if value == "" {
			return set("reason = ''", "discriminates = 1", "calibrated = 1", "shippable = 1")
		}
		return set("reason = "+quoted, "discriminates = 0", "calibrated = 1", "shippable = 0")
	case "rewrite_attempt.rejection":
		if value == "" {
			return set("rejection = ''", "accepted = 1")
		}
		return set("rejection = "+quoted, "accepted = 0")
	case "rewrite_attempt.current_band", "rewrite_attempt.candidate_band":
		if value == "" {
			return set(column+" = ''", "accepted = 0", "rejection = 'not-improved'")
		}
		return set(column + " = " + quoted)
	case "profile.not_ready_reason":
		// Readiness is coupled to its reason, so the companion moves with it.
		if value == "" {
			return set("not_ready_reason = ''", "production_ready = 1")
		}
		return set("not_ready_reason = "+quoted, "production_ready = 0")
	case "calibration_band.claims":
		// Which class a band claims is fixed by the band, so the VALUE chooses
		// the row rather than any row taking any value.
		switch value {
		case "":
			return "UPDATE calibration_band SET claims = '' WHERE band = 'drifting'"
		case "author":
			return "UPDATE calibration_band SET claims = 'author' WHERE band = 'not-you'"
		case "distractor":
			return "UPDATE calibration_band SET claims = 'distractor' WHERE band = 'in-range'"
		}
		return "UPDATE calibration_band SET claims = " + quoted + " WHERE band = 'in-range'"
	case "calibration_band.reason":
		// A reason is coupled to emission — empty exactly when emitted — so the
		// companion moves with it, on one claiming report. drifting is left
		// alone: it is emitted and silent by construction.
		if value == "" {
			return "UPDATE calibration_band SET reason = '', emitted = 1 WHERE band = 'in-range'"
		}
		return "UPDATE calibration_band SET reason = " + quoted + ", emitted = 0 WHERE band = 'in-range'"
	case "calibration_band.band":
		// The band is half the primary key, so setting every row to one value
		// collides. Reduce to a single report first, then move it: what is
		// under test is the vocabulary, not the key.
		return "DELETE FROM calibration_band WHERE band <> 'drifting'; " +
			set("band = "+quoted)
	case "threshold.verdict":
		if value == string(eval.VerdictSeparated) {
			return set("verdict = "+quoted, "t_low = 0.4", "t_high = 0.9")
		}
		return set("verdict = "+quoted, "t_low = 0.9", "t_high = 0.4")
	default:
		return set(column + " = " + quoted)
	}
}

// Every value the owning package declares must actually be storable. A schema
// whose set has fallen behind refuses a value the rest of the tool can produce.
func TestEveryDeclaredEnumValueIsAcceptedByTheSchema(t *testing.T) {
	for qualified, values := range declaredVocabularies() {
		table, column, _ := strings.Cut(qualified, ".")
		t.Run(qualified, func(t *testing.T) {
			raw := seededRaw(t)
			for _, value := range values {
				t.Run(value, func(t *testing.T) {
					if err := attempt(t, raw, enumUpdate(table, column, value)); err != nil {
						t.Errorf("refused a declared value: %v", err)
					}
				})
			}
		})
	}
}

// And the values of every OTHER vocabulary in the schema, plus generic junk,
// are all refused. This is selected behavioural probing, NOT a closure proof: a
// set still listing a value its owning package has retired, and that no other
// column uses, would pass here. TestNoTableNamesAValueNoVocabularyDeclares is
// what looks for that.
func TestTheSchemaRefusesEveryForeignEnumValue(t *testing.T) {
	declared := declaredVocabularies()
	for qualified := range declared {
		table, column, _ := strings.Cut(qualified, ".")
		own := map[string]bool{}
		for _, value := range declared[qualified] {
			own[value] = true
		}
		probes := []string{"not-in-the-set", "NULL", strings.ToUpper(column), " "}
		for other, values := range declared {
			if other == qualified {
				continue
			}
			for _, value := range values {
				if value != "" && !own[value] {
					probes = append(probes, value)
				}
			}
		}
		t.Run(qualified, func(t *testing.T) {
			raw := seededRaw(t)
			for _, probe := range probes {
				t.Run(probe, func(t *testing.T) {
					if attempt(t, raw, enumUpdate(table, column, probe)) == nil {
						t.Errorf("accepted %q", probe)
					}
				})
			}
		})
	}
}

// The bands and rejection codes an attempt can carry include the empty one: an
// attempt refused before it was scored has no band, and an accepted one has no
// rejection code. Both are records the loop really writes.
func TestTheAttemptVocabulariesIncludeTheEmptyValue(t *testing.T) {
	for _, c := range []struct {
		name   string
		values []string
	}{
		{"bands", stringsOf(eval.Bands())},
		{"rejection codes", stringsOf(rewrite.RejectionCodes())},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, value := range c.values {
				if value == "" {
					return
				}
			}
			t.Errorf("%v does not name the empty value", c.values)
		})
	}
}

// ---------------------------------------------------------------------------
// No free-text column
// ---------------------------------------------------------------------------

// DESIGN claims there is no free-text column anywhere. That is only checkable
// if every textual column is assigned a grammar AND that grammar is enforced,
// so every column is named here and every grammar carries values the database
// must refuse. A column added without a grammar fails the first test; a grammar
// that constrains nothing fails the second.
var textualColumnGrammars = map[string]string{
	// The distractor pool holds identities and digests and nothing else, so
	// every textual column of it is hex. A column that accepted arbitrary text
	// is where a filename would eventually go.
	"distractor_pool.id": "hex", "distractor_pool.policy_digest": "hex",
	"distractor_pool.created_at":          "time",
	"distractor_pool_member.pool_id":      "hex",
	"distractor_pool_member.content_hash": "hex",
	"snapshot.id":                         "hex", "snapshot.policy_digest": "hex", "snapshot.created_at": "time",
	"document.document_id": "hex", "document.snapshot_id": "hex", "document.content_hash": "hex",
	"document.path": "rel", "document.register": "register", "document.split": "enum",
	"document.admission": "enum", "document.language": "enum", "document.unavailable_at": "time",
	"node.node_id": "hex", "node.document_id": "hex", "node.kind": "enum",
	"node.role": "enum", "node.exclusion": "enum", "node.containers": "enum-list",
	"feature_vector.node_id": "hex", "feature_vector.manifest_digest": "hex",
	"feature_value.node_id": "hex", "feature_value.manifest_digest": "hex",
	"feature_value.feature": "feature",
	"profile.id":            "hex", "profile.snapshot_id": "hex", "profile.register": "register",
	"profile.not_ready_reason": "enum",
	"profile.unit":             "enum", "profile.variance_convention": "enum", "profile.manifest_digest": "hex",
	"profile_stat.profile_id": "hex", "profile_stat.feature": "feature",
	"profile_head.register": "register", "profile_head.profile_id": "hex",
	"profile_head.updated_at": "time",
	"reference.id":            "hex", "reference.profile_id": "hex", "reference.split": "enum",
	"reference.manifest_digest":    "hex",
	"reference_value.reference_id": "hex", "reference_value.feature": "feature",
	"threshold.id": "hex", "threshold.profile_id": "hex", "threshold.reference_id": "hex",
	"threshold.population_id": "hex", "threshold.verdict": "enum",
	"eval_result.id": "hex", "eval_result.profile_id": "hex", "eval_result.reference_id": "hex",
	"eval_result.reason":            "enum",
	"eval_result.discrimination_id": "hex", "eval_result.discrimination_population_id": "hex",
	"eval_result.discrimination_manifest_digest":    "hex",
	"eval_result.discrimination_weight_scheme":      "algorithm",
	"eval_result.discrimination_distance_algorithm": "algorithm",
	"eval_result.discrimination_scored_tiers":       "tier-list",
	"eval_result.discrimination_split":              "enum",
	"eval_result.discrimination_algorithm":          "algorithm",
	"eval_result.discrimination_clustering":         "enum",
	"eval_result.discrimination_reason":             "enum",
	"eval_result.calibration_id":                    "hex",
	"eval_result.calibration_thresholds_id":         "hex",
	"eval_result.calibration_population_id":         "hex",
	"eval_result.calibration_manifest_digest":       "hex",
	"eval_result.calibration_weight_scheme":         "algorithm",
	"eval_result.calibration_distance_algorithm":    "algorithm",
	"eval_result.calibration_scored_tiers":          "tier-list",
	"eval_result.calibration_split":                 "enum",
	"eval_result.calibration_algorithm":             "algorithm",
	"eval_result.calibration_reason":                "enum",
	"calibration_band.eval_result_id":               "hex",
	"calibration_band.band":                         "enum",
	"calibration_band.claims":                       "enum",
	"calibration_band.reason":                       "enum",
	"release_head.profile_id":                       "hex",
	"release_head.eval_result_id":                   "hex",
	"release_head.updated_at":                       "time",
	"exemplar_selection.id":                         "hex", "exemplar_selection.profile_id": "hex",
	"exemplar_selection.certificate_id": "hex",
	"exemplar_member.selection_id":      "hex", "exemplar_member.node_id": "hex",
	"rewrite_attempt.invocation_id": "hex", "rewrite_attempt.profile_id": "hex",
	"rewrite_attempt.node_id": "hex", "rewrite_attempt.provider_id": "enum",
	"rewrite_attempt.current_hash": "hex", "rewrite_attempt.candidate_hash": "hex",
	"rewrite_attempt.current_band": "enum", "rewrite_attempt.candidate_band": "enum",
	"rewrite_attempt.rejection":                "enum",
	"rewrite_attempt_identifier.invocation_id": "hex",
	"rewrite_attempt_identifier.identifier":    "preserve-identifier",
	"migration.checksum":                       "hex", "migration.applied_at": "time",
}

// Values each grammar must refuse. "enum" is deliberately absent: it is covered
// by the vocabulary tests above, so a column labelled enum that declares no
// closed set fails there rather than passing quietly here.
//
// Two grammars are enforced at the SHAPE only, membership being left to the Go
// codec: "enum-list", because SQLite cannot check each element of a joined
// list, and "preserve-identifier", whose class and direction come from a
// versioned vocabulary. Their membership cases are in
// TestDamageTheSchemaCannotPreventIsCorruptNotSmaller.
var grammarProbes = map[string][]string{
	"hex":      {"", "not-a-hash", "abc123", strings.ToUpper(hashA), hashA[:63], hashA + "a"},
	"rel":      {"", "/absolute.md", "../outside.md", `sub\essay.md`, "a//b.md", ".."},
	"register": {"", "Diary", "my essays", "-leading", "essays/2026", strings.Repeat("a", 33)},
	"time":     {"", "yesterday", "2026/01/01", "2026-01-01 00:00:00"},
	// A versioned contract identifier: lower case, digits, hyphens, ending in a
	// version. Not free text, and not an enum either — these are owned by the
	// components that declare them and change when their contracts do.
	"algorithm": {"", "Uniform V1", "uniform v1", "uniform_v1"},
	// A sorted, duplicate-free list of feature tiers. Upper case, so it cannot
	// borrow the container grammar.
	"tier-list": {"a", "A,A", "A B", "Z"},
	"feature":   {"", "Has Space", "UPPER"},
	"enum-list": {"prose, with punctuation", "Document", "document document"},
	"preserve-identifier": {
		"", "1979", "number:1979", "preserve-v1:number:lost:the year 1979",
		"preserve-v1:number:lost:0123456789abcde", "preserve-v2:number:lost:0123456789abcdef",
	},
}

func TestEveryTextualColumnHasADeclaredGrammar(t *testing.T) {
	db := openRaw(t, newStore(t))
	for table := range declaredSchema {
		rows, err := db.Query("SELECT name FROM pragma_table_info(?) WHERE upper(type) = 'TEXT'", table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if _, declared := textualColumnGrammars[table+"."+column]; !declared {
				t.Errorf("%s.%s is textual and has no declared grammar", table, column)
			}
		}
		rows.Close()
	}
	for qualified := range textualColumnGrammars {
		table, column, _ := strings.Cut(qualified, ".")
		var declaredType string
		err := db.QueryRow("SELECT type FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&declaredType)
		if err != nil {
			t.Errorf("%s: %v", qualified, err)
			continue
		}
		if !strings.EqualFold(declaredType, "TEXT") {
			t.Errorf("%s is declared %s, not TEXT", qualified, declaredType)
		}
	}
}

// A declared grammar that constrains nothing is a label. Each one is exercised
// against the database directly, so the guarantee does not rest on the Go
// validation a raw connection bypasses.
func TestEveryDeclaredGrammarIsEnforcedByTheDatabase(t *testing.T) {
	for qualified, grammar := range textualColumnGrammars {
		probes, enforced := grammarProbes[grammar]
		if !enforced {
			continue
		}
		table, column, _ := strings.Cut(qualified, ".")
		t.Run(qualified, func(t *testing.T) {
			raw := seededRaw(t)
			if rowsIn(t, raw, table) == 0 {
				t.Fatalf("%s has no row to damage; every probe would be vacuous", table)
			}
			for _, probe := range probes {
				t.Run(probe, func(t *testing.T) {
					// Exactly one row, chosen deterministically: updating every
					// row of a keyed column collides on the primary key, which
					// would pass the case for a reason that is not the grammar.
					if attempt(t, raw,
						"UPDATE "+table+" SET "+column+" = ? WHERE rowid = (SELECT min(rowid) FROM "+table+")",
						probe) == nil {
						t.Errorf("accepted %q", probe)
					}
				})
			}
		})
	}
}

func rowsIn(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return count
}

// Every grammar named above is one the probes can refute, or is explicitly the
// enum grammar. A typo in a grammar name would otherwise skip a column silently.
func TestEveryGrammarNameIsOneTheProbesCover(t *testing.T) {
	for qualified, grammar := range textualColumnGrammars {
		if grammar == "enum" {
			if _, closed := declaredVocabularies()[qualified]; !closed {
				t.Errorf("%s is labelled enum but declares no vocabulary", qualified)
			}
			continue
		}
		if _, known := grammarProbes[grammar]; !known {
			t.Errorf("%s names grammar %q, which has no probes", qualified, grammar)
		}
	}
}

// A stale value — one a schema still admits after its owning package dropped it
// — cannot be found by probing, because nothing knows to probe for it. So the
// DDL's own string literals are read back and each one must belong to a
// vocabulary that table declares.
//
// A TRIPWIRE, and scoped like one: only literals shaped like a vocabulary value
// are considered, since GLOB patterns and the empty string appear in the same
// DDL for unrelated reasons. It catches the retired value; it does not prove
// closure any more than the probes above do.
var vocabularyShaped = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func TestNoTableNamesAValueNoVocabularyDeclares(t *testing.T) {
	db := openRaw(t, newStore(t))
	declared := declaredVocabularies()
	for table := range declaredSchema {
		known := map[string]bool{}
		for qualified, values := range declared {
			if owner, _, _ := strings.Cut(qualified, "."); owner == table {
				for _, value := range values {
					known[value] = true
				}
			}
		}
		var ddl string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&ddl); err != nil {
			t.Fatalf("sql for %s: %v", table, err)
		}
		for _, match := range sqlLiteral.FindAllStringSubmatch(ddl, -1) {
			literal := match[1]
			if !vocabularyShaped.MatchString(literal) {
				continue
			}
			if !known[literal] {
				t.Errorf("%s admits %q, which no vocabulary it declares contains", table, literal)
			}
		}
	}
}

var sqlLiteral = regexp.MustCompile(`'([^']*)'`)

// relaxEnum removes one column's closed set from the live schema, so a value
// the database would normally refuse can be stored. That is not a hypothetical:
// the ledger, not the schema, answers "what version is this", so a database
// whose DDL was altered outside this binary still opens — and read validation
// is the only thing left between it and a believed result.
//
// The substitution follows the schema's spelling and fails loudly if it stops
// matching, rather than quietly relaxing nothing.
func relaxEnum(t *testing.T, s *store.Store, table, column string) {
	t.Helper()
	db := openRaw(t, s)
	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&ddl); err != nil {
		t.Fatalf("sql for %s: %v", table, err)
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\s+IN\s*\([^)]*\)`)
	relaxed := pattern.ReplaceAllString(ddl, column+" IS NOT NULL")
	if relaxed == ddl {
		t.Fatalf("no closed set found for %s.%s; this damage would be vacuous", table, column)
	}
	for _, statement := range []string{"PRAGMA writable_schema=ON"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := db.Exec("UPDATE sqlite_master SET sql = ? WHERE name = ?", relaxed, table); err != nil {
		t.Fatalf("relaxing %s: %v", table, err)
	}
	// Editing sqlite_master does not advance the schema cookie by itself, so a
	// connection that has already parsed the schema would keep the old CHECK.
	var cookie int
	if err := db.QueryRow("PRAGMA schema_version").Scan(&cookie); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA schema_version = %d", cookie+1)); err != nil {
		t.Fatalf("advancing schema_version: %v", err)
	}
	if _, err := db.Exec("PRAGMA writable_schema=OFF"); err != nil {
		t.Fatalf("writable_schema off: %v", err)
	}
}

// A reader that trusts the database is not validating. EVERY declared enum
// column is handed a value outside its vocabulary, driven from the vocabulary
// inventory itself so a column added there cannot go unprobed here.
func TestAStoredEnumOutsideItsVocabularyIsCorruptOnRead(t *testing.T) {
	loadOwner := map[string]func(s *store.Store, ids seededIDs) error{
		"document.split": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"document.admission": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"document.language": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"node.kind": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"node.role": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"node.exclusion": func(s *store.Store, ids seededIDs) error {
			_, err := s.Snapshot(ctx(), ids.Snapshot)
			return err
		},
		"profile.unit": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadProfile(ctx(), ids.Profile)
			return err
		},
		"profile.variance_convention": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadProfile(ctx(), ids.Profile)
			return err
		},
		"profile.not_ready_reason": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadProfile(ctx(), ids.Profile)
			return err
		},
		"reference.split": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadReference(ctx(), ids.Reference)
			return err
		},
		"threshold.verdict": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadThreshold(ctx(), ids.Threshold)
			return err
		},
		"eval_result.reason": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"rewrite_attempt.provider_id": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
			return err
		},
		"rewrite_attempt.current_band": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
			return err
		},
		"rewrite_attempt.candidate_band": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
			return err
		},
		"rewrite_attempt.rejection": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
			return err
		},
		// Every column of the release reaches the reader through one loader.
		"eval_result.discrimination_split": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"eval_result.discrimination_clustering": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"eval_result.discrimination_reason": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"eval_result.calibration_split": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"eval_result.calibration_reason": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"calibration_band.band": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"calibration_band.claims": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
		"calibration_band.reason": func(s *store.Store, ids seededIDs) error {
			_, err := s.LoadEvalResult(ctx(), ids.EvalResult)
			return err
		},
	}

	for qualified := range declaredVocabularies() {
		if _, covered := loadOwner[qualified]; !covered {
			t.Errorf("%s declares a vocabulary but no reader probes it", qualified)
		}
	}
	for qualified := range loadOwner {
		if _, declared := declaredVocabularies()[qualified]; !declared {
			t.Errorf("%s is probed but declares no vocabulary", qualified)
		}
	}

	for qualified, load := range loadOwner {
		table, column, _ := strings.Cut(qualified, ".")
		t.Run(qualified, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)
			relaxEnum(t, s, table, column)
			if _, err := openRaw(t, s).Exec(enumUpdate(table, column, "not-in-the-set")); err != nil {
				t.Fatalf("damaging: %v", err)
			}
			if err := load(s, ids); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// seededRaw is one seeded database, opened raw, shared by every probe in a
// case. Building one per probe meant 714 migrations and 714 seeds for the
// vocabulary tests alone, which took the -race suite past four minutes on CI.
func seededRaw(t *testing.T) *sql.DB {
	t.Helper()
	s := newStore(t)
	seedEveryArtifact(t, s)
	return openRaw(t, s)
}

// attempt runs one damaging statement inside a transaction that is ALWAYS
// rolled back, so sharing a database between probes cannot let one probe's
// write — including one that wrongly succeeded — reach the next.
func attempt(t *testing.T, db *sql.DB, statement string, arguments ...any) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(context.Background(), statement, arguments...)
	return err
}

// labelledBands is eval's band vocabulary without the empty member. A band
// report always names one of the three; the empty band is what a rewrite
// attempt carries when it was never scored.
func labelledBands() []string {
	var out []string
	for _, band := range stringsOf(eval.Bands()) {
		if band != "" {
			out = append(out, band)
		}
	}
	sort.Strings(out)
	return out
}

// claimingClasses is eval's class vocabulary plus the empty one, which is what
// the drifting report carries: it claims nothing.
func claimingClasses() []string {
	out := append([]string{""}, stringsOf(eval.Classes())...)
	sort.Strings(out)
	return out
}

// readinessReasons is profile's declared set plus the empty one, which means
// READY rather than an unnamed reason.
func readinessReasons() []string {
	out := append([]string{""}, profile.NotReadyReasons()...)
	sort.Strings(out)
	return out
}

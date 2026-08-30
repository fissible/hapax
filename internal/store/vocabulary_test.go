package store_test

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/text"
)

// Every enum column is closed over a set the OWNING package declares. Naming the
// values here instead would let the two drift apart silently: a value added to
// eval or rewrite would be refused by the database with nothing to say why.
func declaredVocabularies() map[string][]string {
	return map[string][]string{
		"document.split":                 stringsOf(corpus.Splits()),
		"document.admission":             stringsOf(corpus.Admissions()),
		"node.kind":                      stringsOf(text.Kinds()),
		"node.role":                      stringsOf(text.Roles()),
		"node.exclusion":                 stringsOf(text.ExclusionReasons()),
		"profile.unit":                   stringsOf(profile.Units()),
		"profile.variance_convention":    stringsOf(profile.VarianceConventions()),
		"reference.split":                stringsOf(corpus.Splits()),
		"threshold.verdict":              stringsOf(eval.ThresholdVerdicts()),
		"eval_result.reason":             stringsOf(eval.ReleaseReasons()),
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
		for _, value := range values {
			t.Run(qualified+"="+value, func(t *testing.T) {
				s := newStore(t)
				seedEveryArtifact(t, s)
				if _, err := openRaw(t, s).Exec(enumUpdate(table, column, value)); err != nil {
					t.Errorf("refused a declared value: %v", err)
				}
			})
		}
	}
}

// And nothing else is storable. The probes are every value of every OTHER
// vocabulary in the schema plus generic junk, which is what catches a set that
// has grown stale in the other direction — still listing a value its owning
// package has dropped.
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
		for _, probe := range probes {
			t.Run(qualified+"="+probe, func(t *testing.T) {
				s := newStore(t)
				seedEveryArtifact(t, s)
				if _, err := openRaw(t, s).Exec(enumUpdate(table, column, probe)); err == nil {
					t.Errorf("accepted %q", probe)
				}
			})
		}
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
	"snapshot.id": "hex", "snapshot.policy_digest": "hex", "snapshot.created_at": "time",
	"document.document_id": "hex", "document.snapshot_id": "hex", "document.content_hash": "hex",
	"document.path": "rel", "document.register": "register", "document.split": "enum",
	"document.admission": "enum", "document.language": "language", "document.unavailable_at": "time",
	"node.node_id": "hex", "node.document_id": "hex", "node.kind": "enum",
	"node.role": "enum", "node.exclusion": "enum", "node.containers": "enum-list",
	"feature_vector.node_id": "hex", "feature_vector.manifest_digest": "hex",
	"feature_value.node_id": "hex", "feature_value.manifest_digest": "hex",
	"feature_value.feature": "feature",
	"profile.id":            "hex", "profile.snapshot_id": "hex", "profile.register": "register",
	"profile.unit": "enum", "profile.variance_convention": "enum", "profile.manifest_digest": "hex",
	"profile_stat.profile_id": "hex", "profile_stat.feature": "feature",
	"profile_head.register": "register", "profile_head.profile_id": "hex",
	"profile_head.updated_at": "time",
	"reference.id":            "hex", "reference.profile_id": "hex", "reference.split": "enum",
	"reference.manifest_digest":    "hex",
	"reference_value.reference_id": "hex", "reference_value.feature": "feature",
	"threshold.id": "hex", "threshold.profile_id": "hex", "threshold.reference_id": "hex",
	"threshold.population_id": "hex", "threshold.verdict": "enum",
	"eval_result.id": "hex", "eval_result.profile_id": "hex", "eval_result.reference_id": "hex",
	"eval_result.reason":    "enum",
	"exemplar_selection.id": "hex", "exemplar_selection.profile_id": "hex",
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
	"hex":       {"", "not-a-hash", "abc123", strings.ToUpper(hashA), hashA[:63], hashA + "a"},
	"rel":       {"", "/absolute.md", "../outside.md", `sub\essay.md`, "a//b.md", ".."},
	"register":  {"", "Diary", "my essays", "-leading", "essays/2026", strings.Repeat("a", 33)},
	"language":  {"", "English, mostly", "EN", "en_GB"},
	"time":      {"", "yesterday", "2026/01/01", "2026-01-01 00:00:00"},
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
		for _, probe := range probes {
			t.Run(qualified+"/"+probe, func(t *testing.T) {
				s := newStore(t)
				seedEveryArtifact(t, s)
				raw := openRaw(t, s)
				if rowsIn(t, raw, table) == 0 {
					t.Fatalf("%s has no row to damage; the probe would be vacuous", table)
				}
				if _, err := raw.Exec("UPDATE "+table+" SET "+column+" = ?", probe); err == nil {
					t.Errorf("accepted %q", probe)
				}
			})
		}
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

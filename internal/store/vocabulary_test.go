package store_test

import (
	"database/sql"
	"reflect"
	"regexp"
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

// inList reads a column's closed set out of the DDL. A tripwire beside the
// behavioural check below: it is what catches a set that is merely a SUPERSET
// of what the owning package declares, which no insert can reveal.
var inList = regexp.MustCompile(`(?i)\b(\w+)\s+IN\s*\(([^)]*)\)`)

func schemaVocabulary(t *testing.T, db *sql.DB, table, column string) []string {
	t.Helper()
	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&ddl); err != nil {
		t.Fatalf("sql for %s: %v", table, err)
	}
	for _, match := range inList.FindAllStringSubmatch(ddl, -1) {
		if match[1] != column {
			continue
		}
		var out []string
		for _, value := range strings.Split(match[2], ",") {
			out = append(out, strings.Trim(strings.TrimSpace(value), "'"))
		}
		sort.Strings(out)
		return out
	}
	t.Fatalf("%s.%s has no IN list", table, column)
	return nil
}

func TestEveryEnumColumnIsExactlyItsOwningPackagesVocabulary(t *testing.T) {
	db := openRaw(t, newStore(t))
	for qualified, want := range declaredVocabularies() {
		table, column, _ := strings.Cut(qualified, ".")
		if got := schemaVocabulary(t, db, table, column); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", qualified, got, want)
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

// DESIGN's claim is that there is no free-text column anywhere. That is only
// checkable if every textual column is assigned a grammar, so every one is
// named here and the test fails when the schema grows a column this list has
// not classified.
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
	// And nothing is declared that the schema does not have.
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

// The database refuses a value outside each closed set even on a raw connection,
// so the guarantee does not rest on the Go validation alone.
func TestTheDatabaseItselfRefusesAValueOutsideEachClosedSet(t *testing.T) {
	s := newStore(t)
	seedEveryArtifact(t, s)
	raw := openRaw(t, s)
	for qualified := range declaredVocabularies() {
		table, column, _ := strings.Cut(qualified, ".")
		t.Run(qualified, func(t *testing.T) {
			if _, err := raw.Exec("UPDATE " + table + " SET " + column + " = 'not-in-the-set'"); err == nil {
				t.Errorf("%s accepted a value outside its set", qualified)
			}
		})
	}
}

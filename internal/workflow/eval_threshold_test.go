package workflow_test

// #83's other writer.
//
// `hapax eval` persists a threshold itself, before any release is put, and
// derives its verdict from whether the BOOTSTRAP INTERVALS shipped:
//
//	Verdict: func() eval.ThresholdVerdict {
//		if intervals.Shippable { return eval.VerdictSeparated }
//		return eval.VerdictPairIncompatible
//	}()
//
// The store's rule is `(verdict == separated) == (Low < High)`. Interval
// shippability is a property of confidence intervals; ordering is a property of
// two numbers. Where they differ the write is refused and `hapax eval` exits 3
// with `store: invalid: threshold verdict` — an operational failure standing in
// for a measurement.
//
// A fix that corrects Calibrate's sort and internal/store's PutRelease and
// leaves this writer alone passes every test in internal/eval and
// internal/store, and still exits 3.
//
// These tests do not reach for the specific numbers that trip it. They assert
// the invariant over every evaluation the fixtures can produce: whatever eval
// persists, the store's own rule holds over it, and eval completes rather than
// failing.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/workflow"
)

// storedThresholds reads every threshold row through a raw connection, because
// what is under test is the artifact as persisted rather than anything the
// loader might normalise on the way back.
func storedThresholds(t *testing.T, root string) []struct {
	ID        string
	Low, High float64
	Verdict   string
} {
	t.Helper()
	db, err := sql.Open("sqlite", defaultStorePath(root))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(),
		"SELECT id, t_low, t_high, verdict FROM threshold")
	if err != nil {
		t.Fatalf("query thresholds: %v", err)
	}
	defer rows.Close()

	var out []struct {
		ID        string
		Low, High float64
		Verdict   string
	}
	for rows.Next() {
		var row struct {
			ID        string
			Low, High float64
			Verdict   string
		}
		if err := rows.Scan(&row.ID, &row.Low, &row.High, &row.Verdict); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// Every threshold eval writes satisfies the store's own rule.
//
// # What this is, honestly
//
// A GUARD, not a reproduction. None of these fixtures reaches the inverted
// ordering that trips the defect — I tried four corpus shapes and every one
// gave two boundaries close enough together that the broken derivation and the
// correct one agree, so this test passes against unfixed code.
//
// It is here anyway for two reasons. It is the only test that reads what
// `hapax eval` actually WROTE, through a raw connection, so a future change to
// the writer that reintroduces the confusion is caught over whatever fixtures
// exist then. And it fails loudly if eval ever starts failing operationally on
// a corpus pair, which is the user-visible half of #83.
//
// The reproductions live where the defect can be reached deterministically:
// internal/eval/threshold_ordering_test.go for Calibrate, CalibrateBands and
// Bootstrap, and internal/store/threshold_verdict_test.go for PutRelease.
func TestEveryThresholdEvalWritesAgreesWithItsOwnOrdering(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name        string
		distractors func(*testing.T) string
	}{
		{"a pool that separates", func(t *testing.T) string { return distractorCorpus(t, 20) }},
		// A pool of the author's own documents: the two populations are as
		// close as they can be, so the thresholds land on top of each other.
		{"the author's own documents", func(t *testing.T) string { return "" }}, // replaced below
		// Twice as many strangers, which moves the quantiles the thresholds are
		// chosen from without separating the populations.
		{"a larger pool", func(t *testing.T) string { return distractorCorpus(t, 40) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := indexedCorpus(t)
			pool := c.distractors(t)
			if c.name == "the author's own documents" {
				pool = root
			}

			result, err := workflow.Default().Eval(ctx(), evalRequest(root, pool))
			if err != nil {
				t.Fatalf("Eval failed operationally: %v\n\nA pair of corpora that does not "+
					"separate is a measurement. Reporting it as a failure is #83.", err)
			}
			// Adverse or not, it completed and persisted something.
			if result.ProfileID == "" {
				t.Fatal("the evaluation persisted no profile identity")
			}

			stored := storedThresholds(t, root)
			if len(stored) == 0 {
				t.Fatal("no threshold was persisted; this test would assert nothing")
			}
			for _, row := range stored {
				want := eval.VerdictFor(row.Low, row.High)
				if eval.ThresholdVerdict(row.Verdict) != want {
					t.Errorf("threshold %s stored verdict %q beside Low = %v and High = %v, want %q",
						row.ID[:8], row.Verdict, row.Low, row.High, want)
				}
			}
		})
	}
}

// And the user-visible half: an evaluation that cannot ship still EXITS as a
// completed measurement, with a reason a person can act on.
//
// This is what #83 costs when it fires. `hapax eval` on a real fifty-document
// corpus against four hundred strangers exited 3 with a store diagnostic, which
// tells the author their database is broken when what happened is that their
// writing did not separate from other people's.
func TestAnEvaluationThatCannotShipIsAMeasurementAndNotAFailure(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)

	// The author's own documents as the distractor pool: the two populations
	// are as close as they can be, so nothing can separate them.
	result, err := workflow.Default().Eval(ctx(), evalRequest(root, root))
	if err != nil {
		t.Fatalf("Eval failed operationally: %v", err)
	}

	if result.Shippable {
		t.Fatal("a profile evaluated against its own corpus called itself shippable")
	}
	if result.Reason == "" {
		t.Error("an adverse evaluation gave no reason a person could act on")
	}
	if !contains(workflow.EvalReasons(), result.Reason) {
		t.Errorf("reason %q is not one of the declared evaluation reasons", result.Reason)
	}
	// The release is still persisted, so the run is auditable rather than lost.
	if result.ReleaseID == "" {
		t.Error("an adverse evaluation persisted no release")
	}
}

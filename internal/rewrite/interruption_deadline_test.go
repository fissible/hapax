package rewrite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

func TestExpiredDeadlineReturnsZeroOutcomeWithoutProviderAttempt(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	provider := &cancellingProvider{candidates: []string{better}}
	loop, _, _, _, store := loopOverWithProvider(t,
		map[string]score.Report{original: scored(0.50), better: scored(0.40)}, provider, passingGate())
	outcome, err := loop.Rewrite(ctx, segmentUnderTest())
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(outcome, rewrite.Outcome{}) {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
	if provider.calls != 0 || len(store.attempts) != 0 {
		t.Fatalf("provider calls %d, recorded attempts %d", provider.calls, len(store.attempts))
	}
}

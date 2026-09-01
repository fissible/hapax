package workflow_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/workflow"

	_ "modernc.org/sqlite"
)

// store.immediate retries BEGIN IMMEDIATE every five milliseconds until its
// CONTEXT is done — it does not give up at SQLite's busy timeout. So an index
// with no bound does not fail when another writer holds the lock; it waits
// forever. Running `hapax index` in two terminals is an ordinary thing to do,
// and a CLI that hangs on it is worse than one that says it could not get in.

// holdWriteLock takes the database's writer lock and keeps it until the test
// ends. A deferred BEGIN takes nothing, so it is the write that acquires it.
// Held with plain database/sql rather than through store, because what is being
// held is a property of the file and not of a schema this package should know.
func holdWriteLock(t *testing.T, path string) {
	t.Helper()
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	held, err := holder.BeginTx(ctx(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Fresh database per test, so this table is absent and the write is real.
	if _, err := held.ExecContext(ctx(), "CREATE TABLE hapax_lock_probe (x INTEGER)"); err != nil {
		t.Fatalf("taking the writer lock: %v", err)
	}
	t.Cleanup(func() { _ = held.Rollback() })
}

// contendedIndex runs an index against a locked store and reports how long it
// took to give up. The corpus is deliberately tiny: the fixture's own work would
// otherwise dominate the interval being measured.
func contendedIndex(t *testing.T, wait time.Duration) (time.Duration, error) {
	t.Helper()
	root := corpusOf(t, 2)
	first := indexed(t, indexRequest(root))
	holdWriteLock(t, first.StorePath)

	request := indexRequest(root)
	request.LockWait = wait

	type outcome struct {
		elapsed time.Duration
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		started := time.Now()
		_, err := workflow.Default().Index(ctx(), request)
		done <- outcome{time.Since(started), err}
	}()

	budget := wait
	if budget == 0 {
		budget = workflow.DefaultLockWait()
	}
	select {
	case got := <-done:
		return got.elapsed, got.err
	case <-time.After(budget + 30*time.Second):
		t.Fatal("the index never returned; it is waiting on the lock forever")
		return 0, nil
	}
}

// The requested budget is the one spent: it gives up for the reason it was told
// to, and it spends something close to what it was given. Accepting any error
// would pass an implementation that returned the driver's busy error at once,
// and accepting any duration would pass one whose budget is hard-coded.
func TestAnIndexSpendsTheLockBudgetItWasGivenAndThenGivesUp(t *testing.T) {
	t.Parallel()
	const wait = 750 * time.Millisecond
	elapsed, err := contendedIndex(t, wait)

	if err == nil {
		t.Fatal("indexed into a store another writer had locked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("gave up with %v; a lock budget that expires is a deadline", err)
	}
	// Generous on both sides — CI is slower than this machine and the point is
	// the budget was respected, not that it was precise.
	if elapsed < wait/2 {
		t.Errorf("gave up after %s against a %s budget; it did not wait", elapsed, wait)
	}
	if elapsed > wait*20 {
		t.Errorf("took %s against a %s budget; the budget is not the one being spent", elapsed, wait)
	}
}

// An unset LockWait is the declared default and not an unbounded wait, because
// the CLI carries no flag for it and "the user did not say" must not mean "wait
// forever". Timed, not merely bounded: an implementation that ignored the
// default and gave up after a millisecond would satisfy a bound alone.
func TestAnUnsetLockWaitIsTheDeclaredDefault(t *testing.T) {
	t.Parallel()
	// Pinned, not merely positive. This test WAITS the default out in order to
	// prove the acquisition path honours it, so the value is a standing cost on
	// every suite run and choosing it is a decision rather than a detail. One
	// second is long enough for a competing index to finish a small commit and
	// short enough that paying it each run is reasonable.
	if workflow.DefaultLockWait() != time.Second {
		t.Fatalf("DefaultLockWait() = %v, want 1s", workflow.DefaultLockWait())
	}
	elapsed, err := contendedIndex(t, 0)

	if err == nil {
		t.Fatal("indexed into a store another writer had locked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("gave up with %v", err)
	}
	if elapsed < workflow.DefaultLockWait()/2 {
		t.Errorf("an unset budget gave up after %s, well short of the declared %s",
			elapsed, workflow.DefaultLockWait())
	}
	if elapsed > workflow.DefaultLockWait()*4 {
		t.Errorf("an unset budget spent %s against a declared %s", elapsed, workflow.DefaultLockWait())
	}
}

// And the wait is a budget on ACQUIRING the lock, not a deadline on the command:
// an index that gets the lock immediately is not cut short by a small one, so
// setting a budget cannot make an otherwise fine index fail.
func TestASmallLockWaitDoesNotCutShortAnIndexThatGetsTheLock(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	request := indexRequest(root)
	request.LockWait = time.Millisecond

	result, err := workflow.Default().Index(ctx(), request)
	if err != nil {
		t.Fatalf("Index with a one-millisecond lock budget and no contention: %v", err)
	}
	if result.Mode != workflow.IndexProfileAndReference {
		t.Errorf("mode = %q", result.Mode)
	}
}

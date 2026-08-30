package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
)

// SQLite lock acquisition and a caller-bounded busy timeout are part of the
// contract, so they are tested across real processes. Goroutines in one process
// can be serialised by a mutex, a semaphore or a pool limit and would pass while
// proving nothing.
const (
	writerEnv = "HAPAX_STORE_TEST_WRITER"
	dbEnv     = "HAPAX_STORE_TEST_DB"
)

// TestMain lets this binary re-exec itself as a writer.
func TestMain(m *testing.M) {
	if os.Getenv(writerEnv) == "" {
		os.Exit(m.Run())
	}
	s, err := store.Open(os.Getenv(dbEnv))
	if err != nil {
		os.Stderr.WriteString("open: " + err.Error())
		os.Exit(2)
	}
	// Report ready with the connection already open, then block until the
	// parent releases every child at once. Without this the children start
	// staggered and may never collide, so a lock-racy store would pass.
	os.Stdout.WriteString("ready\n")
	if _, err := io.ReadAll(os.Stdin); err != nil {
		os.Stderr.WriteString("barrier: " + err.Error())
		os.Exit(4)
	}
	if err := s.PutSnapshot(ctxBackground(), concurrentWrite()); err != nil {
		_ = s.Close()
		os.Stderr.WriteString("put: " + err.Error())
		os.Exit(3)
	}
	_ = s.Close()
	os.Exit(0)
}

// Eight separate processes writing the same aggregate all succeed: an identical
// write is idempotent, and the loser of a lock race rereads rather than failing.
func TestConcurrentWritersInSeparateProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	seeded, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	const writers = 8
	type child struct {
		cmd     *exec.Cmd
		release io.WriteCloser
		stderr  *bytes.Buffer
	}
	children := make([]child, writers)
	for i := range children {
		cmd := exec.Command(os.Args[0], "-test.run=TestConcurrentWritersInSeparateProcesses")
		cmd.Env = append(os.Environ(), writerEnv+"=1", dbEnv+"="+path)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout: %v", err)
		}
		stderr := new(bytes.Buffer)
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		// Wait for "ready": the child has its connection open.
		line := make([]byte, len("ready\n"))
		if _, err := io.ReadFull(stdout, line); err != nil {
			t.Fatalf("child %d never reported ready: %v (%s)", i, err, stderr)
		}
		children[i] = child{cmd: cmd, release: stdin, stderr: stderr}
	}

	// Releasing alone only synchronises the start; a scheduler could still run
	// them one at a time. So the parent HOLDS the write lock while releasing
	// them and lets go only afterwards, which forces real contention for every
	// child that reaches the lock inside that window. It does not PROVE all
	// eight blocked — an unscheduled child could still begin after the
	// rollback — and the claim is limited to what it establishes. No test-only
	// method on Store is needed for it.
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer holder.Close()
	tx, err := holder.BeginTx(ctxBackground(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO migration (version, checksum, applied_at) VALUES (6000, '" + identity.HashBytes([]byte("lock")) + "', '2026-01-01T00:00:00Z')"); err != nil {
		t.Fatalf("take the lock: %v", err)
	}

	for i := range children {
		if err := children[i].release.Close(); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
	// Long enough that every released child has reached the lock and blocked.
	time.Sleep(300 * time.Millisecond)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	for i := range children {
		if err := children[i].cmd.Wait(); err != nil {
			t.Errorf("writer %d: %v (%s)", i, err, children[i].stderr)
		}
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	got, err := s.Snapshot(ctxBackground(), concurrentWrite().ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got.Documents) != 1 || len(got.Documents[0].Nodes) != 1 {
		t.Errorf("eight processes produced %d documents", len(got.Documents))
	}
}

// A writer that cannot get the lock fails with the CALLER's deadline rather
// than a driver-level busy message, because the caller's context is what bounds
// the wait. The lock is held from a raw connection rather than through a
// test-only method on Store: production code should not grow an API that exists
// only for a test.
func TestALockWaitIsBoundedByTheCallersContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	seeded, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer holder.Close()
	tx, err := holder.BeginTx(ctxBackground(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// BEGIN IMMEDIATE takes the write lock and holds it until this rolls back.
	if _, err := tx.Exec("INSERT INTO migration (version, checksum, applied_at) VALUES (5000, '" + identity.HashBytes([]byte("lock")) + "', '2026-01-01T00:00:00Z')"); err != nil {
		t.Fatalf("take the lock: %v", err)
	}
	defer tx.Rollback()

	blocked, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer blocked.Close()

	// Two materially different deadlines: a fixed driver timeout, or an
	// immediate busy error remapped to DeadlineExceeded, satisfies neither.
	for _, deadline := range []time.Duration{200 * time.Millisecond, 1200 * time.Millisecond} {
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		start := time.Now()
		err = blocked.PutSnapshot(ctx, concurrentWrite())
		waited := time.Since(start)
		cancel()

		if err == nil {
			t.Fatal("wrote while another connection held the write lock")
		}
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != context.DeadlineExceeded {
			t.Errorf("deadline %v: error = %v, ctx.Err() = %v; the caller's context must bound the wait",
				deadline, err, ctx.Err())
		}
		if waited < deadline/2 {
			t.Errorf("deadline %v: returned after %v — it did not wait for the lock", deadline, waited)
		}
		if waited > deadline*3 {
			t.Errorf("deadline %v: waited %v — the busy timeout is not the caller's", deadline, waited)
		}
	}
}

type writerFailure struct {
	err    error
	output string
}

func (w *writerFailure) Error() string { return w.err.Error() + ": " + w.output }

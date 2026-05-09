package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for the bridgeIdleLocked unsigned-subtraction underflow
// that produced GB-scale pty.hootty.log accumulation. Two angles, both
// expressing the same invariant (lastEmittedMs is monotonic, callers must
// not pass a regressed nowMs):
//
//   - TestRecorderUnderflowAfterResize sets up a violated state directly
//     (lastEmittedMs > pendingBucketMs) and asserts flushPendingLocked
//     returns promptly without flooding the file.
//   - TestRecorderResizeWriteFlushRunaway exercises the same shape via
//     the public API: RecordResize at a non-bucket-aligned ms followed by
//     a Write inside the same 33ms window, then a Write that triggers a
//     flush.
//
// Bound: flush must complete within flushBudget and the on-disk file must
// stay under fileBudget. Pre-fix, both budgets were blown by orders of
// magnitude (multi-MB and 2s+ in the original repro).

const (
	flushBudget = 200 * time.Millisecond
	fileBudget  = 4 * 1024 // generous: fits preamble + a handful of frames
)

func TestRecorderUnderflowAfterResize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	// Synthesize the post-resize state directly: lastEmittedMs=1000
	// (resize at ms=1000), pendingBucketMs=990 (Write at ms~1010,
	// bucket=990). This is the state Write would produce pre-fix; with
	// the invariant fix in Write, the public API can no longer reach
	// it, but bridgeIdleLocked's defensive guard must still neutralise
	// it if a future caller regresses.
	rec.mu.Lock()
	rec.lastEmittedMs = 1000
	rec.pendingHasBucket = true
	rec.pendingBucketMs = 990
	rec.pendingBuf = []byte("oops")
	rec.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		done <- rec.flushPendingLocked()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("flushPendingLocked: %v", err)
		}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		if fi.Size() > fileBudget {
			t.Fatalf("log size %d > budget %d — regression: spacer-loop runaway?",
				fi.Size(), fileBudget)
		}
	case <-time.After(flushBudget):
		fi, _ := os.Stat(path)
		t.Fatalf("flushPendingLocked did not return in %s; log grew to %d bytes — regression: bridgeIdleLocked underflow loop",
			flushBudget, fi.Size())
	}
}

func TestRecorderResizeWriteFlushRunaway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	// Place "start" 1000ms in the past so the FIRST RecordResize lands
	// at a non-bucket-aligned nowMs (1000 % 33 == 10; bucket is 990).
	rec.started = time.Now().Add(-1000 * time.Millisecond)
	if err := rec.RecordResize(120, 40); err != nil {
		t.Fatalf("RecordResize: %v", err)
	}

	// First Write: nowMs is in the same 33ms bucket as 1000.
	// Pre-fix this set pendingBucketMs=990 < lastEmittedMs=1000.
	// With the Write-side floor it is set to 1000.
	if _, err := rec.Write([]byte("a")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}

	// Second Write: shift nowMs forward into a different bucket so
	// flushPendingLocked is triggered. Pre-fix this entered the
	// runaway spacer loop; post-fix it must return promptly.
	rec.started = time.Now().Add(-1050 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, werr := rec.Write([]byte("b"))
		done <- werr
	}()

	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("Write 2: %v", werr)
		}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		if fi.Size() > fileBudget {
			t.Fatalf("log size %d > budget %d — regression: spacer-loop runaway?",
				fi.Size(), fileBudget)
		}
	case <-time.After(flushBudget):
		fi, _ := os.Stat(path)
		t.Fatalf("Write did not return in %s via public API; log grew to %d bytes — regression: lastEmittedMs invariant violated",
			flushBudget, fi.Size())
	}
}

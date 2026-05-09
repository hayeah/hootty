package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecorderUnderflowAfterResize provokes the bridgeIdleLocked underflow
// when pendingBucketMs < lastEmittedMs. That ordering arises in production
// whenever RecordResize lands at a non-bucket-aligned ms (call it R), then
// the next Write within the same 33ms window has bucket = (now/33)*33 < R.
// On the subsequent flush, bridgeIdleLocked(bucket) computes bucket -
// lastEmittedMs in uint64, which underflows to ~2^64 and the spacer loop
// races to fill the disk at 9 bytes per iteration.
func TestRecorderUnderflowAfterResize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	// Synthesize the post-resize state directly: lastEmittedMs=1000
	// (resize at ms=1000), pendingBucketMs=990 (Write at ms~1010,
	// bucket=990). Set under the recorder mutex to mimic production
	// flow ordering.
	rec.mu.Lock()
	rec.lastEmittedMs = 1000
	rec.pendingHasBucket = true
	rec.pendingBucketMs = 990
	rec.pendingBuf = []byte("oops")
	rec.mu.Unlock()

	t.Logf("before flush: lastEmittedMs=%d pendingBucketMs=%d", rec.lastEmittedMs, rec.pendingBucketMs)

	// flushPendingLocked is the suspect path (it calls bridgeIdleLocked
	// with bucket < lastEmittedMs). Run it under a timeout so a runaway
	// loop fails the test instead of hanging the suite.
	done := make(chan error, 1)
	go func() {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		done <- rec.flushPendingLocked()
	}()

	select {
	case err := <-done:
		fi, _ := os.Stat(path)
		t.Logf("flush returned err=%v; log size = %d bytes", err, fi.Size())
	case <-time.After(2 * time.Second):
		fi, _ := os.Stat(path)
		t.Fatalf("flushPendingLocked did not return in 2s; log grew to %d bytes (runaway spacer loop confirmed)", fi.Size())
	}
}

// TestRecorderResizeWriteFlushRunaway exercises the same bug via the
// Recorder's PUBLIC API — RecordResize at a non-aligned ms, immediately
// followed by Write within the same 33ms bucket, then a Write into a
// later bucket to force a flush.
//
// To make timing deterministic we rewind rec.started so that nowMs()
// reads as the values we want.
func TestRecorderResizeWriteFlushRunaway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.hootty.log")
	rec, err := NewRecorder(path, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	// Place "start" 1000ms in the past so the FIRST RecordResize (which
	// reads nowMs ≈ 1000) lands at a non-bucket-aligned ms.
	// 1000 % 33 = 10, so bucket of 1000 is 990.
	rec.started = time.Now().Add(-1000 * time.Millisecond)

	// Resize at nowMs ≈ 1000 sets lastEmittedMs = 1000.
	if err := rec.RecordResize(120, 40); err != nil {
		t.Fatalf("RecordResize: %v", err)
	}

	// First Write: nowMs is still in the same bucket as 1000, so
	// bucket = 990, which is < lastEmittedMs (1000). Pending state
	// is now pendingBucketMs=990, lastEmittedMs=1000.
	if _, err := rec.Write([]byte("a")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}

	// Bump start back another 50ms so the next Write lands in a
	// different bucket and triggers a flush of the (990) pending bucket.
	rec.started = time.Now().Add(-1050 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := rec.Write([]byte("b"))
		done <- err
	}()

	select {
	case err := <-done:
		fi, _ := os.Stat(path)
		t.Logf("Write returned err=%v; log size = %d bytes", err, fi.Size())
	case <-time.After(2 * time.Second):
		fi, _ := os.Stat(path)
		t.Fatalf("Write did not return in 2s via public API; log grew to %d bytes — runaway confirmed", fi.Size())
	}
}

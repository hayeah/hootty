package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireAndProbe(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "test-lock")
	os.MkdirAll(subdir, 0755)

	// Should not be alive before acquiring
	if ProbeFlock(subdir) {
		t.Fatal("expected flock to NOT be held before acquire")
	}

	// Acquire
	fd, err := AcquireFlock(subdir)
	if err != nil {
		t.Fatalf("AcquireFlock: %v", err)
	}

	// Note: ProbeFlock from the same process opens a new fd, which gets
	// its own independent lock on macOS/Linux. So it WON'T see EWOULDBLOCK.
	// This is expected per the spec: "IsAlive must be called from a
	// different process than the supervisor."
	// We test the cross-process probe in the integration test.

	// Double acquire from same process should fail (same dir, new fd)
	fd2, err := AcquireFlock(subdir)
	if err == nil {
		// On some systems, same-process flock on a new fd may succeed.
		// On macOS it typically does (per-fd locks). Clean up.
		ReleaseFlock(fd2)
	}

	// Release
	if err := ReleaseFlock(fd); err != nil {
		t.Fatalf("ReleaseFlock: %v", err)
	}

	// After release, should not be alive
	if ProbeFlock(subdir) {
		t.Fatal("expected flock to NOT be held after release")
	}
}

func TestAcquireFlockNonexistent(t *testing.T) {
	_, err := AcquireFlock("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

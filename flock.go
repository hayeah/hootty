package supervisor

import (
	"fmt"
	"os"
	"syscall"
)

// AcquireFlock opens the directory at path and acquires an exclusive
// non-blocking flock on its fd. Returns the open file (caller must keep
// it open for the lock lifetime) or an error if the lock is already held.
func AcquireFlock(path string) (*os.File, error) {
	fd, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dir for flock: %w", err)
	}
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fd.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("flock held by another process")
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return fd, nil
}

// ProbeFlock tests whether a directory flock is held by another process.
// Returns true if the lock is currently held (directory is "alive").
func ProbeFlock(path string) bool {
	fd, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fd.Close()
	err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK {
		return true // lock held → alive
	}
	if err == nil {
		// We acquired it → not held. Release immediately.
		syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	}
	return false
}

// ReleaseFlock releases the flock and closes the file descriptor.
func ReleaseFlock(fd *os.File) error {
	if fd == nil {
		return nil
	}
	syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	return fd.Close()
}

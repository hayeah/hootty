package supervisor

import (
	"io"
	"os"
	"sync"
)

// Recorder appends raw PTY bytes to a file. The point is to keep an
// authoritative byte log of everything the child wrote so a future
// `attach` CLI (or anything else) can replay the session into a
// terminal byte-for-byte.
//
// Concurrency: the supervisor's read loop is the only writer; the
// mutex is defensive against tests that might Write from multiple
// goroutines.
type Recorder struct {
	mu sync.Mutex
	w  io.WriteCloser
}

// NewRecorder opens (or creates+truncates) the file at path for the
// recording. Returns a Recorder that writes raw bytes through.
func NewRecorder(path string) (*Recorder, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &Recorder{w: f}, nil
}

// Write appends data to the recording. Returns the number of bytes
// written (matches io.Writer).
func (r *Recorder) Write(data []byte) (int, error) {
	if r == nil || r.w == nil {
		return len(data), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.w.Write(data)
}

// Close flushes and closes the underlying file. Safe to call
// multiple times.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil {
		return nil
	}
	err := r.w.Close()
	r.w = nil
	return err
}

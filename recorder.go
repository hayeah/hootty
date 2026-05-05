package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	defaultCastWidth  = 80
	defaultCastHeight = 24
)

// Recorder appends asciicast v2 JSONL events to a file. The point is
// to keep an authoritative output log of everything the child wrote
// so asciinema-compatible tools can replay the session.
//
// Concurrency: the session's read loop is the only writer; the
// mutex is defensive against tests that might write from multiple
// goroutines.
type Recorder struct {
	mu      sync.Mutex
	path    string
	w       io.WriteCloser
	started time.Time
}

// RecorderOption configures optional asciicast header metadata.
type RecorderOption func(*recorderOptions)

type recorderOptions struct {
	command string
	title   string
}

// WithRecorderCommand records the command metadata in the asciicast
// header.
func WithRecorderCommand(command string) RecorderOption {
	return func(o *recorderOptions) { o.command = command }
}

// WithRecorderTitle records the title metadata in the asciicast
// header.
func WithRecorderTitle(title string) RecorderOption {
	return func(o *recorderOptions) { o.title = title }
}

type asciicastHeader struct {
	Version   int    `json:"version"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Timestamp int64  `json:"timestamp"`
	Command   string `json:"command,omitempty"`
	Title     string `json:"title,omitempty"`
}

// NewRecorder opens (or creates+truncates) path and writes an
// asciicast v2 header line. Subsequent Write calls append output
// events.
func NewRecorder(path string, cols, rows uint16, opts ...RecorderOption) (*Recorder, error) {
	o := recorderOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if cols == 0 {
		cols = defaultCastWidth
	}
	if rows == 0 {
		rows = defaultCastHeight
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		w:       f,
		path:    path,
		started: time.Now(),
	}
	header := asciicastHeader{
		Version:   2,
		Width:     int(cols),
		Height:    int(rows),
		Timestamp: r.started.Unix(),
		Command:   o.command,
		Title:     o.title,
	}
	if err := json.NewEncoder(f).Encode(header); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

// Write appends data as an asciicast output event. Returns len(data)
// on success so Recorder still satisfies io.Writer.
func (r *Recorder) Write(data []byte) (int, error) {
	if r == nil || r.w == nil {
		return len(data), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeEventLocked("o", string(data)); err != nil {
		return 0, err
	}
	return len(data), nil
}

// RecordResize appends an asciicast resize event.
func (r *Recorder) RecordResize(cols, rows uint16) error {
	if r == nil || r.w == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeEventLocked("r", fmt.Sprintf("%dx%d", cols, rows))
}

func (r *Recorder) writeEventLocked(kind, data string) error {
	event := []any{time.Since(r.started).Seconds(), kind, data}
	return json.NewEncoder(r.w).Encode(event)
}

// Path returns the on-disk path of the recording.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
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

// AsciicastOutputEvent is one output event selected for attach
// playback.
type AsciicastOutputEvent struct {
	At   time.Duration
	Data []byte
}

// ReadAsciicastOutputEvents reads output events from path. If window
// is positive, only events from the final window duration are returned.
// Resize and input events are intentionally ignored for attach
// playback.
func ReadAsciicastOutputEvents(path string, window time.Duration) ([]AsciicastOutputEvent, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return nil, scanner.Err()
	}

	var events []AsciicastOutputEvent
	var last time.Duration
	for scanner.Scan() {
		var raw []json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			return nil, err
		}
		if len(raw) < 3 {
			continue
		}
		var seconds float64
		var kind string
		if err := json.Unmarshal(raw[0], &seconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw[1], &kind); err != nil {
			return nil, err
		}
		at := time.Duration(seconds * float64(time.Second))
		if at > last {
			last = at
		}
		if kind != "o" {
			continue
		}
		var data string
		if err := json.Unmarshal(raw[2], &data); err != nil {
			return nil, err
		}
		events = append(events, AsciicastOutputEvent{
			At:   at,
			Data: []byte(data),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if window <= 0 || len(events) == 0 {
		return events, nil
	}
	cutoff := last - window
	if cutoff <= 0 {
		return events, nil
	}
	i := 0
	for i < len(events) && events[i].At < cutoff {
		i++
	}
	return events[i:], nil
}

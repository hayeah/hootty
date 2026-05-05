package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/hayeah/hootty/internal/shortid"
)

// Store reads managed process state from a directory of directories.
type Store struct {
	Dir string // e.g. ~/.agentboss/ or .devport/
}

func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

// Load reads state.json for the given key.
func (s *Store) Load(key string) (*StateFile, error) {
	path := filepath.Join(s.Dir, key, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state %q: %w", key, err)
	}
	var state StateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state %q: %w", key, err)
	}
	return &state, nil
}

// List walks all subdirs, reads state.json from each, and returns them
// sorted by created_at.
func (s *Store) List() ([]*StateFile, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list store: %w", err)
	}
	var states []*StateFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := s.Load(entry.Name())
		if err != nil {
			continue // skip broken entries
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].Session.CreatedAt.Before(states[j].Session.CreatedAt)
	})
	return states, nil
}

// IsAlive probes the directory flock for the given key.
// Returns true if a session currently holds the lock.
func (s *Store) IsAlive(key string) bool {
	return ProbeFlock(filepath.Join(s.Dir, key))
}

// Delete removes the entire state directory for the given key.
func (s *Store) Delete(key string) error {
	return os.RemoveAll(filepath.Join(s.Dir, key))
}

// Resolve finds a state by full id or unique prefix match
// (case-insensitive). Returns shortid.IDTooShortError,
// shortid.AmbiguousIDError, or shortid.IDNotFoundError on failure;
// callers can errors.As against those to surface friendly messages.
func (s *Store) Resolve(query string) (*StateFile, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidates = append(candidates, entry.Name())
	}
	match, err := shortid.Resolve(query, candidates)
	if err != nil {
		return nil, err
	}
	return s.Load(match)
}

// Writer holds the dir flock and owns all writes to state.json.
// Only one Writer can exist per key (enforced by flock).
type Writer struct {
	dir   string
	dirFD *os.File
	mu    sync.Mutex
	state StateFile
}

// OpenWriter creates the state directory (if needed), acquires an
// exclusive dir flock, and writes the initial state.json.
func OpenWriter(dir string, initial StateFile) (*Writer, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	fd, err := AcquireFlock(dir)
	if err != nil {
		return nil, fmt.Errorf("acquire flock %q: %w", dir, err)
	}
	w := &Writer{
		dir:   dir,
		dirFD: fd,
		state: initial,
	}
	if err := w.flush(); err != nil {
		ReleaseFlock(fd)
		return nil, err
	}
	return w, nil
}

// Update mutates state under lock and rewrites state.json atomically.
func (w *Writer) Update(fn func(*StateFile)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(&w.state)
	return w.flush()
}

// UpdateState marshals the provided value into the "state" section
// of state.json. This is the method Services call (directly or
// through Session.UpdateState, which additionally publishes to
// /events subscribers).
func (w *Writer) UpdateState(state any) error {
	return w.Update(func(s *StateFile) {
		data, err := json.Marshal(state)
		if err != nil {
			return
		}
		s.State = json.RawMessage(data)
	})
}

// Snapshot returns a copy of the current state (mutex-protected).
func (w *Writer) Snapshot() StateFile {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// Close releases the flock and closes the dir fd.
func (w *Writer) Close() error {
	return ReleaseFlock(w.dirFD)
}

func (w *Writer) flush() error {
	return AtomicWriteJSON(filepath.Join(w.dir, "state.json"), &w.state)
}

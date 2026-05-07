package session

import (
	"sync"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
	"github.com/hayeah/hootty/internal/shortid"
)

// attachRegistry tracks every currently-connected attachment for one
// session and mirrors the set into state.json via writer.Update. IDs
// are minted with shortid.Generate against just this session's
// attachments[*].id (a tiny pool, so 3-char shortids essentially
// always succeed).
//
// The registry holds, per id, a closeFunc that closes the underlying
// hijacked conn from outside. `hoot detach` calls Close(id) /
// CloseAll(); the actual teardown unwinds through the per-attachment
// serveOne goroutine, which calls Remove(id) on its way out.
type attachRegistry struct {
	mu      sync.Mutex
	writer  *Writer
	members map[string]*registeredAttach
}

type registeredAttach struct {
	record AttachmentRecord
	close  func() // best-effort, idempotent
}

func newAttachRegistry(writer *Writer) *attachRegistry {
	return &attachRegistry{
		writer:  writer,
		members: make(map[string]*registeredAttach),
	}
}

// Add mints a fresh id, stores the record + closeFunc, and persists
// the new attachments slice via writer.Update. closeFunc must be
// idempotent (CloseAll vs. supervisor-shutdown can race).
func (r *attachRegistry) Add(size attachwire.PTYSize, origin attachwire.Origin, closeFunc func()) (string, error) {
	r.mu.Lock()
	existing := make(map[string]bool, len(r.members))
	for id := range r.members {
		existing[id] = true
	}
	id, err := shortid.Generate(existing)
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	rec := AttachmentRecord{
		ID:        id,
		StartedAt: time.Now().UTC(),
		Size:      size,
	}
	if !origin.IsZero() {
		o := origin
		rec.Origin = &o
	}
	r.members[id] = &registeredAttach{record: rec, close: closeFunc}
	snap := r.snapshotLocked()
	r.mu.Unlock()
	if r.writer != nil {
		_ = r.writer.Update(func(s *StateFile) {
			s.Session.Attachments = snap
		})
	}
	return id, nil
}

// Remove drops the entry by id and persists the new slice. No-op if
// id is unknown (Close + serveOne teardown both call Remove).
func (r *attachRegistry) Remove(id string) {
	r.mu.Lock()
	if _, ok := r.members[id]; !ok {
		r.mu.Unlock()
		return
	}
	delete(r.members, id)
	snap := r.snapshotLocked()
	r.mu.Unlock()
	if r.writer != nil {
		_ = r.writer.Update(func(s *StateFile) {
			s.Session.Attachments = snap
		})
	}
}

// Close issues the registered closeFunc for id. Returns false if no
// such id is registered. Does NOT remove the entry from the registry —
// the closing serveOne goroutine calls Remove on its way out.
func (r *attachRegistry) Close(id string) bool {
	r.mu.Lock()
	m, ok := r.members[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	m.close()
	return true
}

// CloseAll issues every registered closeFunc. Returns the number of
// attachments that were live at call time.
func (r *attachRegistry) CloseAll() int {
	r.mu.Lock()
	closers := make([]func(), 0, len(r.members))
	for _, m := range r.members {
		closers = append(closers, m.close)
	}
	r.mu.Unlock()
	for _, c := range closers {
		c()
	}
	return len(closers)
}

// SetEffectiveSize persists the supervisor's currently-effective PTY
// size to state.json. Called from the AttachSet sizeApplier whenever
// min-wins recompute changes the effective size.
func (r *attachRegistry) SetEffectiveSize(size attachwire.PTYSize) {
	if r.writer == nil {
		return
	}
	_ = r.writer.Update(func(s *StateFile) {
		s.Session.Size = size
	})
}

// snapshotLocked returns a deterministic copy of the attachments
// slice for state.json. Caller holds r.mu. The slice is intentionally
// returned even when empty (nil) so writer.Update clears the field
// when the last attachment leaves.
func (r *attachRegistry) snapshotLocked() []AttachmentRecord {
	if len(r.members) == 0 {
		return nil
	}
	out := make([]AttachmentRecord, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, m.record)
	}
	// Stable order: sort by StartedAt so test assertions and
	// `hoot list` output don't flicker on map-iteration order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].StartedAt.After(out[j].StartedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Snapshot returns a copy of the current attachments slice. Used by
// tests to inspect registry state without going through state.json.
func (r *attachRegistry) Snapshot() []AttachmentRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

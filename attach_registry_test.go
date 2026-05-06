package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

// TestAttachRegistryAddRemoveStateJSON exercises the registry +
// state.json roundtrip: Add appends a record (with id, size, origin)
// and persists; Remove drops it and clears the slice. The store
// reads the on-disk JSON back to verify what `hoot list` would see.
func TestAttachRegistryAddRemoveStateJSON(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "abc")
	w, err := OpenWriter(stateDir, StateFile{
		Session: SessionState{Key: "abc", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	reg := newAttachRegistry(w)
	closed := false
	id, err := reg.Add(
		attachwire.PTYSize{Cols: 80, Rows: 24},
		attachwire.Origin{Host: "laptop", Term: "xterm-256color"},
		func() { closed = true },
	)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == "" {
		t.Fatalf("Add returned empty id")
	}

	// On-disk state should now carry the attachment.
	store := NewStore(dir)
	state, err := store.Load("abc")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(state.Session.Attachments); got != 1 {
		t.Fatalf("attachments len = %d, want 1; state = %+v", got, state.Session)
	}
	rec := state.Session.Attachments[0]
	if rec.ID != id {
		t.Errorf("rec.ID = %q, want %q", rec.ID, id)
	}
	if rec.Size.Cols != 80 || rec.Size.Rows != 24 {
		t.Errorf("rec.Size = %+v, want 80x24", rec.Size)
	}
	if rec.Origin == nil || rec.Origin.Host != "laptop" {
		t.Errorf("rec.Origin = %+v, want Host=laptop", rec.Origin)
	}

	// Close + Remove should clear the slice and call closeFunc.
	if !reg.Close(id) {
		t.Fatalf("Close(%q) returned false", id)
	}
	if !closed {
		t.Errorf("closeFunc was not called")
	}
	reg.Remove(id)
	state, err = store.Load("abc")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(state.Session.Attachments); got != 0 {
		t.Errorf("after Remove: attachments len = %d, want 0", got)
	}
}

// TestAttachRegistryEmptyOriginIsNil verifies that an Origin with no
// fields populated round-trips as `omitempty`-elided JSON (the
// AttachmentRecord.Origin pointer is nil, so the marshaled record
// has no "origin" key at all).
func TestAttachRegistryEmptyOriginIsNil(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "abc")
	w, err := OpenWriter(stateDir, StateFile{
		Session: SessionState{Key: "abc", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	reg := newAttachRegistry(w)
	if _, err := reg.Add(
		attachwire.PTYSize{Cols: 80, Rows: 24},
		attachwire.Origin{},
		func() {},
	); err != nil {
		t.Fatalf("Add: %v", err)
	}

	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Origin != nil {
		t.Errorf("Origin = %+v, want nil for zero-value origin", snap[0].Origin)
	}
}

// TestAttachRegistryCloseAll closes every member and reports the
// pre-close member count.
func TestAttachRegistryCloseAll(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "abc")
	w, err := OpenWriter(stateDir, StateFile{
		Session: SessionState{Key: "abc", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	reg := newAttachRegistry(w)
	closes := 0
	for i := 0; i < 3; i++ {
		if _, err := reg.Add(
			attachwire.PTYSize{Cols: 80, Rows: 24},
			attachwire.Origin{},
			func() { closes++ },
		); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	if got := reg.CloseAll(); got != 3 {
		t.Errorf("CloseAll returned %d, want 3", got)
	}
	if closes != 3 {
		t.Errorf("closeFunc called %d times, want 3", closes)
	}
}

// TestAttachRegistryIDsAreSessionScoped confirms that two separate
// registries (i.e. two sessions) can mint the same short id without
// any cross-session coordination — uniqueness is only required
// within one session's own attachments[*].
func TestAttachRegistryIDsAreSessionScoped(t *testing.T) {
	dir := t.TempDir()
	openReg := func(key string) (*attachRegistry, *Writer) {
		w, err := OpenWriter(filepath.Join(dir, key), StateFile{
			Session: SessionState{Key: key, CreatedAt: time.Now()},
		})
		if err != nil {
			t.Fatalf("OpenWriter %s: %v", key, err)
		}
		return newAttachRegistry(w), w
	}
	a, wa := openReg("alpha")
	defer wa.Close()
	b, wb := openReg("beta")
	defer wb.Close()

	idA, err := a.Add(attachwire.PTYSize{Cols: 80, Rows: 24}, attachwire.Origin{}, func() {})
	if err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	idB, err := b.Add(attachwire.PTYSize{Cols: 80, Rows: 24}, attachwire.Origin{}, func() {})
	if err != nil {
		t.Fatalf("Add beta: %v", err)
	}
	// Distinct *registries*, so even if shortid happens to mint the
	// same string, both Adds succeed — that's the whole point of
	// session-scoped IDs.
	_ = idA
	_ = idB
}

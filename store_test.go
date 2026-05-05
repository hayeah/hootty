package session

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/shortid"
)

func TestWriterAndStore(t *testing.T) {
	dir := t.TempDir()
	stateDir := dir + "/vite"

	initial := StateFile{
		Session: SessionState{
			Key:       "vite",
			CreatedAt: time.Now(),
		},
	}

	// Open writer
	w, err := OpenWriter(stateDir, initial)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	// Store can read it
	store := NewStore(dir)
	state, err := store.Load("vite")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Session.Key != "vite" {
		t.Errorf("got key=%s, want vite", state.Session.Key)
	}

	// Update state section
	svcState := map[string]any{"state": "healthy", "port": 20042}
	if err := w.UpdateState(svcState); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	// Re-read from store
	state, _ = store.Load("vite")
	var svc map[string]any
	json.Unmarshal(state.State, &svc)
	if svc["state"] != "healthy" {
		t.Errorf("got state.state=%v, want healthy", svc["state"])
	}

	// List
	states, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(states) != 1 {
		t.Errorf("got %d states, want 1", len(states))
	}

	// Resolve by unique prefix
	resolved, err := store.Resolve("vit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Session.Key != "vite" {
		t.Errorf("got key=%s, want vite", resolved.Session.Key)
	}

	// Resolve by exact match
	resolved, err = store.Resolve("vite")
	if err != nil {
		t.Fatalf("Resolve exact: %v", err)
	}
	if resolved.Session.Key != "vite" {
		t.Errorf("exact: got key=%s, want vite", resolved.Session.Key)
	}
}

func TestStoreResolveErrors(t *testing.T) {
	dir := t.TempDir()

	// Two sessions with overlapping prefix.
	for _, k := range []string{"abc123", "abc456"} {
		w, err := OpenWriter(filepath.Join(dir, k), StateFile{
			Session: SessionState{Key: k, CreatedAt: time.Now()},
		})
		if err != nil {
			t.Fatalf("OpenWriter %s: %v", k, err)
		}
		w.Close()
	}
	store := NewStore(dir)

	if _, err := store.Resolve("ab"); err == nil {
		t.Fatal("expected error for too-short query")
	} else {
		var tooShort *shortid.IDTooShortError
		if !errors.As(err, &tooShort) {
			t.Fatalf("expected IDTooShortError, got %T: %v", err, err)
		}
	}

	if _, err := store.Resolve("abc"); err == nil {
		t.Fatal("expected ambiguous error")
	} else {
		var ambig *shortid.AmbiguousIDError
		if !errors.As(err, &ambig) {
			t.Fatalf("expected AmbiguousIDError, got %T: %v", err, err)
		}
		if len(ambig.Matches) != 2 {
			t.Errorf("got %d matches, want 2", len(ambig.Matches))
		}
	}

	if _, err := store.Resolve("zzz"); err == nil {
		t.Fatal("expected not-found error")
	} else {
		var nf *shortid.IDNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("expected IDNotFoundError, got %T: %v", err, err)
		}
	}
}

func TestWriterSnapshot(t *testing.T) {
	dir := t.TempDir()
	stateDir := dir + "/test"

	initial := StateFile{
		Session: SessionState{Key: "test"},
	}

	w, err := OpenWriter(stateDir, initial)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	snap := w.Snapshot()
	if snap.Session.Key != "test" {
		t.Errorf("got key=%s, want test", snap.Session.Key)
	}
}

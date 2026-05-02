package supervisor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWriterAndStore(t *testing.T) {
	dir := t.TempDir()
	stateDir := dir + "/vite"

	initial := StateFile{
		Supervisor: SupervisorState{
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
	if state.Supervisor.Key != "vite" {
		t.Errorf("got key=%s, want vite", state.Supervisor.Key)
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

	// Resolve
	resolved, err := store.Resolve("vi")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Supervisor.Key != "vite" {
		t.Errorf("got key=%s, want vite", resolved.Supervisor.Key)
	}
}

func TestWriterSnapshot(t *testing.T) {
	dir := t.TempDir()
	stateDir := dir + "/test"

	initial := StateFile{
		Supervisor: SupervisorState{Key: "test"},
	}

	w, err := OpenWriter(stateDir, initial)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	snap := w.Snapshot()
	if snap.Supervisor.Key != "test" {
		t.Errorf("got key=%s, want test", snap.Supervisor.Key)
	}
}

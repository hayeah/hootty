package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	data := map[string]any{
		"key":   "vite",
		"port":  20042,
		"alive": true,
	}

	if err := AtomicWriteJSON(path, data); err != nil {
		t.Fatalf("AtomicWriteJSON: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if result["key"] != "vite" {
		t.Errorf("got key=%v, want vite", result["key"])
	}
	if result["port"] != float64(20042) {
		t.Errorf("got port=%v, want 20042", result["port"])
	}
}

func TestAtomicWriteJSONOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Write first version
	AtomicWriteJSON(path, map[string]string{"v": "1"})

	// Overwrite
	AtomicWriteJSON(path, map[string]string{"v": "2"})

	content, _ := os.ReadFile(path)
	var result map[string]string
	json.Unmarshal(content, &result)

	if result["v"] != "2" {
		t.Errorf("got v=%s, want 2", result["v"])
	}

	// No leftover tmp files
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.json" {
			t.Errorf("leftover file: %s", e.Name())
		}
	}
}

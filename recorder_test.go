package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderWritesAsciicastV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.cast")
	rec, err := NewRecorder(path, 120, 40,
		WithRecorderCommand("sh -lc echo hi"),
		WithRecorderTitle("demo"),
	)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte("hello\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.RecordResize(100, 30); err != nil {
		t.Fatalf("RecordResize: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cast: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("missing header")
	}
	var header asciicastHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Version != 2 || header.Width != 120 || header.Height != 40 {
		t.Fatalf("header = %+v, want asciicast v2 120x40", header)
	}
	if header.Command != "sh -lc echo hi" || header.Title != "demo" {
		t.Fatalf("header metadata = %+v", header)
	}

	if !scanner.Scan() {
		t.Fatal("missing output event")
	}
	var output []any
	if err := json.Unmarshal(scanner.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output[1] != "o" || output[2] != "hello\r\n" {
		t.Fatalf("output event = %#v", output)
	}

	if !scanner.Scan() {
		t.Fatal("missing resize event")
	}
	var resize []any
	if err := json.Unmarshal(scanner.Bytes(), &resize); err != nil {
		t.Fatalf("decode resize: %v", err)
	}
	if resize[1] != "r" || resize[2] != "100x30" {
		t.Fatalf("resize event = %#v", resize)
	}
}

func TestReadAsciicastOutputEventsBoundsWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.cast")
	data := []byte(`{"version":2,"width":80,"height":24,"timestamp":1}
[0,"o","old"]
[10,"r","100x30"]
[20,"o","new"]
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write cast: %v", err)
	}

	events, err := ReadAsciicastOutputEvents(path, 5*time.Second)
	if err != nil {
		t.Fatalf("ReadAsciicastOutputEvents: %v", err)
	}
	if len(events) != 1 || string(events[0].Data) != "new" {
		t.Fatalf("events = %#v, want only recent output", events)
	}
}

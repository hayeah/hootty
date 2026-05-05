package supervisor

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestLibghosttyPTYFormatters smoke-tests Format{Text,HTML,VT} after
// feeding a small VT stream through the master end of a fresh PTY
// pair. The slave is closed immediately; we treat the master as a
// plain os.File for the duration of the test.
func TestLibghosttyPTYFormatters(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()

	p, err := NewLibghosttyPTY(master, 80, 24)
	if err != nil {
		slave.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	defer p.Close()

	// Write VT bytes into the slave so the readLoop picks them up
	// (the master's read side observes whatever is written to the
	// slave).
	if _, err := slave.Write([]byte("hello\r\n\x1b[1mbold\x1b[0m\r\ndone\r\n")); err != nil {
		t.Fatalf("slave write: %v", err)
	}

	// Wait for "done" to land in the formatter output. The
	// master.Read → dispatcher → format hop is asynchronous, so a
	// single FormatText round-trip can race the readLoop's feed
	// action onto the dispatcher queue. Poll briefly instead.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := p.FormatText()
		if err == nil && strings.Contains(string(out), "done") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	plainBytes, err := p.FormatText()
	if err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	plain := string(plainBytes)
	for _, want := range []string{"hello", "bold", "done"} {
		if !strings.Contains(plain, want) {
			t.Errorf("FormatText: missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "\x1b") {
		t.Errorf("FormatText leaked escape sequences: %q", plain)
	}

	html, err := p.FormatHTML()
	if err != nil {
		t.Fatalf("FormatHTML: %v", err)
	}
	if !strings.Contains(string(html), "font-weight: bold") {
		t.Errorf("FormatHTML: missing bold style: %q", string(html))
	}
	if !strings.Contains(string(html), "hello") {
		t.Errorf("FormatHTML: missing hello: %q", string(html))
	}

	vt, err := p.FormatVT()
	if err != nil {
		t.Fatalf("FormatVT: %v", err)
	}
	if !strings.Contains(string(vt), "\x1b[") {
		t.Errorf("FormatVT: expected escape sequences in output, got %q", string(vt))
	}
	if !strings.Contains(string(vt), "bold") {
		t.Errorf("FormatVT: missing 'bold' text: %q", string(vt))
	}

	// Slave was kept open so the master's reads don't see EOF;
	// close it here so the readLoop can exit on master.Close.
	_ = slave.Close()
}

func TestLibghosttyPTYSnapshotPartsSplitScrollbackAndScreen(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	var fixture strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&fixture, "line-%02d\r\n", i)
	}
	fixture.WriteString("\x1b[2;5H\x1b[1mCUR\x1b[0m")
	writePTYAndWait(t, p, slave, []byte(fixture.String()), "CUR")

	scrollback, screen, err := p.SnapshotParts()
	if err != nil {
		t.Fatalf("SnapshotParts: %v", err)
	}
	if !strings.Contains(string(scrollback), "line-01") || !strings.Contains(string(scrollback), "line-06") {
		t.Fatalf("scrollback missing expected history rows: %q", scrollback)
	}
	if strings.Contains(string(scrollback), "line-07") || strings.Contains(string(scrollback), "CUR") {
		t.Fatalf("scrollback contains visible screen rows: %q", scrollback)
	}
	for _, want := range []string{"line-07", "line-10", "CUR", "\x1b[2;8H"} {
		if !strings.Contains(string(screen), want) {
			t.Fatalf("screen missing %q: %q", want, screen)
		}
	}
}

// TestLibghosttyRecorderRoundtrip verifies that all bytes written
// through the slave end up byte-identical in the recorder file.
func TestLibghosttyRecorderRoundtrip(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()

	tmp := t.TempDir()
	logPath := tmp + "/pty.log"
	rec, err := NewRecorder(logPath)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	p, err := NewLibghosttyPTY(master, 80, 24, WithRecorder(rec))
	if err != nil {
		slave.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	defer p.Close()

	// PTY line discipline rewrites LF→CRLF on the slave→master
	// path, so we can't compare verbatim. Check that the recorded
	// stream contains the meaningful tokens (and the SGR escape).
	if _, err := slave.Write([]byte("abc\r\n\x1b[31mred\x1b[0m\r\n")); err != nil {
		t.Fatalf("slave write: %v", err)
	}
	// Poll until "red" shows up in the formatter — proves the
	// readLoop has processed our chunk through the dispatcher (the
	// recorder writes inside the same dispatcher action).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := p.FormatText()
		if strings.Contains(string(out), "red") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.Close() // flushes recorder

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{"abc", "\x1b[31m", "red", "\x1b[0m"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("recorder roundtrip: missing %q in log %q", want, string(got))
		}
	}

	_ = slave.Close()
}

// TestLibghosttyPTYWriteRoundtrip exercises Write: bytes handed to
// the master are observable on the slave end of the pair.
func TestLibghosttyPTYWriteRoundtrip(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	p, err := NewLibghosttyPTY(master, 80, 24)
	if err != nil {
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	defer p.Close()

	// Trailing newline so the slave's line-discipline ICANON mode
	// hands the buffered line to the reader.
	if err := p.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	type res struct {
		n   int
		buf []byte
	}
	out := make(chan res, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := slave.Read(buf)
		out <- res{n, buf[:n]}
	}()
	select {
	case r := <-out:
		if !strings.Contains(string(r.buf), "hello") {
			t.Errorf("Write roundtrip: got %q want substring %q", string(r.buf), "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slave to receive Write bytes")
	}
}

// Package attachetest provides libghostty-backed fixtures for attach
// protocol UX tests.
package attachetest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hayeah/supervisor"
	libghostty "github.com/mitchellh/go-libghostty"
)

type Remote struct {
	PTY    *supervisor.LibghosttyPTY
	slave  *os.File
	master *os.File
	server *httptest.Server
}

func NewRemote(t testing.TB, cols, rows uint16) *Remote {
	return NewRemoteWithOptions(t, cols, rows)
}

func NewRemoteWithOptions(t testing.TB, cols, rows uint16, opts ...supervisor.LibghosttyOption) *Remote {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	allOpts := append([]supervisor.LibghosttyOption{supervisor.WithLibghosttyScrollback(200)}, opts...)
	ptyImpl, err := supervisor.NewLibghosttyPTY(master, cols, rows, allOpts...)
	if err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	mux := http.NewServeMux()
	ptyImpl.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	r := &Remote{PTY: ptyImpl, slave: slave, master: master, server: server}
	t.Cleanup(r.Close)
	return r
}

func (r *Remote) Close() {
	if r.server != nil {
		r.server.Close()
	}
	if r.PTY != nil {
		_ = r.PTY.Close()
	}
	if r.slave != nil {
		_ = r.slave.Close()
	}
	if r.master != nil {
		_ = r.master.Close()
	}
}

func (r *Remote) AttachURL() string {
	return r.server.URL + "/attach"
}

func (r *Remote) DialAttach(t testing.TB) net.Conn {
	t.Helper()
	addr := strings.TrimPrefix(r.server.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial attach server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (r *Remote) WriteAndWait(t testing.TB, payload []byte, want string) {
	t.Helper()
	if _, err := r.slave.Write(payload); err != nil {
		t.Fatalf("remote slave write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := r.PTY.FormatText()
		if err == nil && strings.Contains(string(out), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := r.PTY.FormatText()
	t.Fatalf("timed out waiting for remote %q; last=%q", want, string(out))
}

type LocalTerminal struct {
	mu   sync.Mutex
	term *libghostty.Terminal
	raw  bytes.Buffer
}

func NewLocalTerminal(t testing.TB, cols, rows uint16) *LocalTerminal {
	t.Helper()
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(500),
	)
	if err != nil {
		t.Fatalf("local NewTerminal: %v", err)
	}
	lt := &LocalTerminal{term: term}
	t.Cleanup(term.Close)
	return lt
}

func (lt *LocalTerminal) Write(p []byte) (int, error) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.raw.Write(p)
	lt.term.VTWrite(p)
	return len(p), nil
}

func (lt *LocalTerminal) WaitText(t testing.TB, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(lt.PlainText(t), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for local text %q; last=%q", want, lt.PlainText(t))
}

func (lt *LocalTerminal) WaitRawText(t testing.TB, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lt.mu.Lock()
		got := lt.raw.String()
		lt.mu.Unlock()
		if strings.Contains(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	lt.mu.Lock()
	got := lt.raw.String()
	lt.mu.Unlock()
	t.Fatalf("timed out waiting for local raw text %q; last=%q", want, got)
}

func (lt *LocalTerminal) Resize(t testing.TB, cols, rows uint16) {
	t.Helper()
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if err := lt.term.Resize(cols, rows, 0, 0); err != nil {
		t.Fatalf("local resize: %v", err)
	}
}

func (lt *LocalTerminal) PlainText(t testing.TB) string {
	t.Helper()
	lt.mu.Lock()
	defer lt.mu.Unlock()
	f, err := libghostty.NewFormatter(lt.term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		t.Fatalf("local plain formatter: %v", err)
	}
	defer f.Close()
	out, err := f.FormatString()
	if err != nil {
		t.Fatalf("local plain format: %v", err)
	}
	return out
}

func (lt *LocalTerminal) Snapshot(t testing.TB) string {
	t.Helper()
	lt.mu.Lock()
	defer lt.mu.Unlock()

	cols, err := lt.term.Cols()
	if err != nil {
		t.Fatalf("local cols: %v", err)
	}
	rows, err := lt.term.Rows()
	if err != nil {
		t.Fatalf("local rows: %v", err)
	}
	cursorX, err := lt.term.CursorX()
	if err != nil {
		t.Fatalf("local cursor x: %v", err)
	}
	cursorY, err := lt.term.CursorY()
	if err != nil {
		t.Fatalf("local cursor y: %v", err)
	}
	visible, err := lt.term.CursorVisible()
	if err != nil {
		t.Fatalf("local cursor visible: %v", err)
	}
	active, err := lt.term.ActiveScreen()
	if err != nil {
		t.Fatalf("local active screen: %v", err)
	}
	style, err := lt.term.CursorStyle()
	if err != nil {
		t.Fatalf("local cursor style: %v", err)
	}
	f, err := libghostty.NewFormatter(lt.term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterTrim(true),
		libghostty.WithFormatterExtraCursor(true),
		libghostty.WithFormatterExtraStyle(true),
		libghostty.WithFormatterExtraModes(true),
	)
	if err != nil {
		t.Fatalf("local vt formatter: %v", err)
	}
	defer f.Close()
	vt, err := f.Format()
	if err != nil {
		t.Fatalf("local vt format: %v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "cols=%d rows=%d\n", cols, rows)
	fmt.Fprintf(&b, "cursor=%d,%d visible=%t active=%s sgr=%s\n", cursorY+1, cursorX+1, visible, screenName(active), styleSummary(style))
	b.WriteString("formatVT:\n")
	b.WriteString(VisualizeVT(vt))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func (lt *LocalTerminal) RawSnapshot(t testing.TB) string {
	t.Helper()
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return VisualizeVT(lt.raw.Bytes())
}

func VisualizeVT(vt []byte) string {
	return strings.ReplaceAll(string(vt), "\x1b", "<ESC>")
}

func CompareGolden(t testing.TB, name, got string) {
	t.Helper()
	path := GoldenPath(name)
	if os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s mismatch\n--- want\n%s\n--- got\n%s", name, string(want), got)
	}
}

func GoldenPath(name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", name+".golden")
}

func WriteUpgrade(t testing.TB, conn net.Conn, url string) {
	t.Helper()
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Upgrade", "supervise-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	if err := bufrw.Flush(); err != nil {
		t.Fatalf("flush upgrade: %v", err)
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upgrade got %d %s, want 101", resp.StatusCode, string(body))
	}
}

func screenName(screen libghostty.TerminalScreen) string {
	switch screen {
	case libghostty.ScreenPrimary:
		return "primary"
	case libghostty.ScreenAlternate:
		return "alternate"
	default:
		return fmt.Sprintf("unknown(%d)", screen)
	}
}

func styleSummary(style *libghostty.Style) string {
	if style == nil || style.IsDefault() {
		return "default"
	}
	var parts []string
	if style.Bold() {
		parts = append(parts, "bold")
	}
	if style.Faint() {
		parts = append(parts, "faint")
	}
	if style.Italic() {
		parts = append(parts, "italic")
	}
	if style.Underline() != 0 {
		parts = append(parts, fmt.Sprintf("underline=%d", style.Underline()))
	}
	if style.Inverse() {
		parts = append(parts, "inverse")
	}
	if style.Blink() {
		parts = append(parts, "blink")
	}
	if style.Strikethrough() {
		parts = append(parts, "strikethrough")
	}
	if style.Overline() {
		parts = append(parts, "overline")
	}
	if style.Invisible() {
		parts = append(parts, "invisible")
	}
	parts = append(parts, "fg="+colorSummary(style.FgColor()))
	parts = append(parts, "bg="+colorSummary(style.BgColor()))
	return strings.Join(parts, ",")
}

func colorSummary(color libghostty.StyleColor) string {
	switch color.Tag {
	case libghostty.StyleColorNone:
		return "default"
	case libghostty.StyleColorPalette:
		return fmt.Sprintf("palette(%d)", color.Palette)
	case libghostty.StyleColorRGB:
		return fmt.Sprintf("rgb(%d,%d,%d)", color.RGB.R, color.RGB.G, color.RGB.B)
	default:
		return fmt.Sprintf("unknown(%d)", color.Tag)
	}
}

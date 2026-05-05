package session

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	libghostty "github.com/mitchellh/go-libghostty"
)

func TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 40, 5)
	defer cleanup()

	writePTYAndWait(t, p, slave, altScreenFixture(), "ALT-PANEL")

	snap, err := p.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	primaryIdx := bytes.Index(snap, []byte("primary-01"))
	if primaryIdx < 0 {
		t.Fatalf("snapshot missing primary scrollback: %q", snap)
	}
	altIdx := bytes.Index(snap, []byte("ALT-PANEL"))
	if altIdx < 0 {
		t.Fatalf("snapshot missing alt screen: %q", snap)
	}
	if primaryIdx > altIdx {
		t.Fatalf("primary appears after alt in snapshot: primary=%d alt=%d snapshot=%q", primaryIdx, altIdx, snap)
	}

	client := newReplayTerminal(t, 40, 5)
	defer client.Close()
	client.VTWrite(snap)
	active, err := client.ActiveScreen()
	if err != nil {
		t.Fatalf("client ActiveScreen after snapshot: %v", err)
	}
	if active != libghostty.ScreenAlternate {
		t.Fatalf("client active screen after snapshot = %v, want alternate", active)
	}
	if got := formatTestTerminalText(t, client); !strings.Contains(got, "ALT-PANEL") {
		t.Fatalf("client alt screen missing ALT-PANEL after snapshot: %q", got)
	}

	client.VTWrite([]byte("\x1b[?1049l"))
	active, err = client.ActiveScreen()
	if err != nil {
		t.Fatalf("client ActiveScreen after alt exit: %v", err)
	}
	if active != libghostty.ScreenPrimary {
		t.Fatalf("client active screen after alt exit = %v, want primary", active)
	}
	primary := formatTestTerminalText(t, client)
	if !strings.Contains(primary, "primary-01") || !strings.Contains(primary, "primary-12") {
		t.Fatalf("client primary missing scrollback after alt exit: %q", primary)
	}
	if strings.Contains(primary, "ALT-PANEL") {
		t.Fatalf("client primary still contains alt screen after alt exit: %q", primary)
	}
}

func TestLibghosttyPTYSubscribeWithSnapshot_LiveAltExitRestoresPrimary(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 40, 5)
	defer cleanup()

	writePTYAndWait(t, p, slave, altScreenFixture(), "ALT-PANEL")

	liveCh, snap, cancel, err := p.SubscribeWithSnapshot()
	if err != nil {
		t.Fatalf("SubscribeWithSnapshot: %v", err)
	}
	defer cancel()

	client := newReplayTerminal(t, 40, 5)
	defer client.Close()
	client.VTWrite(snap)

	if _, err := slave.Write([]byte("\x1b[?1049lprimary-after\r\n")); err != nil {
		t.Fatalf("slave write alt exit: %v", err)
	}
	live := readLiveUntil(t, liveCh, "primary-after")
	if !bytes.Contains(live, []byte("\x1b[?1049l")) {
		t.Fatalf("live stream did not include alt-screen exit: %q", live)
	}
	client.VTWrite(live)

	active, err := client.ActiveScreen()
	if err != nil {
		t.Fatalf("client ActiveScreen after live exit: %v", err)
	}
	if active != libghostty.ScreenPrimary {
		t.Fatalf("client active screen after live exit = %v, want primary", active)
	}
	primary := formatTestTerminalText(t, client)
	for _, want := range []string{"primary-01", "primary-10", "primary-after"} {
		if !strings.Contains(primary, want) {
			t.Fatalf("client primary missing %q after live exit: %q", want, primary)
		}
	}
}

func altScreenFixture() []byte {
	var b bytes.Buffer
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "primary-%02d\r\n", i)
	}
	b.WriteString("\x1b[?1049h")
	b.WriteString("\x1b[2J\x1b[H")
	b.WriteString("ALT-PANEL\r\nALT-ROW\r\n")
	return b.Bytes()
}

func newTestLibghosttyPTY(t *testing.T, cols, rows uint16) (*LibghosttyPTY, *os.File, func()) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	p, err := NewLibghosttyPTY(master, cols, rows, WithLibghosttyScrollback(100))
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	cleanup := func() {
		_ = p.Close()
		_ = slave.Close()
		_ = master.Close()
	}
	return p, slave, cleanup
}

func writePTYAndWait(t *testing.T, p *LibghosttyPTY, slave *os.File, payload []byte, want string) {
	t.Helper()
	if _, err := slave.Write(payload); err != nil {
		t.Fatalf("slave write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := p.FormatText()
		if err == nil && strings.Contains(string(out), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := p.FormatText()
	t.Fatalf("timed out waiting for %q in active screen; last=%q", want, string(out))
}

func newReplayTerminal(t *testing.T, cols, rows uint16) *libghostty.Terminal {
	t.Helper()
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(100),
	)
	if err != nil {
		t.Fatalf("NewTerminal replay: %v", err)
	}
	return term
}

func formatTestTerminalText(t *testing.T, term *libghostty.Terminal) string {
	t.Helper()
	f, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		t.Fatalf("NewFormatter text: %v", err)
	}
	defer f.Close()
	out, err := f.Format()
	if err != nil {
		t.Fatalf("Format text: %v", err)
	}
	return string(out)
}

func readLiveUntil(t *testing.T, ch <-chan []byte, want string) []byte {
	t.Helper()
	var got bytes.Buffer
	deadline := time.After(2 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				t.Fatalf("live channel closed before %q; got %q", want, got.String())
			}
			got.Write(chunk)
			if strings.Contains(got.String(), want) {
				return got.Bytes()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live %q; got %q", want, got.String())
		}
	}
}

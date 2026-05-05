package supervisor

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestSubscriberFanoutStripsQueries reproduces the "junk chars on
// detach" symptom at the LibghosttyPTY level: write a chunk that
// contains a real-world tmux startup probe burst into the PTY, and
// assert that:
//
//   - the subscriber channel does NOT see any of the query bytes
//   - the emulator (formatter) still saw the printable text in full
//
// This proves the recorder + emulator path got raw bytes (so the
// libghostty auto-reply path still fires and the emulator is in
// sync) while the user-facing fanout was scrubbed.
func TestSubscriberFanoutStripsQueries(t *testing.T) {
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

	// Subscribe BEFORE writing so we observe everything.
	ch, cancel := p.SubscribeAtRecord()
	defer cancel()

	// Realistic-ish burst:
	//   "hello\r\n"                printable
	//   ESC[c                       DA1
	//   ESC[>c                      DA2
	//   ESC[>q                      XTVERSION
	//   ESC]11;?BEL                 OSC 11 query
	//   ESC[6n                      CPR
	//   "world\r\n"                 printable
	//   ESC[?2026$p                 DECRQM
	//   "done\r\n"                  printable
	burst := "hello\r\n" +
		"\x1b[c" +
		"\x1b[>c" +
		"\x1b[>q" +
		"\x1b]11;?\x07" +
		"\x1b[6n" +
		"world\r\n" +
		"\x1b[?2026$p" +
		"done\r\n"

	if _, err := slave.Write([]byte(burst)); err != nil {
		t.Fatalf("slave write: %v", err)
	}

	// Drain the subscriber channel for up to 1s; stop early once we
	// see "done\r\n".
	var got bytes.Buffer
	deadline := time.After(1 * time.Second)
loop:
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				break loop
			}
			got.Write(chunk)
			if bytes.Contains(got.Bytes(), []byte("done")) {
				// give one tiny grace window for any trailing chunk
				time.Sleep(50 * time.Millisecond)
				select {
				case more := <-ch:
					got.Write(more)
				default:
				}
				break loop
			}
		case <-deadline:
			break loop
		}
	}

	gs := got.String()

	// All printable text must have made it through.
	for _, want := range []string{"hello", "world", "done"} {
		if !strings.Contains(gs, want) {
			t.Errorf("subscriber missing %q in %q", want, gs)
		}
	}

	// None of the query bytes should appear in the subscriber stream.
	for _, banned := range []string{
		"\x1b[c",
		"\x1b[>c",
		"\x1b[>q",
		"\x1b]11;?",
		"\x1b[6n",
		"\x1b[?2026$p",
	} {
		if strings.Contains(gs, banned) {
			t.Errorf("subscriber leaked query %q in %q", banned, gs)
		}
	}

	// The emulator path is unfiltered — its plain dump should contain
	// all three printable lines.
	deadlinePoll := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlinePoll) {
		out, err := p.FormatText()
		if err == nil && strings.Contains(string(out), "done") {
			plain := string(out)
			for _, want := range []string{"hello", "world", "done"} {
				if !strings.Contains(plain, want) {
					t.Errorf("emulator missing %q in %q", want, plain)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("emulator never saw 'done'")
}

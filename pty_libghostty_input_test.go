package session

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/term"
)

// makeSlaveRaw puts the PTY slave into raw mode so that slave.Read
// returns bytes immediately as they arrive (no canonical line
// buffering, no ICRNL / OPOST mangling). The original termios is
// restored on cleanup.
func makeSlaveRaw(t *testing.T, slave *os.File) {
	t.Helper()
	old, err := term.MakeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	t.Cleanup(func() { _ = term.Restore(int(slave.Fd()), old) })
}

// TestBracketedPasteActive_TogglesWithDECSet drives DECSET 2004 from
// the slave side of the PTY and asserts that BracketedPasteActive
// flips correctly.
func TestBracketedPasteActive_TogglesWithDECSet(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	if p.BracketedPasteActive() {
		t.Fatalf("initial state: expected bracketed paste off, got on")
	}

	// Enable DECSET 2004 on the inner terminal by writing the
	// sequence into the slave (the PTY's readLoop will feed it to
	// libghostty). Poll briefly because the dispatcher feeds async.
	if _, err := slave.Write([]byte("\x1b[?2004h")); err != nil {
		t.Fatalf("slave write enable: %v", err)
	}
	if !waitForBracketedPaste(p, true, 2*time.Second) {
		t.Fatalf("timed out waiting for bracketed paste to turn on")
	}

	if _, err := slave.Write([]byte("\x1b[?2004l")); err != nil {
		t.Fatalf("slave write disable: %v", err)
	}
	if !waitForBracketedPaste(p, false, 2*time.Second) {
		t.Fatalf("timed out waiting for bracketed paste to turn off")
	}
}

func waitForBracketedPaste(p *LibghosttyPTY, want bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.BracketedPasteActive() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestHandleInput_PasteOff_RawPassthrough confirms the default path
// (no paste param) writes the body verbatim.
func TestHandleInput_PasteOff_RawPassthrough(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()
	makeSlaveRaw(t, slave)

	srv := httptest.NewServer(http.HandlerFunc(p.handleInput))
	defer srv.Close()

	resp := postBody(t, srv.URL, "POST", strings.NewReader("hello"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d want 204", resp.StatusCode)
	}
	got := readMaster(t, slave, 5*time.Second, 5)
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("master read: got %q want %q", got, "hello")
	}
}

// TestHandleInput_PasteOn_NoMode_Returns409 confirms paste=on with
// the receiver not in DECSET 2004 mode returns 409 and writes nothing.
func TestHandleInput_PasteOn_NoMode_Returns409(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(p.handleInput))
	defer srv.Close()

	resp := postBody(t, srv.URL+"?paste=on", "POST", strings.NewReader("hello"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status: got %d want 409", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "bracketed paste not enabled") {
		t.Fatalf("body: missing error message, got %q", body)
	}

	// Confirm nothing was written to the master.
	if buf := readMasterMaybe(slave, 100*time.Millisecond, 16); len(buf) != 0 {
		t.Fatalf("master: expected no write, got %q", buf)
	}
}

// TestHandleInput_PasteOn_ModeOn_WrapsBody confirms the happy path
// wraps body in 200~ … 201~.
func TestHandleInput_PasteOn_ModeOn_WrapsBody(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	if _, err := slave.Write([]byte("\x1b[?2004h")); err != nil {
		t.Fatalf("enable 2004: %v", err)
	}
	if !waitForBracketedPaste(p, true, 2*time.Second) {
		t.Fatalf("waiting for 2004 on")
	}
	makeSlaveRaw(t, slave)

	srv := httptest.NewServer(http.HandlerFunc(p.handleInput))
	defer srv.Close()

	resp := postBody(t, srv.URL+"?paste=on", "POST", strings.NewReader("abc"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d want 204", resp.StatusCode)
	}

	want := append(append([]byte{}, pasteStart...), append([]byte("abc"), pasteEnd...)...)
	got := readMaster(t, slave, 5*time.Second, len(want))
	if !bytes.Equal(got, want) {
		t.Fatalf("master read: got %q want %q", got, want)
	}
}

// TestHandleInput_PasteOn_RejectsEnd confirms that if the body
// contains \e[201~ we 400 before any write.
func TestHandleInput_PasteOn_RejectsEnd(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	if _, err := slave.Write([]byte("\x1b[?2004h")); err != nil {
		t.Fatalf("enable 2004: %v", err)
	}
	if !waitForBracketedPaste(p, true, 2*time.Second) {
		t.Fatalf("waiting for 2004 on")
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handleInput))
	defer srv.Close()

	body := append([]byte("trying "), pasteEnd...)
	resp := postBody(t, srv.URL+"?paste=on", "POST", bytes.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	if buf := readMasterMaybe(slave, 100*time.Millisecond, 16); len(buf) != 0 {
		t.Fatalf("master: expected no write, got %q", buf)
	}
}

// TestHandleInput_BodyTooLarge confirms we reject >1 MiB before any
// write.
func TestHandleInput_BodyTooLarge(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(p.handleInput))
	defer srv.Close()

	big := bytes.Repeat([]byte{'a'}, inputBodyCap+10)
	resp := postBody(t, srv.URL, "POST", bytes.NewReader(big))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d want 413", resp.StatusCode)
	}
	if buf := readMasterMaybe(slave, 100*time.Millisecond, 16); len(buf) != 0 {
		t.Fatalf("master: expected no write, got %q", buf)
	}
}

// TestWriteAtomicityUnderConcurrentWriters fires N concurrent Write
// calls with distinct ~8 KiB payloads and asserts that each payload
// appears contiguously in the master read stream — none interleave.
// This is the proof writeMu does its job.
func TestWriteAtomicityUnderConcurrentWriters(t *testing.T) {
	p, slave, cleanup := newTestLibghosttyPTY(t, 20, 5)
	defer cleanup()
	makeSlaveRaw(t, slave)

	const writers = 8
	const chunkSize = 8 * 1024
	const total = writers * chunkSize

	payloads := make([][]byte, writers)
	for i := range payloads {
		buf := make([]byte, chunkSize)
		marker := byte('A' + i)
		for j := range buf {
			buf[j] = marker
		}
		payloads[i] = buf
	}

	// Reader goroutine drains the slave concurrently with writers
	// so the master.Write calls don't wedge on a full PTY buffer.
	type readResult struct {
		buf []byte
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 0, total)
		tmp := make([]byte, 4096)
		for len(buf) < total {
			n, err := slave.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				readCh <- readResult{buf, err}
				return
			}
		}
		readCh <- readResult{buf, nil}
	}()

	var wg sync.WaitGroup
	wg.Add(writers)
	for _, payload := range payloads {
		go func() {
			defer wg.Done()
			if err := p.Write(payload); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	wg.Wait()

	var got []byte
	select {
	case r := <-readCh:
		if r.err != nil {
			t.Fatalf("read: %v (got %d/%d bytes)", r.err, len(r.buf), total)
		}
		got = r.buf
	case <-time.After(10 * time.Second):
		t.Fatalf("read: timed out waiting for %d bytes", total)
	}

	// Walk the stream chunk-by-chunk: each contiguous run of one
	// byte must be exactly chunkSize. Interleaving would produce
	// shorter runs.
	i := 0
	for i < len(got) {
		end := i
		for end < len(got) && got[end] == got[i] {
			end++
		}
		if end-i != chunkSize {
			t.Fatalf("interleaved write detected: run of %q at offset %d has length %d, want %d", string(got[i]), i, end-i, chunkSize)
		}
		i = end
	}
}

func postBody(t *testing.T, url, method string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// readMaster reads exactly want bytes from the slave's master end
// (slave.Read returns whatever the master wrote). Times out as
// configured.
func readMaster(t *testing.T, slave io.Reader, timeout time.Duration, want int) []byte {
	t.Helper()
	type result struct {
		buf []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 0, want)
		tmp := make([]byte, want)
		for len(buf) < want {
			n, err := slave.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				ch <- result{buf, err}
				return
			}
			if len(buf) >= want {
				break
			}
		}
		ch <- result{buf, nil}
	}()
	select {
	case r := <-ch:
		if r.err != nil && len(r.buf) < want {
			t.Fatalf("read master: %v (got %d/%d bytes)", r.err, len(r.buf), want)
		}
		return r.buf
	case <-time.After(timeout):
		t.Fatalf("read master: timed out waiting for %d bytes", want)
		return nil
	}
}

// readMasterMaybe reads up to limit bytes within timeout and returns
// what came in; never fails.
func readMasterMaybe(slave io.Reader, timeout time.Duration, limit int) []byte {
	type result struct{ buf []byte }
	ch := make(chan result, 1)
	go func() {
		tmp := make([]byte, limit)
		n, _ := slave.Read(tmp)
		ch <- result{tmp[:n]}
	}()
	select {
	case r := <-ch:
		return r.buf
	case <-time.After(timeout):
		return nil
	}
}

package session

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

// dialAttachAndHello opens an attach connection on rpc.sock, runs
// the hoot-attach/1 upgrade, and sends a Hello frame. Returns the
// raw conn (positioned at the first server frame) so the caller can
// drain server traffic and then exercise the detach path.
func dialAttachAndHello(t *testing.T, sockPath string, hello attachwire.Hello) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial attach: %v", err)
	}
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, _ := http.NewRequest(http.MethodGet, "http://unix/attach", nil)
	req.Header.Set("Upgrade", "hoot-attach/1")
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade got %d, want 101", resp.StatusCode)
	}
	if n := bufrw.Reader.Buffered(); n > 0 {
		t.Fatalf("internal: %d bytes buffered after upgrade", n)
	}
	helloPayload, _ := json.Marshal(hello)
	if err := attachwire.WriteFrame(conn, attachwire.MsgHello, helloPayload); err != nil {
		t.Fatalf("write Hello: %v", err)
	}
	return conn
}

// startDrainingClient drains server frames in the background until
// the conn is closed. Returns a channel that closes once the reader
// exits (i.e. once the server force-closes the conn during detach).
func startDrainingClient(conn net.Conn) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(conn)
		for {
			if _, _, err := attachwire.ReadFrame(r); err != nil {
				return
			}
		}
	}()
	return done
}

// runRegistryRunner spawns a Runner whose Service blocks on ctx
// until cancelled. Returns the rpc.sock path and a cleanup that
// cancels + waits for Run to return.
func runRegistryRunner(t *testing.T, key string) (sockPath string, cancelAll func()) {
	t.Helper()
	dir := shortTempDir(t)
	pty := newTestPTY(t)
	svc := serviceFunc(func(ctx context.Context, super Session) error {
		<-ctx.Done()
		return ctx.Err()
	})
	runner := New(SessionConfig{
		StateDir: dir,
		Key:      key,
		Service:  svc,
		PTY:      pty,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	sockPath = filepath.Join(dir, key, "rpc.sock")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := net.Dial("unix", sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rpc.sock did not appear at %s", sockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return sockPath, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Runner.Run did not return within 5s of cancel")
		}
	}
}

// waitForAttachments polls state.json on the rpc.sock until the
// attachments slice has the wanted length, or the deadline expires.
func waitForAttachments(t *testing.T, sockPath string, want int) []AttachmentRecord {
	t.Helper()
	client := httpOverUnix(sockPath)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://unix/state")
		if err != nil {
			t.Fatalf("GET /state: %v", err)
		}
		var sf StateFile
		err = json.NewDecoder(resp.Body).Decode(&sf)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("decode state: %v", err)
		}
		if len(sf.Session.Attachments) == want {
			return sf.Session.Attachments
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, _ := client.Get("http://unix/state")
	body, _ := io.ReadAll(resp.Body)
	t.Fatalf("timed out waiting for %d attachments; last state=%s", want, string(body))
	return nil
}

// TestDetachByAttachmentID brings up a Runner, attaches twice, then
// DELETE /attachments/{id} for one of them. Asserts only that one's
// conn observes EOF and the state.json reflects the removal.
func TestDetachByAttachmentID(t *testing.T) {
	sockPath, stop := runRegistryRunner(t, "abc")
	defer stop()

	hello := attachwire.Hello{Size: attachwire.PTYSize{Cols: 80, Rows: 24}}
	connA := dialAttachAndHello(t, sockPath, hello)
	defer connA.Close()
	doneA := startDrainingClient(connA)

	connB := dialAttachAndHello(t, sockPath, hello)
	defer connB.Close()
	doneB := startDrainingClient(connB)

	atts := waitForAttachments(t, sockPath, 2)
	target := atts[0].ID

	client := httpOverUnix(sockPath)
	req, _ := http.NewRequest(http.MethodDelete, "http://unix/attachments/"+target, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /attachments/%s: %v", target, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}

	// One client should see EOF; the other should still be alive.
	var closedFirst, aliveOther <-chan struct{}
	if atts[0].ID == target {
		closedFirst = doneA
		aliveOther = doneB
	} else {
		closedFirst = doneB
		aliveOther = doneA
	}
	select {
	case <-closedFirst:
	case <-time.After(2 * time.Second):
		t.Fatalf("targeted attachment did not close within 2s")
	}
	select {
	case <-aliveOther:
		t.Fatalf("non-targeted attachment was closed")
	case <-time.After(150 * time.Millisecond):
	}

	// state.json should now show one attachment.
	rest := waitForAttachments(t, sockPath, 1)
	if rest[0].ID == target {
		t.Errorf("state still references closed id %q", target)
	}
}

// TestDetachBySessionID exercises DELETE /attachments (no id) — the
// `hoot detach <session>` path that closes every attachment.
func TestDetachBySessionID(t *testing.T) {
	sockPath, stop := runRegistryRunner(t, "abc")
	defer stop()

	hello := attachwire.Hello{Size: attachwire.PTYSize{Cols: 80, Rows: 24}}
	var conns []net.Conn
	var dones []<-chan struct{}
	for i := 0; i < 3; i++ {
		c := dialAttachAndHello(t, sockPath, hello)
		conns = append(conns, c)
		dones = append(dones, startDrainingClient(c))
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	waitForAttachments(t, sockPath, 3)

	client := httpOverUnix(sockPath)
	req, _ := http.NewRequest(http.MethodDelete, "http://unix/attachments", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /attachments: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}

	var wg sync.WaitGroup
	for _, d := range dones {
		wg.Add(1)
		go func(d <-chan struct{}) {
			defer wg.Done()
			select {
			case <-d:
			case <-time.After(2 * time.Second):
				t.Errorf("attachment did not close within 2s")
			}
		}(d)
	}
	wg.Wait()

	waitForAttachments(t, sockPath, 0)
}

// TestDetachUnknownAttachment404 covers the no-such-id path.
func TestDetachUnknownAttachment404(t *testing.T) {
	sockPath, stop := runRegistryRunner(t, "abc")
	defer stop()

	client := httpOverUnix(sockPath)
	req, _ := http.NewRequest(http.MethodDelete, "http://unix/attachments/zzzz", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

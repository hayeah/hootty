package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

func TestCmdRunRemoteAttachSpawnsThenAttaches(t *testing.T) {
	helloCh := make(chan attachwire.Hello, 1)
	var sawCreate, sawAttach bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			sawCreate = true
			var body createSessionReq
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.Key != "abc123" || strings.Join(body.Argv, " ") != "bash -lc echo hi" {
				t.Errorf("create body = %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session": map[string]any{"key": "abc123"},
				"state":   map[string]any{},
				"alive":   true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/abc123/attach-raw":
			sawAttach = true
			if r.Header.Get("Upgrade") != "hoot-attach/1" {
				t.Errorf("Upgrade = %q, want hoot-attach/1", r.Header.Get("Upgrade"))
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("response writer does not support hijack")
				return
			}
			conn, rw, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: hoot-attach/1\r\nConnection: Upgrade\r\n\r\n")
			if err := rw.Flush(); err != nil {
				t.Errorf("flush upgrade: %v", err)
				return
			}
			typ, payload, err := attachwire.ReadFrame(rw.Reader)
			if err != nil {
				t.Errorf("read Hello: %v", err)
				return
			}
			if typ != attachwire.MsgHello {
				t.Errorf("first frame = 0x%02x, want Hello", typ)
				return
			}
			var hello attachwire.Hello
			if err := json.Unmarshal(payload, &hello); err != nil {
				t.Errorf("decode Hello: %v", err)
				return
			}
			helloCh <- hello
			if err := attachwire.WriteFrame(conn, attachwire.MsgSnapshotScreen, []byte("remote-ready\r\n")); err != nil {
				t.Errorf("write snapshot: %v", err)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	os.Stdin = stdinR
	defer func() {
		os.Stdin = oldStdin
		_ = stdinR.Close()
		_ = stdinW.Close()
	}()

	out := captureStdout(t, func() {
		err := cmdRun([]string{
			"--remote", "http://" + server.Listener.Addr().String(),
			"--attach",
			"--no-reconnect",
			"--prefix-key", "C-a",
			"--key", "abc123",
			"--", "bash", "-lc", "echo hi",
		})
		if err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})
	if !sawCreate {
		t.Fatalf("POST /sessions was not called")
	}
	if !sawAttach {
		t.Fatalf("GET /sessions/abc123/attach-raw was not called")
	}
	if !strings.Contains(out, "[connected. abc123 @ http://"+server.Listener.Addr().String()+"]") {
		t.Fatalf("stdout missing connect banner: %q", out)
	}
	if !strings.Contains(out, "remote-ready") {
		t.Fatalf("stdout missing attached snapshot: %q", out)
	}

	select {
	case hello := <-helloCh:
		if hello.Size.Cols == 0 || hello.Size.Rows == 0 {
			t.Fatalf("Hello.Size = %+v, want non-zero cols/rows", hello.Size)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Hello")
	}
}

func TestAttachOptionsFromFlags(t *testing.T) {
	opts, err := attachOptionsFromFlags("C-a", true)
	if err != nil {
		t.Fatalf("attachOptionsFromFlags: %v", err)
	}
	if opts.PrefixByte != 0x01 {
		t.Fatalf("PrefixByte = 0x%02x, want 0x01", opts.PrefixByte)
	}
	if !opts.NoReconnect {
		t.Fatalf("NoReconnect = false, want true")
	}
	if _, err := attachOptionsFromFlags("x", false); err == nil {
		t.Fatalf("attachOptionsFromFlags accepted printable prefix")
	}
}

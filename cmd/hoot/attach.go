package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	session "github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/attachwire"
	"github.com/hayeah/hootty/internal/sessionpick"
)

// cmdAttach implements `hoot attach`. Two transports:
//
//   - Local (default): dials <state-dir>/<key>/rpc.sock and performs
//     the hoot-attach/1 HTTP/1.1 Upgrade.
//   - Remote (`--remote`): dials a `hoot serve` over http(s), or
//     opens an ssh-backed tunnel to a remote `hoot serve`, then
//     performs the same Upgrade against /sessions/<id>/attach-raw.
//     Server resolves the short-id; the client passes its raw
//     user-typed argument.
//
// The remote path supports automatic reconnect (default; `--no-reconnect`
// to opt out). Termios stays raw across drops; on reconnect the client
// sends a fresh Hello with current local cols/rows, and the server
// repaints by sending a libghostty snapshot of the current screen.
//
// Exit codes:
//
//	0   clean detach (server EOF after normal child exit, <prefix>.,
//	    or remote drop with --no-reconnect)
//	1   protocol error, dial failure on first connect, bad state dir
//	2   argument error (no session matched, ambiguous prefix, bad
//	    --prefix-key, --remote parse error)
//	130 terminated by a trapped signal (SIGINT/SIGTERM/SIGHUP)
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hoot attach [flags] [<id-or-prefix-or-pattern>]

With no positional argument, an interactive fzf picker over the
available sessions is launched. With one argument, hoot attempts an
id-prefix match first; on miss, it falls back to a pre-seeded fzf
picker (--query=<arg> --select-1 --exit-0) so a unique fuzzy match
auto-attaches. Use --strict to disable the fuzzy fallback.

  --remote <url>         remote `+"`hoot serve`"+` target: host, host:port,
                         user@host (defaults to ssh://), or an explicit
                         http(s)://host:port or ssh://host URL
  --state-dir <dir>      session state directory (default: ~/.hoot);
                         local sessions and ssh tunnel state
  --strict               exact id-prefix match only — never invoke fzf,
                         never open the picker
  --no-reconnect         exit on first drop instead of auto-reconnecting
                         (--remote only; ignored for local attach)
  --prefix-key <key>     command prefix byte (default: C-^). Forms: C-^, ^^,
                         0x1e, or a single ASCII control byte.

The picker line format is `+"`[id]\\t@host\\tcwd\\tcmd\\ttag`"+`. The
single-char field markers (`+"`@`, `[`, `*`"+`) double as natural fzf
query prefixes — type `+"`@m4`"+` to scope to host, `+"`[a3`"+` to scope to id, or
`+"`*`"+` for live attached sessions. fzf's full extended-search syntax
applies (`+"`'exact`, `^prefix`, `suffix$`, `!negate`, `a | b`"+`).

The fzf binary must be on PATH; use --strict to bypass it for
scripted use.

Inside an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte to the remote; <prefix>Ctrl-Z suspends hoot
attach (resume with fg); <prefix>c clones the session.

On attach the terminal window title is set to a one-line summary in
the form `+"`🦉 <id> [@host] <cwd> [<cmd...>]`"+` (`+"`@host`"+` omitted for local
sessions, argv truncated at 60 runes). The previous title is saved
via the xterm title stack (CSI 22;2t) and restored (CSI 23;2t) on
detach. Inner TUIs that set their own title will override hoot's.

The same session summary is printed once as ordinary terminal output
just before the screen snapshot is rendered, then the visible viewport
is scrolled into the local terminal's scrollback ring — so a single
scroll-up after attach reveals which session you are in, without a
live overlay that fights TUI redraws.

During a remote disconnect: backoff is 1,2,4,8,16,30s capped at 30s and
retries forever. Press any key to wake the backoff and retry now.
<prefix>. still detaches cleanly.
`)
	}
	remoteFlag := fs.String("remote", "", "remote target (host, user@host, host:port — defaults to ssh://; or explicit http(s)://host:port or ssh://host)")
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting (--remote only)")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte (e.g. C-^, ^a, 0x1c)")
	strict := fs.Bool("strict", false, "exact id-prefix match only — no fzf picker, no fuzzy fallback")
	restorerName := fs.String("restorer", "hoot", "terminal restorer policy: hoot (default, comprehensive cleanup) or dtach (clear-on-attach + cursor-show-on-detach; experimental control)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return 2
	}
	arg := ""
	if len(rest) == 1 {
		arg = rest[0]
	}

	attachOpts, err := attachOptionsFromFlags(*prefixSpec, *noReconnect, *restorerName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
		return 2
	}

	var reconnect bool
	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
		return 2
	}
	if remote != nil {
		defer remote.Close()
		reconnect = !attachOpts.NoReconnect
	}

	key, code, err := resolveSessionKey(arg, *strict, remote, *stateDir, pickerOptions{Verb: "attach"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
		return code
	}

	var target attachTarget
	if remote != nil {
		target = remoteAttachTarget(remote, key)
	} else {
		target = localAttachTarget(*stateDir, key)
	}

	// Best-effort load of the session's StateFile to feed the HUD
	// (terminal title + the at-attach scrollback status line). A miss
	// here is not fatal — runServerLoop falls back to a "[connected. K
	// @ H]" banner, and the title stack stays untouched.
	hud := newHUDState(loadHUDLine(remote, *stateDir, key))

	exitCode, err := runAttachLoop(target, attachOpts.PrefixByte, reconnect, hud, attachOpts.Restorer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
	}
	return exitCode
}

// loadHUDLine fetches a SessionWithMeta for `key` and renders it via
// FormatHUD. Returns "" on any error so the caller can keep going —
// the session is already resolved, the HUD is purely cosmetic.
func loadHUDLine(remote *Remote, stateDir, key string) string {
	sm, ok := loadSessionMeta(remote, stateDir, key)
	if !ok {
		return ""
	}
	return sessionpick.FormatHUD(sm)
}

// loadSessionMeta returns the SessionWithMeta for key, or (zero, false)
// on any failure. Local: read state.json directly. Remote: walk the
// /sessions list and find the matching key.
func loadSessionMeta(remote *Remote, stateDir, key string) (sessionpick.SessionWithMeta, bool) {
	if remote != nil {
		states, err := loadSessionList(remote, stateDir)
		if err != nil {
			return sessionpick.SessionWithMeta{}, false
		}
		for _, sm := range states {
			if sm.State != nil && sm.State.Session.Key == key {
				return sm, true
			}
		}
		return sessionpick.SessionWithMeta{}, false
	}
	store := session.NewStore(stateDir)
	state, err := store.Load(key)
	if err != nil {
		return sessionpick.SessionWithMeta{}, false
	}
	return sessionpick.SessionWithMeta{
		State: state,
		Alive: store.IsAlive(key),
		Host:  "local",
	}, true
}

// stdoutIsTTY reports whether stdout is connected to a terminal. We
// gate the picker on stdout (not stdin) because fzf opens /dev/tty
// itself for keyboard, but it draws on whatever stdout points to —
// no point launching it for output that's being piped or redirected.
//
// var so tests can swap it (the real os.Stdout always reports !tty
// under `go test`).
var stdoutIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// dialFn returns a connection that has already completed the
// hoot-attach/1 HTTP/1.1 Upgrade handshake — i.e. ready for
// attachwire frames in both directions.
type dialFn func(ctx context.Context) (net.Conn, error)

type attachLabel struct {
	Session string
	Host    string
}

type attachTarget struct {
	dial  dialFn
	label attachLabel
	clone func(ctx context.Context) (attachTarget, error)
}

type attachTargetState struct {
	mu     sync.Mutex
	target attachTarget
}

func (s *attachTargetState) current() attachTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

func (s *attachTargetState) set(target attachTarget) {
	s.mu.Lock()
	s.target = target
	s.mu.Unlock()
}

type attachWriters struct {
	stdout io.Writer
	stderr io.Writer
}

type attachOptions struct {
	PrefixByte  byte
	NoReconnect bool
	Restorer    string // "hoot" (default) or "dtach"; passed to newRestorer
}

func attachOptionsFromFlags(prefixSpec string, noReconnect bool, restorer string) (attachOptions, error) {
	prefixByte, err := attachwire.ParsePrefixKey(prefixSpec)
	if err != nil {
		return attachOptions{}, err
	}
	if _, err := newRestorer(restorer); err != nil {
		return attachOptions{}, err
	}
	return attachOptions{
		PrefixByte:  prefixByte,
		NoReconnect: noReconnect,
		Restorer:    restorer,
	}, nil
}

const (
	attachHeartbeatInterval = 200 * time.Millisecond
	attachHeartbeatTimeout  = 800 * time.Millisecond
)

// runAttachLoop is the top-level driver. It owns termios, signal
// traps, the stdin reader and SIGWINCH watcher; everything below
// re-runs per attempt across reconnects.
//
// Returns the process exit code and an optional error to print.
func runAttachLoop(initial attachTarget, prefixByte byte, reconnect bool, hud *hudState, restorerName string) (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writers := attachWriters{stdout: os.Stdout, stderr: os.Stderr}
	targets := &attachTargetState{target: initial}
	var attached atomic.Bool
	restorer, err := newRestorer(restorerName)
	if err != nil {
		return 2, err
	}
	// Detach order: title pop FIRST (cosmetic UI state, runs even if we
	// never connected), THEN emitDetach (mode-reset cleanup + banner,
	// only if we did connect). Sequencing per BOSS_LOG: "the title push/
	// pop is purely cosmetic UI state; the restorer owns the inner-
	// program-affecting modes. Doing hud-first-then-restorer at detach
	// means the title pops before we reset all the modes."
	defer func() {
		hud.onDetach(writers.stdout)
		if attached.Load() {
			emitDetach(writers.stdout, targets.current().label, restorer)
		}
	}()

	// Signal trap. Raw-mode Ctrl-C is just a byte forwarded to the
	// remote, so SIGINT here is from `kill -INT` not the keyboard.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	signaled := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			close(signaled)
			cancel()
		case <-ctx.Done():
		}
	}()

	// Termios raw if stdin is a tty. Set once; stays raw across
	// reconnects so we don't flicker between drops.
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		var err error
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 1, fmt.Errorf("term.MakeRaw: %w", err)
		}
		defer func() {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}()
	}

	// Restorer Attach FIRST: pushes a kitty kbd stack frame (hoot mode)
	// or clears the screen (dtach mode). Sequenced BEFORE hud.onAttach
	// per the boss-recommended ordering — restorer owns the inner-program-
	// affecting modes; HUD title is cosmetic UI state and can be set
	// "on a clean slate" once the restorer has done its setup. Sequenced
	// AFTER MakeRaw so the bytes don't get post-processed by canonical-
	// mode line discipline. Note: matches dtach upstream's "clear at
	// attach.c entry, before master connect" behavior — if the dial
	// fails, dtach mode still cleared the screen, faithful to dtach.
	if err := restorer.Attach(writers.stdout); err != nil {
		return 1, fmt.Errorf("restorer.Attach: %w", err)
	}

	// Push + set the terminal title now. Sequenced AFTER MakeRaw so
	// the OSC bytes don't get post-processed by canonical-mode line
	// discipline, and so any user keypress arriving while the dial is
	// in flight isn't echoed by the kernel before the prefix-key FSM
	// can consume it. Sequenced BEFORE runConnectLoop so the title
	// reflects the session as soon as `hoot attach` lands, even if the
	// first dial is slow or backoffs.
	hud.onAttach(writers.stdout)

	// Initial size; SIGWINCH updates it.
	cols, rows := localOrDefaultSize()
	var size atomic.Uint64
	size.Store(packSize(cols, rows))

	// Shared connection state. The stdin and SIGWINCH goroutines see
	// the *current* session's send func via this; if no session,
	// inputs are dropped and a wake is signaled.
	shared := &sharedSession{
		wake: make(chan struct{}, 1),
	}

	// SIGWINCH watcher: re-reads size, updates atomic, sends a Size
	// frame to the current session (if connected). Coalesces.
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	go func() {
		for {
			select {
			case <-winchCh:
				// Drain any backlog so we coalesce.
				for {
					select {
					case <-winchCh:
					default:
						goto sendIt
					}
				}
			sendIt:
				c, r := localOrDefaultSize()
				size.Store(packSize(c, r))
				payload, _ := json.Marshal(attachwire.Size{Cols: c, Rows: r})
				shared.sendIfConnected(attachwire.MsgSize, payload)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Stdin reader + prefix-key FSM. Runs once across all reconnects.
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		runStdinFSM(ctx, prefixByte, shared, cancel, func(ctx context.Context) error {
			current := targets.current()
			if current.clone == nil {
				return errors.New("clone is not supported for this attach target")
			}
			fmt.Fprintf(writers.stderr, "\r\n\x1b[2m[hoot: cloning %s...]\x1b[0m\r\n", current.label.Session)
			next, err := current.clone(ctx)
			if err != nil {
				return err
			}
			targets.set(next)
			shared.markSwitched()
			return nil
		}, writers.stderr)
	}()

	// Reconnect loop.
	loopErr := runConnectLoop(ctx, targets, reconnect, &size, shared, &attached, writers, restorer, hud)

	// Determine exit code from outer signals.
	select {
	case <-signaled:
		return 130, nil
	default:
	}
	if loopErr != nil {
		var hee *httpErrExit2
		if errors.As(loopErr, &hee) {
			fmt.Fprintln(os.Stderr, "hoot attach: "+hee.msg)
			return 2, nil
		}
		return 1, loopErr
	}
	return 0, nil
}

// runConnectLoop runs the connect→session→drop→reconnect FSM. It
// returns when:
//
//   - ctx is cancelled (user detach via <prefix>d, signal, etc.)
//   - the session ends cleanly (server EOF) → return nil
//   - reconnect is disabled and a drop or first-connect error occurs
//   - first connect fails (we always fail-fast on the first attempt
//     even with reconnect=true; a typo'd host should error in a beat)
func runConnectLoop(
	ctx context.Context,
	targets *attachTargetState,
	reconnect bool,
	size *atomic.Uint64,
	shared *sharedSession,
	attached *atomic.Bool,
	writers attachWriters,
	restorer TerminalRestorer,
	hud *hudState,
) error {
	gotConnectedOnce := false
	for {
		// Dial.
		target := targets.current()
		conn, err := target.dial(ctx)
		if err != nil {
			// User cancelled (e.g. <prefix>d before we connected, or
			// SIGINT during dial). Clean exit, not error.
			if ctx.Err() != nil {
				return nil
			}
			// Sentinel server-side resolution errors: surface and exit 2.
			var hee *httpErrExit2
			if errors.As(err, &hee) {
				return hee
			}
			if !gotConnectedOnce {
				// First-connect failure: fail-fast.
				return err
			}
			// Mid-stream drop, retry path: backoff (with keystroke wake).
			if !reconnect {
				return err
			}
			if !waitBackoff(ctx, shared, "redial failed") {
				return nil // ctx cancelled during backoff = clean detach
			}
			continue
		}

		// Connected. Reset backoff and send Hello with current size.
		shared.resetTier()
		c, r := unpackSize(size.Load())
		err = runSession(ctx, conn, c, r, shared, target.label, attached, writers, restorer, hud)
		_ = conn.Close()

		if ctx.Err() != nil {
			// User detach / signal — clean exit regardless of err.
			return nil
		}
		if shared.takeSwitched() {
			gotConnectedOnce = false
			continue
		}
		gotConnectedOnce = true
		if !reconnect {
			// In non-reconnect mode, treat EOF / closed as clean
			// detach (matches local-socket "child exited" semantics).
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Reconnect mode: ANY conn termination (including EOF from a
		// bridge dying mid-stream) is a drop we should retry. If the
		// session is genuinely gone, the next dial returns 404 and we
		// surface exit 2.
		// Drop. Print disconnected line; wait for backoff.
		fmt.Fprintf(writers.stderr,
			"\r\n\x1b[2m[hoot: disconnected: %v]\x1b[0m\r\n", err)
		if !waitBackoff(ctx, shared, "") {
			return nil // ctx cancelled during backoff = clean detach
		}
		// Drain any pending wake before redial so the new connection
		// doesn't see a stale wake.
		select {
		case <-shared.wake:
		default:
		}
	}
}

// runSession runs a single connected session over conn. Owns the
// per-session writer goroutine and server-read loop. Sends the Hello
// frame as the first thing on the wire. Returns nil on clean server
// EOF, an error otherwise.
func runSession(
	ctx context.Context,
	conn net.Conn,
	cols, rows uint16,
	shared *sharedSession,
	label attachLabel,
	attached *atomic.Bool,
	writers attachWriters,
	restorer TerminalRestorer,
	hud *hudState,
) error {
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	type outMsg struct {
		typ     byte
		payload []byte
	}
	outCh := make(chan outMsg, 256)
	var lastSent atomic.Int64
	var lastRecv atomic.Int64
	now := time.Now().UnixNano()
	lastSent.Store(now)
	lastRecv.Store(now)
	writerDone := make(chan error, 1)
	go func() {
		var werr error
		for m := range outCh {
			if err := attachwire.WriteFrame(conn, m.typ, m.payload); err != nil {
				werr = err
				break
			}
			lastSent.Store(time.Now().UnixNano())
		}
		writerDone <- werr
	}()

	send := func(typ byte, payload []byte) bool {
		select {
		case outCh <- outMsg{typ, payload}:
			return true
		case <-sessionCtx.Done():
			return false
		}
	}

	// Wire send into the shared bus *before* Hello so Size frames
	// from a SIGWINCH that fired between connect and Hello aren't
	// lost. (Order doesn't matter as long as Hello goes first; our
	// channel buffers up to 256 so an early Size queues behind.)
	shared.setSend(send, conn)
	defer shared.clearSend()

	// Hello.
	helloPayload, _ := json.Marshal(attachwire.Hello{
		Size:   attachwire.PTYSize{Cols: cols, Rows: rows},
		Origin: localOrigin(),
	})
	if !send(attachwire.MsgHello, helloPayload) {
		return errors.New("ctx cancelled before Hello")
	}

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		pingTicker := time.NewTicker(attachHeartbeatInterval)
		defer pingTicker.Stop()
		watchdogTicker := time.NewTicker(attachHeartbeatInterval / 2)
		defer watchdogTicker.Stop()
		for {
			select {
			case <-pingTicker.C:
				if time.Since(time.Unix(0, lastSent.Load())) > attachHeartbeatInterval {
					send(attachwire.MsgPing, nil)
				}
			case <-watchdogTicker.C:
				if time.Since(time.Unix(0, lastRecv.Load())) > attachHeartbeatTimeout {
					_ = conn.Close()
					return
				}
			case <-sessionCtx.Done():
				return
			}
		}
	}()

	// Server-read loop (in this goroutine, since we own the lifetime).
	r := bufio.NewReader(conn)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runServerLoop(sessionCtx, r, label, attached, writers, restorer, hud, rows, func() {
			lastRecv.Store(time.Now().UnixNano())
		})
	}()

	// Wait for either side to finish.
	var sessErr error
	select {
	case err := <-serverDone:
		sessErr = err
	case <-ctx.Done():
		// Outer cancel; tear down.
	}

	sessionCancel()
	_ = conn.Close()
	<-heartbeatDone
	close(outCh)
	<-writerDone
	// Drain serverDone if it hasn't fired yet.
	select {
	case <-serverDone:
	default:
	}

	// EOF / closed conn are not nil-ed out here — runConnectLoop
	// decides per the reconnect flag (mid-stream bridge death looks
	// like EOF on the TCP side, but in reconnect=false mode we still
	// want to treat it as a clean detach for backwards compat with
	// the local-socket path's "child exited → session closed" semantics).
	return sessErr
}

// runStdinFSM reads stdin one byte at a time and runs the prefix-key
// state machine. When connected, byte events are forwarded as
// MsgInput frames; when disconnected, they're dropped silently and
// signal a reconnect-wake. <prefix>. cancels the outer ctx.
//
// Command vocabulary mirrors mosh:
//
//	<prefix> .       detach (cancel ctx)
//	<prefix> ^       send a literal prefix byte to the remote
//	<prefix> Ctrl-Z  SIGTSTP self (resume with fg)
//	<prefix> c       clone current session and switch to it
func runStdinFSM(ctx context.Context, prefix byte, shared *sharedSession, cancel context.CancelFunc, clone func(context.Context) error, stderr io.Writer) {
	readCh := make(chan byteOrErr, 1)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				readCh <- byteOrErr{buf[0], nil}
			}
			if err != nil {
				readCh <- byteOrErr{0, err}
				return
			}
		}
	}()

	act := fsmActions{
		forwardBytes: func(bs []byte) {
			for _, b := range bs {
				shared.sendInputOrWake(b)
			}
		},
		literal: func() {
			// Literal prefix: send if connected, drop if not (no wake —
			// this is data, not a "user is here" signal).
			shared.sendIfConnected(attachwire.MsgInput, []byte{prefix})
		},
		detach: func() {
			cancel()
		},
		suspend: func() {
			_ = syscall.Kill(os.Getpid(), syscall.SIGTSTP)
		},
		clone: func() error {
			return clone(ctx)
		},
	}
	runChordFSM(prefix, readByteFromChan(ctx, readCh), act, stderr)
}

// runServerLoop reads frames from the server and dispatches them.
// Snapshot parts are locally phased into:
//
//  1. scrollback replay (MsgSnapshotScrollback) — bytes flow straight
//     to stdout so the local terminal walks them through its own
//     scroll machinery and pushes lines onto the local scrollback ring.
//  2. status-line print + viewport scroll (on first MsgSnapshotScreen):
//     write the HUD line as a normal scrolled line, then `CSI <rows> S`
//     to scroll the entire visible viewport (HUD + scrollback tail)
//     into the local scrollback ring, then `CSI H` to home the cursor.
//     The HUD line ends up as the bottom-most scrollback entry — "scroll
//     up one line" reveals it, per the section spec. Modern xterm,
//     iTerm2, Ghostty, kitty, Wezterm, Alacritty all push CSI-S-displaced
//     lines into scrollback.
//  3. visible screen (MsgSnapshotScreen payload) — written onto the
//     now-blank viewport; the formatter's bytes fully repaint every cell.
//
// Subsequent MsgSnapshotScreen frames (after a reconnect re-replay)
// take the same path so the user gets a fresh "you reconnected to X"
// scrollback entry on every connect.
//
// Live Output writes straight to stdout. Size reports go to stderr.
// Returns on EOF or protocol error. The restorer's Attach is already
// called from runAttachLoop entry (once per process); here we only
// call Observe on each chunk for state tracking.
//
// `localRows` is the local terminal's visible row count, used as the
// `Pn` for the `CSI Pn S` scroll-up. Passing the exact row count
// guarantees the entire viewport is scrolled into local scrollback
// regardless of where the cursor was before the snapshot phase.
func runServerLoop(ctx context.Context, r *bufio.Reader, label attachLabel, attached *atomic.Bool, writers attachWriters, restorer TerminalRestorer, hud *hudState, localRows uint16, markRecv func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		typ, payload, err := attachwire.ReadFrame(r)
		if err != nil {
			return err
		}
		if markRecv != nil {
			markRecv()
		}
		switch typ {
		case attachwire.MsgSnapshotScrollback:
			if len(payload) > 0 {
				if _, werr := writers.stdout.Write(payload); werr != nil {
					return werr
				}
			}
			if restorer != nil {
				restorer.Observe(payload)
			}
		case attachwire.MsgSnapshotScreen:
			if !attached.Load() {
				attached.Store(true)
			}
			if werr := emitAttachStatus(writers.stdout, hud, label, localRows); werr != nil {
				return werr
			}
			if len(payload) > 0 {
				if _, werr := writers.stdout.Write(payload); werr != nil {
					return werr
				}
			}
			if restorer != nil {
				restorer.Observe(payload)
			}
		case attachwire.MsgOutput:
			if _, werr := writers.stdout.Write(payload); werr != nil {
				return werr
			}
			if restorer != nil {
				restorer.Observe(payload)
			}
		case attachwire.MsgSize:
			var sz attachwire.Size
			if err := json.Unmarshal(payload, &sz); err == nil {
				lc, lr := localSize()
				if lc != 0 && (sz.Cols != lc || sz.Rows != lr) {
					fmt.Fprintf(writers.stderr,
						"\r\n\x1b[2m[hoot: remote pty %d×%d, your terminal %d×%d]\x1b[0m\r\n",
						sz.Cols, sz.Rows, lc, lr)
				}
			}
		case attachwire.MsgPong:
			// Heartbeat response; activity was recorded above.
		case attachwire.MsgHello, attachwire.MsgInput, attachwire.MsgPing:
			return fmt.Errorf("server sent unexpected frame type 0x%02x", typ)
		default:
			return fmt.Errorf("server sent unknown frame type 0x%02x", typ)
		}
	}
}

// emitAttachStatus writes the one-shot "you are attached" status line
// into the local terminal so it lands as the bottom-most line of the
// local scrollback ring — "scroll up one line" reveals it. The
// sequence is engineered so that:
//
//   - the HUD line ends up at the BOTTOM of scrollback (newest entry),
//     not several rows up; the user shouldn't have to hunt for it.
//   - whatever the local terminal had in its viewport before attach
//     is preserved in scrollback above the HUD line, instead of being
//     wiped by the cursor-clear that the old emitConnect+CSI 2 J path
//     used to do.
//
// Bytes emitted, in order:
//
//	CSI <localRows> ; 1 H   cursor to bottom-left of viewport
//	CSI 2 K                 clear that bottom row (in case it had local content)
//	\x1b[2m <line> \x1b[0m  the dim-styled HUD line on the bottom row
//	CSI <localRows> S       scroll up `localRows` lines: every visible
//	                        row (including the HUD on the bottom row)
//	                        is appended to local scrollback in order;
//	                        viewport becomes blank.
//	CSI H                   cursor home for the snapshot paint
//
// On terminals that implement `CSI Pn S` per xterm (xterm, iTerm2,
// Ghostty, kitty, Wezterm, Alacritty all do), the displaced rows are
// appended to scrollback in viewport order — so after the scroll the
// scrollback's last row is the HUD line. On a hypothetical terminal
// that doesn't push CSI-S-displaced rows into scrollback, the HUD line
// would simply not appear in scrollback; the screen still paints
// correctly because the snapshot is a full repaint of the viewport.
//
// `hud` is allowed to be nil or carry an empty Line(); in that case we
// fall back to a "[connected. K @ H]" string so every attach has at
// least a session-and-host marker.
func emitAttachStatus(w io.Writer, hud *hudState, label attachLabel, localRows uint16) error {
	line := hud.Line()
	if line == "" {
		line = fmt.Sprintf("[connected. %s @ %s]", label.Session, label.Host)
	}
	if localRows == 0 {
		localRows = 24
	}
	seq := fmt.Sprintf("\x1b[%d;1H\x1b[2K\x1b[2m%s\x1b[0m\x1b[%dS\x1b[H", localRows, line, localRows)
	if _, err := io.WriteString(w, seq); err != nil {
		return err
	}
	return nil
}

// emitDetach writes the restorer's cleanup bytes followed by the
// "[disconnected. ...]" banner. The banner is local CLI UX, not
// terminal-state hygiene, so it stays here rather than in any
// TerminalRestorer impl. Cleanup error is swallowed: this is the final
// write before the deferred function returns, and any failure mode
// (closed stdout, broken pipe) means the user already isn't seeing
// output anyway.
func emitDetach(w io.Writer, label attachLabel, restorer TerminalRestorer) {
	if restorer != nil {
		_ = restorer.Cleanup(w)
	}
	fmt.Fprintf(w, "\r\n[disconnected. %s @ %s]\r\n", label.Session, label.Host)
}

// sharedSession is the cross-goroutine bus connecting the stdin/
// SIGWINCH/wake machinery to the *current* connection's writer.
// When sendFn is nil, the session is disconnected and inputs are
// dropped (with a wake signal for stdin events).
type sharedSession struct {
	mu       sync.Mutex
	sendFn   func(typ byte, payload []byte) bool
	conn     net.Conn
	switched bool
	wake     chan struct{}
	tier     atomic.Int32 // current backoff tier; 0 = first retry == 1s
}

func (s *sharedSession) setSend(fn func(byte, []byte) bool, conn net.Conn) {
	s.mu.Lock()
	s.sendFn = fn
	s.conn = conn
	s.mu.Unlock()
}

func (s *sharedSession) clearSend() {
	s.mu.Lock()
	s.sendFn = nil
	s.conn = nil
	s.mu.Unlock()
}

func (s *sharedSession) markSwitched() {
	s.mu.Lock()
	s.switched = true
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sharedSession) takeSwitched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switched := s.switched
	s.switched = false
	return switched
}

// sendInputOrWake: if connected, forward the byte as MsgInput; if
// not, drop and signal a wake (the user is at the keyboard, retry now).
func (s *sharedSession) sendInputOrWake(b byte) {
	s.mu.Lock()
	fn := s.sendFn
	s.mu.Unlock()
	if fn != nil {
		fn(attachwire.MsgInput, []byte{b})
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// sendIfConnected sends the frame if a session exists; otherwise drops
// it without waking (used for SIGWINCH and literal-prefix bytes).
func (s *sharedSession) sendIfConnected(typ byte, payload []byte) {
	s.mu.Lock()
	fn := s.sendFn
	s.mu.Unlock()
	if fn != nil {
		fn(typ, payload)
	}
}

// waitBackoff sleeps for the next backoff tier (capped at 30s),
// rewriting the disconnect-status line in place each second so the
// user sees the countdown advance. Returns true on timer expiry,
// false if ctx cancels. A keystroke on shared.wake also returns true
// (immediate redial).
//
// State is held in a method-local closure: each call advances the
// schedule one tier. Reset by exiting waitBackoff after a successful
// connect (caller drains shared.wake).
var backoffSchedule = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// backoffTier is incremented by waitBackoff; reset to 0 on success.
// Stored on the shared session so it persists across calls.
func waitBackoff(ctx context.Context, shared *sharedSession, _ string) bool {
	tier := int(shared.tier.Add(1) - 1)
	if tier >= len(backoffSchedule) {
		tier = len(backoffSchedule) - 1
	}
	d := backoffSchedule[tier]
	deadline := time.Now().Add(d)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return true
		}
		// Print/refresh the status line in place.
		secs := int((remain + time.Second - 1) / time.Second)
		fmt.Fprintf(os.Stderr,
			"\r\x1b[2K\x1b[2m[hoot: reconnecting in %ds — press any key to retry now]\x1b[0m",
			secs)
		// Sleep up to 1s so we can refresh the countdown.
		step := time.Second
		if remain < step {
			step = remain
		}
		select {
		case <-ctx.Done():
			fmt.Fprint(os.Stderr, "\r\x1b[2K")
			return false
		case <-shared.wake:
			fmt.Fprint(os.Stderr, "\r\x1b[2K")
			return true
		case <-time.After(step):
		}
	}
}

// resetTier zeroes the backoff schedule. Called after each successful
// session connect.
func (s *sharedSession) resetTier() { s.tier.Store(0) }

// httpErrExit2 is the dialer's sentinel for server-side resolve
// errors (404 no-match, 409 ambiguous). The connect loop maps it to
// exit code 2.
type httpErrExit2 struct {
	msg string
}

func (e *httpErrExit2) Error() string { return e.msg }

// localDialer returns a dialFn for the local rpc.sock case.
func localAttachTarget(stateDir, key string) attachTarget {
	sockPath := filepath.Join(stateDir, key, "rpc.sock")
	return attachTarget{
		dial:  localDialer(sockPath),
		label: attachLabel{Session: key, Host: "local"},
		clone: func(ctx context.Context) (attachTarget, error) {
			body, err := cloneLocalSession(ctx, stateDir, key)
			if err != nil {
				return attachTarget{}, err
			}
			return localAttachTarget(stateDir, body.Session.Key), nil
		},
	}
}

func remoteAttachTarget(remote *Remote, key string) attachTarget {
	return attachTarget{
		dial:  dialAttachRaw(remote, key),
		label: attachLabel{Session: key, Host: remote.display},
		clone: func(ctx context.Context) (attachTarget, error) {
			body, err := cloneSessionRemote(remote, key, "", nil)
			if err != nil {
				return attachTarget{}, err
			}
			return remoteAttachTarget(remote, body.Session.Key), nil
		},
	}
}

func cloneLocalSession(ctx context.Context, stateDir, key string) (*cloneSessionResp, error) {
	sockPath := filepath.Join(stateDir, key, "rpc.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnixSock(ctx, sockPath)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/clone", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, remoteResponseError(resp)
	}
	var body cloneSessionResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.StateFile == nil || body.Session.Key == "" {
		return nil, errors.New("local clone: response missing session key")
	}
	return &body, nil
}

// localDialer returns a dialFn for the local rpc.sock case.
func localDialer(sockPath string) dialFn {
	return func(ctx context.Context) (net.Conn, error) {
		conn, err := dialUnixSock(ctx, sockPath)
		if err != nil {
			return nil, err
		}
		if err := upgradeAttachConn(conn, "http://hoot/attach"); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// upgradeAttachConn writes the hoot-attach/1 HTTP/1.1 Upgrade
// request to conn and reads the response. On 101 the conn is left
// positioned at the first attachwire byte. On 404 / 409 returns a
// *httpErrExit2 sentinel; on other non-101 returns a generic error.
func upgradeAttachConn(conn net.Conn, urlStr string) error {
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Upgrade", "hoot-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		return fmt.Errorf("write upgrade: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		return fmt.Errorf("flush upgrade: %w", err)
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		return fmt.Errorf("read upgrade response: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		// Hand back any bytes the bufio reader may have over-read.
		// We can't easily push them back into conn; instead, callers
		// of this function must use a fresh bufio.Reader on conn for
		// frame parsing. Since attachwire is server-initiated after
		// the upgrade, in practice bufrw.Reader has 0 buffered bytes
		// here. (We assert this via Buffered().)
		if n := bufrw.Reader.Buffered(); n > 0 {
			return fmt.Errorf("internal: %d bytes buffered after upgrade response", n)
		}
		return nil
	case http.StatusNotFound:
		body, _ := io.ReadAll(resp.Body)
		return &httpErrExit2{msg: strings.TrimSpace(string(body))}
	case http.StatusConflict:
		body, _ := io.ReadAll(resp.Body)
		return &httpErrExit2{msg: strings.TrimSpace(string(body))}
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server refused upgrade: %s\n%s", resp.Status, string(body))
	}
}

// dialUnixSock dials a unix socket, with a long-path fallback (chdir
// then bind a relative path). Mirrors the server-side dialSock.
func dialUnixSock(ctx context.Context, sockPath string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err == nil {
		return conn, nil
	}
	// Fallback: chdir to make the socket path short.
	cwd, gerr := os.Getwd()
	if gerr != nil {
		return nil, err
	}
	if cerr := os.Chdir(filepath.Dir(sockPath)); cerr != nil {
		return nil, err
	}
	defer os.Chdir(cwd)
	return d.DialContext(ctx, "unix", filepath.Base(sockPath))
}

// localOrigin assembles the Origin metadata the client reports in
// Hello: hostname + the usual TERM/$TERM_PROGRAM env vars + $USER.
// Best-effort: any field that can't be read is left empty and
// omitempty-elided over the wire.
func localOrigin() attachwire.Origin {
	host, _ := os.Hostname()
	return attachwire.Origin{
		Host:               host,
		Term:               os.Getenv("TERM"),
		TermProgram:        os.Getenv("TERM_PROGRAM"),
		TermProgramVersion: os.Getenv("TERM_PROGRAM_VERSION"),
		User:               os.Getenv("USER"),
	}
}

// localSize returns local terminal cols/rows, or (0,0) if stdin is
// not a tty.
func localSize() (uint16, uint16) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return 0, 0
	}
	c, r, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 0, 0
	}
	return uint16(c), uint16(r)
}

// localOrDefaultSize returns local terminal size, falling back to
// 80×24 if stdin is not a tty (scripted attaches).
func localOrDefaultSize() (uint16, uint16) {
	c, r := localSize()
	if c == 0 || r == 0 {
		return 80, 24
	}
	return c, r
}

// packSize / unpackSize fold (cols, rows) into a single uint64 so
// they can ride an atomic.Uint64 without a separate mutex.
func packSize(cols, rows uint16) uint64 {
	return uint64(cols)<<16 | uint64(rows)
}

func unpackSize(v uint64) (uint16, uint16) {
	return uint16(v >> 16), uint16(v & 0xffff)
}

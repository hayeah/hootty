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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/attachwire"
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
		fmt.Fprintf(os.Stderr, `usage: hoot attach [flags] <id-or-prefix>

  --remote <url>         remote `+"`hoot serve`"+` target: host:port,
                         http://host:port, https://host:port, or ssh://host
  --state-dir <dir>      session state directory (default: ~/.hoot);
                         local sessions and ssh tunnel state
  --no-reconnect         exit on first drop instead of auto-reconnecting
                         (--remote only; ignored for local attach)
  --no-ascii-cinema-playback
                         skip asciicast history playback on first attach
  --ascii-cinema-playback-window <duration>
                         history window to replay (default 5m; 0 = full cast)
  --ascii-cinema-playback-speed <float>
                         playback speed multiplier (default 8)
  --prefix-key <key>     command prefix byte (default: C-^). Forms: C-^, ^^,
                         0x1e, or a single ASCII control byte.

Inside an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte to the remote; <prefix>Ctrl-Z suspends hoot
attach (resume with fg); <prefix>? prints help.

During a remote disconnect: backoff is 1,2,4,8,16,30s capped at 30s and
retries forever. Press any key to wake the backoff and retry now.
<prefix>. still detaches cleanly.
`)
	}
	remoteFlag := fs.String("remote", "", "remote target (host:port, http(s)://host:port, or ssh://host)")
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting (--remote only)")
	noAsciiCinemaPlayback := fs.Bool("no-ascii-cinema-playback", false, "skip asciicast history playback on first attach")
	asciiCinemaPlaybackWindow := fs.Duration("ascii-cinema-playback-window", 5*time.Minute, "asciicast history window to replay (0 = full cast)")
	asciiCinemaPlaybackSpeed := fs.Float64("ascii-cinema-playback-speed", 8, "asciicast playback speed multiplier")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte (e.g. C-^, ^a, 0x1c)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return 2
	}

	attachOpts, err := attachOptionsFromFlags(
		*prefixSpec,
		*noReconnect,
		*noAsciiCinemaPlayback,
		*asciiCinemaPlaybackWindow,
		*asciiCinemaPlaybackSpeed,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
		return 2
	}

	var target attachTarget
	var reconnect bool
	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
		return 2
	}
	if remote != nil {
		defer remote.Close()
		reconnect = !attachOpts.NoReconnect
		target = remoteAttachTarget(remote, rest[0])
	} else {
		store := session.NewStore(*stateDir)
		state, err := store.Resolve(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
			return 2
		}
		target = localAttachTarget(*stateDir, state.Session.Key)
	}

	exitCode, err := runAttachLoop(target, attachOpts.PrefixByte, reconnect, attachOpts.Playback)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot attach: %v\n", err)
	}
	return exitCode
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

type attachPlaybackConfig struct {
	Enabled bool
	Window  time.Duration
	Speed   float64
}

type attachOptions struct {
	PrefixByte  byte
	NoReconnect bool
	Playback    attachPlaybackConfig
}

func attachOptionsFromFlags(prefixSpec string, noReconnect, noAsciiCinemaPlayback bool, asciiCinemaPlaybackWindow time.Duration, asciiCinemaPlaybackSpeed float64) (attachOptions, error) {
	prefixByte, err := attachwire.ParsePrefixKey(prefixSpec)
	if err != nil {
		return attachOptions{}, err
	}
	if asciiCinemaPlaybackWindow < 0 {
		return attachOptions{}, errors.New("--ascii-cinema-playback-window must be >= 0")
	}
	if asciiCinemaPlaybackSpeed <= 0 {
		return attachOptions{}, errors.New("--ascii-cinema-playback-speed must be > 0")
	}
	return attachOptions{
		PrefixByte:  prefixByte,
		NoReconnect: noReconnect,
		Playback: attachPlaybackConfig{
			Enabled: !noAsciiCinemaPlayback,
			Window:  asciiCinemaPlaybackWindow,
			Speed:   asciiCinemaPlaybackSpeed,
		},
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
func runAttachLoop(initial attachTarget, prefixByte byte, reconnect bool, playback attachPlaybackConfig) (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writers := attachWriters{stdout: os.Stdout, stderr: os.Stderr}
	targets := &attachTargetState{target: initial}
	var attached atomic.Bool
	modeTracker := &terminalModeTracker{}
	defer func() {
		if attached.Load() {
			emitDetach(writers.stdout, targets.current().label, modeTracker)
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
	loopErr := runConnectLoop(ctx, targets, reconnect, &size, shared, &attached, writers, playback, modeTracker)

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
	playback attachPlaybackConfig,
	modeTracker *terminalModeTracker,
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
		sessionPlayback := playback
		if gotConnectedOnce {
			sessionPlayback.Enabled = false
		}
		err = runSession(ctx, conn, c, r, shared, target.label, attached, writers, sessionPlayback, modeTracker)
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
	playback attachPlaybackConfig,
	modeTracker *terminalModeTracker,
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
	playbackEnabled := playback.Enabled
	playbackWindowSeconds := playback.Window.Seconds()
	playbackSpeed := playback.Speed
	helloPayload, _ := json.Marshal(attachwire.Hello{
		Cols:                             cols,
		Rows:                             rows,
		AsciiCinemaPlayback:              &playbackEnabled,
		AsciiCinemaPlaybackWindowSeconds: &playbackWindowSeconds,
		AsciiCinemaPlaybackSpeed:         &playbackSpeed,
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
		serverDone <- runServerLoop(sessionCtx, r, label, attached, writers, modeTracker, func() {
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
//	<prefix> ?       print one-line help on stderr
//	<prefix> c       clone current session and switch to it
func runStdinFSM(ctx context.Context, prefix byte, shared *sharedSession, cancel context.CancelFunc, clone func(context.Context) error, stderr io.Writer) {
	type readResult struct {
		b   byte
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				readCh <- readResult{buf[0], nil}
			}
			if err != nil {
				readCh <- readResult{0, err}
				return
			}
		}
	}()

	const (
		stateNormal    = 0
		stateAfterPref = 1
	)
	state := stateNormal
	for {
		select {
		case <-ctx.Done():
			return
		case rr := <-readCh:
			if rr.err != nil {
				return
			}
			b := rr.b
			switch state {
			case stateNormal:
				if b == prefix {
					state = stateAfterPref
					continue
				}
				shared.sendInputOrWake(b)
			case stateAfterPref:
				switch b {
				case '^':
					// Literal prefix: send if connected, drop if not
					// (no wake — this is data, not a "user is here" signal).
					shared.sendIfConnected(attachwire.MsgInput, []byte{prefix})
				case '.':
					cancel()
					return
				case 0x1a: // Ctrl-Z: suspend self, resume with fg
					_ = syscall.Kill(os.Getpid(), syscall.SIGTSTP)
				case '?':
					fmt.Fprintln(stderr,
						"\r\nhoot attach commands: \".\" detach, \"^\" literal prefix, \"Ctrl-Z\" suspend, \"c\" clone, \"?\" help\r")
				case 'c':
					if err := clone(ctx); err != nil {
						fmt.Fprintf(stderr, "\r\n\x1b[2m[hoot: clone failed: %v]\x1b[0m\r\n", err)
					}
				default:
					// silent ignore
				}
				state = stateNormal
			}
		}
	}
}

// runServerLoop reads frames from the server and dispatches them.
// Snapshot parts are locally phased into connect banner, scrollback,
// viewport clear, and visible screen. Live Output writes straight to
// stdout. Size reports go to stderr. Returns on EOF or protocol error.
func runServerLoop(ctx context.Context, r *bufio.Reader, label attachLabel, attached *atomic.Bool, writers attachWriters, modeTracker *terminalModeTracker, markRecv func()) error {
	connectEmitted := false
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
			if !connectEmitted {
				emitConnect(writers.stdout, label)
				connectEmitted = true
				attached.Store(true)
			}
			if modeTracker != nil {
				if err := modeTracker.pushKittyKbdForSnapshot(writers.stdout, payload); err != nil {
					return err
				}
			}
			if len(payload) > 0 {
				if _, werr := writers.stdout.Write(payload); werr != nil {
					return werr
				}
			}
			if modeTracker != nil {
				modeTracker.observe(payload)
			}
		case attachwire.MsgSnapshotScreen:
			if !connectEmitted {
				emitConnect(writers.stdout, label)
				connectEmitted = true
				attached.Store(true)
			}
			if _, werr := writers.stdout.Write([]byte("\x1b[H\x1b[2J")); werr != nil {
				return werr
			}
			if modeTracker != nil {
				if err := modeTracker.pushKittyKbdForSnapshot(writers.stdout, payload); err != nil {
					return err
				}
			}
			if len(payload) > 0 {
				if _, werr := writers.stdout.Write(payload); werr != nil {
					return werr
				}
			}
			if modeTracker != nil {
				modeTracker.observe(payload)
			}
		case attachwire.MsgOutput:
			if _, werr := writers.stdout.Write(payload); werr != nil {
				return werr
			}
			if modeTracker != nil {
				modeTracker.observe(payload)
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

func emitConnect(w io.Writer, label attachLabel) {
	fmt.Fprintf(w, "\r\n[connected. %s @ %s]\r\n", label.Session, label.Host)
}

// Per-mode terminal-control sequences. Each one is independently safe to
// send: the DECRST forms (`CSI ? <n> l`) and the keyboard protocol
// sequences all use private-use CSI prefixes, so terminals that don't
// implement the mode parse-and-drop the bytes without printing or
// responding.
const (
	// Pop one level of kitty keyboard progressive-enhancement flags.
	// Symmetric with the `CSI > <flags> u` push the inner TUI emitted
	// at startup; without this, keys arrive at the outer shell as
	// `CSI <code>;<mods> u` reports instead of plain bytes.
	seqKittyKbdPop = "\x1b[<u"

	// Push a hoot-owned kitty keyboard stack level before applying
	// libghostty's snapshot state (`CSI = <flags>;1u`). Detach pops
	// this same level, preserving whatever keyboard mode the outer
	// terminal had before attach.
	seqKittyKbdPushEmpty = "\x1b[>u"

	// Disable xterm modifyOtherKeys mode 2. libghostty snapshots emit
	// `CSI > 4;2m` when the remote TUI has this enabled.
	seqModifyOtherKeysOff = "\x1b[>4;0m"

	// Disable focus in/out reporting (DECSET 1004). Some TUIs use
	// FocusIn/Out to refresh state; left on, the outer shell receives
	// `CSI I` / `CSI O` whenever the window gains/loses focus.
	seqFocusEventsOff = "\x1b[?1004l"

	// Disable bracketed paste (DECSET 2004). Left on, pasted text
	// arrives wrapped in `CSI 200~ ... CSI 201~` which readline-style
	// shells handle, but anything else sees the brackets as garbage.
	seqBracketedPasteOff = "\x1b[?2004l"

	// Disable SGR-encoded mouse reports (DECSET 1006). SGR is the
	// modern encoding; pair with the legacy 1000 disable below to
	// cover both styles regardless of which the inner TUI enabled.
	seqMouseSGROff = "\x1b[?1006l"

	// Disable X10/normal mouse reporting (DECSET 1000). Cheap
	// belt-and-suspenders against TUIs that enabled mouse tracking
	// without the SGR extension.
	seqMouseX10Off = "\x1b[?1000l"

	// Leave alternate screen buffer (DECRST 1049). Restores the
	// primary screen so the disconnect banner and the user's shell
	// history are visible again.
	seqAltScreenOff = "\x1b[?1049l"

	// Reset SGR attributes. Belt-and-suspenders in case the TUI was
	// mid-render (bold/inverse/colored) when we tore the pty down.
	seqSGRReset = "\x1b[0m"

	// Show cursor (DECTCEM set). TUIs commonly hide the cursor with
	// `CSI ? 25 l`; without this the outer shell prompt has no caret.
	seqCursorShow = "\x1b[?25h"

	// Cursor home + clear screen. Cosmetic: clears the residual
	// alt-screen contents that briefly flashed back as we left 1049.
	seqClearScreen = "\x1b[H\x1b[2J"
)

// resetTermModes is what hoot writes to the LOCAL tty on detach to
// undo terminal modes the remote TUI may have enabled. Order:
//  1. Modal "I'm a TUI" flags off (idempotent on non-supporting terms)
//  2. Visual reset (alt-screen, SGR, cursor, clear)
//
// Kitty keyboard pops and modifyOtherKeys reset are emitted conditionally
// by terminalModeTracker because they correspond to state we observed or
// intentionally created on the local tty.
const resetTermModes = "" +
	seqFocusEventsOff +
	seqBracketedPasteOff +
	seqMouseSGROff +
	seqMouseX10Off +
	seqAltScreenOff +
	seqSGRReset +
	seqCursorShow +
	seqClearScreen

func emitDetach(w io.Writer, label attachLabel, modeTracker *terminalModeTracker) {
	prefix := ""
	if modeTracker != nil {
		prefix = modeTracker.detachReset()
	}
	fmt.Fprintf(w, "%s%s\r\n[disconnected. %s @ %s]\r\n", prefix, resetTermModes, label.Session, label.Host)
}

type terminalModeTracker struct {
	kittyKbdPopsNeeded  atomic.Int32
	snapshotKittyPushed atomic.Bool
	modifyOtherKeys     atomic.Bool

	csi []byte
}

func (t *terminalModeTracker) pushKittyKbdForSnapshot(w io.Writer, payload []byte) error {
	if !containsKittyKbdSet(payload) {
		return nil
	}
	if !t.snapshotKittyPushed.CompareAndSwap(false, true) {
		return nil
	}
	t.addKittyKbdPop(1)
	_, err := w.Write([]byte(seqKittyKbdPushEmpty))
	return err
}

func (t *terminalModeTracker) observe(payload []byte) {
	for _, b := range payload {
		if len(t.csi) == 0 {
			if b == '\x1b' {
				t.csi = append(t.csi, b)
			}
			continue
		}
		if len(t.csi) == 1 {
			if b == '[' {
				t.csi = append(t.csi, b)
				continue
			}
			if b == '\x1b' {
				t.csi = t.csi[:1]
				continue
			}
			t.csi = t.csi[:0]
			continue
		}

		t.csi = append(t.csi, b)
		if len(t.csi) > 32 {
			t.csi = t.csi[:0]
			continue
		}
		if b >= 0x40 && b <= 0x7e {
			t.observeCSI(t.csi[2:])
			t.csi = t.csi[:0]
		}
	}
}

func (t *terminalModeTracker) observeCSI(seq []byte) {
	if len(seq) < 2 {
		return
	}
	private := seq[0]
	final := seq[len(seq)-1]
	params := seq[1 : len(seq)-1]

	switch {
	case private == '>' && final == 'u':
		t.addKittyKbdPop(1)
	case private == '<' && final == 'u':
		t.removeKittyKbdPop(parseCSIParamDefault(params, 1))
	case private == '>' && final == 'm':
		t.observeModifyOtherKeys(params)
	}
}

func (t *terminalModeTracker) observeModifyOtherKeys(params []byte) {
	if len(params) == 0 {
		t.modifyOtherKeys.Store(false)
		return
	}
	parts := strings.Split(string(params), ";")
	if parts[0] != "4" {
		return
	}
	t.modifyOtherKeys.Store(len(parts) >= 2 && parts[1] == "2")
}

func (t *terminalModeTracker) detachReset() string {
	var reset strings.Builder
	if n := int(t.kittyKbdPopsNeeded.Load()); n > 0 {
		if n == 1 {
			reset.WriteString(seqKittyKbdPop)
		} else {
			fmt.Fprintf(&reset, "\x1b[<%du", n)
		}
	}
	if t.modifyOtherKeys.Load() {
		reset.WriteString(seqModifyOtherKeysOff)
	}
	return reset.String()
}

func (t *terminalModeTracker) addKittyKbdPop(n int) {
	if n <= 0 {
		return
	}
	t.kittyKbdPopsNeeded.Add(int32(n))
}

func (t *terminalModeTracker) removeKittyKbdPop(n int) {
	if n <= 0 {
		return
	}
	for {
		current := t.kittyKbdPopsNeeded.Load()
		next := current - int32(n)
		if next < 0 {
			next = 0
		}
		if t.kittyKbdPopsNeeded.CompareAndSwap(current, next) {
			return
		}
	}
}

func containsKittyKbdSet(payload []byte) bool {
	for i := 0; i+3 < len(payload); i++ {
		if payload[i] != '\x1b' || payload[i+1] != '[' || payload[i+2] != '=' {
			continue
		}
		j := i + 3
		for j < len(payload) && ((payload[j] >= '0' && payload[j] <= '9') || payload[j] == ';') {
			j++
		}
		if j < len(payload) && payload[j] == 'u' {
			return true
		}
	}
	return false
}

func parseCSIParamDefault(params []byte, def int) int {
	if len(params) == 0 {
		return def
	}
	n, err := strconv.Atoi(string(params))
	if err != nil || n <= 0 {
		return def
	}
	return n
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

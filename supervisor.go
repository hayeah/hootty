package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// chdirMu serializes the chdir dance in listenSocket's long-path
// fallback. chdir is process-global; without this, two concurrent
// listenSocket calls from different Runners in the same process
// (common in tests) could interleave their chdirs.
var chdirMu sync.Mutex

// SupervisorConfig configures a Runner. Four fields, no more:
//   - StateDir: the base directory (e.g. ~/.agentboss or .devport).
//   - Key: the session id inside StateDir. The runner owns
//     <StateDir>/<Key>/.
//   - Service: the thing to run. Runner.Run calls Service.Run once
//     and returns whatever it returns.
//   - PTY: the terminal transport (*LibghosttyPTY). Must be
//     non-nil — the Service reaches it via super.PTY().
//
// Everything else (restart policy, briefing replay, signal-specific
// cleanup) is the Service's business.
type SupervisorConfig struct {
	StateDir string
	Key      string
	Service  Service
	PTY      *LibghosttyPTY
}

// Runner is the concrete supervisor entry point. Implements the
// Supervisor interface that Service.Run receives.
type Runner struct {
	cfg    SupervisorConfig
	writer *Writer
	mux    *http.ServeMux
	bus    *eventBus
	log    *slog.Logger
}

// New creates a Runner from the given config. The Runner is not
// useful until you call Run (which acquires the flock, opens the
// socket, and starts the Service).
func New(cfg SupervisorConfig) *Runner {
	return &Runner{
		cfg: cfg,
		mux: http.NewServeMux(),
		bus: newEventBus(),
		log: slog.Default().With("supervisor", cfg.Key),
	}
}

// UpdateState implements Supervisor. Atomically rewrites the `state`
// section of state.json and publishes a "state" event to /events
// subscribers.
func (r *Runner) UpdateState(state any) error {
	if r.writer == nil {
		return errors.New("supervisor: UpdateState called before Run")
	}
	if err := r.writer.UpdateState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err == nil {
		r.bus.Publish(Event{Type: "state", Data: data})
	}
	return nil
}

// PTY implements Supervisor. Returns the terminal transport chosen
// at construction time.
func (r *Runner) PTY() *LibghosttyPTY { return r.cfg.PTY }

// Mux implements Supervisor. The library registers default handlers
// on it (/state, /events); Services may add more during Run.
func (r *Runner) Mux() *http.ServeMux { return r.mux }

// Run is the entire supervisor loop. It:
//  1. Acquires the directory flock (fails if another supervisor owns
//     the key).
//  2. Writes the initial state.json with the supervisor's own PID
//     (so external killers can find us).
//  3. Registers default routes on the mux (/state, /events).
//  4. Opens <StateDir>/<Key>/rpc.sock and serves the mux.
//  5. Installs a signal handler that cancels ctx via
//     context.WithCancelCause with ErrSIG{TERM,INT,HUP}.
//  6. Calls Service.Run(ctx, runner) and returns whatever it returns.
//
// The Service is responsible for spawning its child, monitoring
// exit, restart policy, and graceful shutdown on ctx cancel. The
// library does not orchestrate any of that.
func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.Service == nil {
		return errors.New("supervisor: Service is required")
	}
	if r.cfg.PTY == nil {
		return errors.New("supervisor: PTY is required")
	}
	if r.cfg.StateDir == "" || r.cfg.Key == "" {
		return errors.New("supervisor: StateDir and Key are required")
	}

	stateDir := filepath.Join(r.cfg.StateDir, r.cfg.Key)
	initial := StateFile{
		Supervisor: SupervisorState{
			Key:       r.cfg.Key,
			PID:       os.Getpid(),
			CreatedAt: time.Now(),
		},
	}

	writer, err := OpenWriter(stateDir, initial)
	if err != nil {
		return fmt.Errorf("open writer: %w", err)
	}
	r.writer = writer
	defer writer.Close()
	r.log.Info("acquired flock", "dir", stateDir)

	r.registerDefaultRoutes()
	r.cfg.PTY.RegisterRoutes(r.mux)

	sock, err := r.listenSocket(stateDir)
	if err != nil {
		return fmt.Errorf("listen socket: %w", err)
	}
	defer sock.Close()
	defer r.bus.closeAll()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	sigCh := make(chan os.Signal, 3)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case sig := <-sigCh:
			r.log.Info("received signal", "signal", sig)
			cancel(causeForSignal(sig))
		case <-ctx.Done():
		}
	}()

	return r.cfg.Service.Run(ctx, r)
}

// registerDefaultRoutes wires /state and /events onto the mux.
func (r *Runner) registerDefaultRoutes() {
	r.mux.HandleFunc("/state", r.handleState)
	r.mux.HandleFunc("/events", r.handleEvents)
}

// handleState returns the current StateFile as JSON.
func (r *Runner) handleState(w http.ResponseWriter, _ *http.Request) {
	snap := r.writer.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(&snap); err != nil {
		r.log.Warn("state encode failed", "err", err)
	}
}

// handleEvents serves an SSE stream of library-published events.
// One event type today: "state", emitted from UpdateState.
func (r *Runner) handleEvents(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Replay current state once so a fresh subscriber has something
	// to render.
	snap := r.writer.Snapshot()
	if len(snap.State) > 0 {
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", snap.State)
		flusher.Flush()
	}

	ch, cancel := r.bus.Subscribe()
	defer cancel()

	for {
		select {
		case <-req.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, string(ev.Data))
			flusher.Flush()
		}
	}
}

// listenSocket creates rpc.sock inside stateDir and serves r.mux on
// it. Returns a closer that both stops the listener and waits for
// in-flight handlers to finish.
//
// Long-path handling: unix socket paths go into `struct sockaddr_un`
// whose `sun_path` holds at most 104 bytes on macOS / 108 on Linux.
// State dirs deep inside $TMPDIR or a project's `.devport/...` can
// easily blow past that. Fallback: chdir into the state dir and
// bind a relative path. chdir is process-global, so we gate it on
// a package-level mutex (short critical section: just Chdir +
// Listen + Chdir back).
func (r *Runner) listenSocket(stateDir string) (interface{ Close() error }, error) {
	sockPath := filepath.Join(stateDir, "rpc.sock")
	// Remove any stale socket file from a previous run that didn't
	// shut down cleanly. The flock is what guarantees we're the
	// only writer; the socket file is just a rendezvous point.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		// Fall back to relative-path bind for overlong sun_path.
		// On macOS the syscall surfaces this as EINVAL rather than
		// ENAMETOOLONG, so we just retry on any listen error when
		// the absolute path is over the conservative 100-byte bound.
		if len(sockPath) >= 100 {
			relLn, relErr := listenUnixRelative(stateDir)
			if relErr == nil {
				ln = relLn
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("listen %s: %w", sockPath, err)
		}
	}
	srv := &http.Server{Handler: r.mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.log.Warn("rpc socket serve exited", "err", err)
		}
	}()
	return socketCloser{srv: srv, ln: ln}, nil
}

// listenUnixRelative binds rpc.sock via a temporary chdir into
// stateDir so the kernel only sees the 8-byte relative name. See
// listenSocket for why this is necessary. The returned listener
// behaves identically to a net.Listen("unix", absPath) listener.
func listenUnixRelative(stateDir string) (net.Listener, error) {
	chdirMu.Lock()
	defer chdirMu.Unlock()

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getcwd: %w", err)
	}
	if err := os.Chdir(stateDir); err != nil {
		return nil, fmt.Errorf("chdir %s: %w", stateDir, err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	// A fresh _ = os.Remove in case the absolute-path remove at the
	// caller didn't reach the socket (path length issue there too,
	// but os.Remove uses unlink(2) which accepts PATH_MAX).
	_ = os.Remove("rpc.sock")
	return net.Listen("unix", "rpc.sock")
}

type socketCloser struct {
	srv *http.Server
	ln  net.Listener
}

func (c socketCloser) Close() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.srv.Shutdown(shutdownCtx)
	_ = c.ln.Close()
	return err
}

# supervisor

A small Go library + reference CLI that supervises a single child process.
The child is spawned on a libghostty-backed PTY; its raw output is teed to a
file for replay; and the current screen state is exposed through an
embeddable `http.ServeMux` (plain text / HTML / raw VT).

This is the slimmer cousin of `~/github.com/hayeah/dotfiles/libs/hayeah-go/supervisor`,
extracted so it can stand on its own. The major shift from the dotfiles
version: tmux is no longer a spawn backend. The supervisor opens its own
PTY and runs Ghostty's VT parser against the byte stream
([mitchellh/go-libghostty](https://github.com/mitchellh/go-libghostty)).

## Status

In progress — see `docs/tasks/extract-supervisor-library-with-pty-libghostty-drop-tmux-spawn/spec.md`.

## Build

`libghostty-vt` is a Zig library reachable via cgo + pkg-config. Build it
once locally (clone, `make build`), then point this repo at it via
`PKG_CONFIG_PATH`. The `Makefile` defaults to
`$LIBGHOSTTY/build/_deps/ghostty-src/zig-out/share/pkgconfig` where
`$LIBGHOSTTY` is `~/github.com/mitchellh/go-libghostty` by default.

```sh
make build      # → bin/supervise
make test
```

Prereqs (one-time):

- Zig 0.15.2 (`mise use -g zig@0.15.2` — Zig 0.16 does NOT work)
- CMake
- pkg-config

## Library shape

```go
import "github.com/hayeah/supervisor"

ptyImpl, _ := supervisor.NewLibghosttyPTY(master, cols, rows,
    supervisor.WithRecorder(recorderFile))

run := supervisor.New(supervisor.SupervisorConfig{
    StateDir: ".state",
    Key:      "demo",
    Service:  myService,   // implements supervisor.Service
    PTY:      ptyImpl,
})
err := run.Run(ctx)        // blocks; serves rpc.sock
```

The `Runner.Mux()` http.ServeMux is the public surface. Mount it standalone
(as `supervise` does over a unix socket) or embed it into a larger Go HTTP
server.

### HTTP endpoints contributed by `LibghosttyPTY`

| Path              | Method | Description                                                   |
| ----------------- | ------ | ------------------------------------------------------------- |
| `/pty/text`       | GET    | Plain-text formatter dump. `?scrollback=1` to include history.|
| `/pty/html`       | GET    | HTML fragment dump (libghostty `FormatterFormatHTML`).        |
| `/pty/vt`         | GET    | Self-contained VT replay of the current screen.               |
| `/pty/stream`     | GET    | Chunked binary: VT snapshot + live PTY bytes.                 |
| `/pty/input`      | POST   | Body bytes → PTY master (raw passthrough).                    |
| `/pty/resize`     | POST   | `{cols, rows}` JSON → resize.                                 |

Plus the always-present library routes:

| Path              | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `/state`          | JSON snapshot of `state.json`.                       |
| `/events`         | SSE stream of state-change events.                   |

## `supervise` CLI

```sh
supervise run --state-dir <dir> --key <key> -- <cmd> [args...]
```

Opens a PTY pair, forks an internal supervisor process (with the master
on fd 3 and stdio = slave), serves the mux on `<dir>/<key>/rpc.sock`. The
supervisor holds a flock on `<dir>/<key>/` for the lifetime of the
supervised child.

## Future work (out of scope here)

- An `attach` CLI that replays `<dir>/<key>/pty.log` into a tmux pane (the
  tee recorder is the seam).
- Multi-attachment window-size negotiation.
- Remote HTTP access.

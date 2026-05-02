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
| `/attach`         | GET    | HTTP/1.1 Upgrade → binary attach protocol (see below).        |

Plus the always-present library routes:

| Path              | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `/state`          | JSON snapshot of `state.json`.                       |
| `/events`         | SSE stream of state-change events.                   |

## `supervise` CLI

```sh
supervise run     [--state-dir <dir>] [--key <key>] -- <cmd> [args...]
supervise list    [--state-dir <dir>]
supervise resolve [--state-dir <dir>] <id-or-prefix>
supervise attach  [--state-dir <dir>] [--no-full-replay]
                  [--prefix-key <key>] <id-or-prefix>
supervise serve   [--state-dir <dir>] --port <p> [--addr 127.0.0.1] [--prefix /api]
```

`--state-dir` defaults to `~/.supervise` for every subcommand.

`supervise list` emits JSONL — one line per session, each line a
serialized `supervisor.StateFile` (the same shape `<dir>/<key>/state.json`
holds on disk). Pipe through `jq -s` if you want an array.

`run` opens a PTY pair, forks an internal supervisor process (with the
master on fd 3 and stdio = slave), and serves the mux on
`<dir>/<key>/rpc.sock`. The supervisor holds a flock on `<dir>/<key>/`
for the lifetime of the supervised child.

If `--key` is omitted, `supervise` generates a random short id (3–8
chars drawn from `0-9a-z` minus `l` and `o`, collision-checked against
existing sessions). The chosen key is printed on stdout.

Anywhere a session key is accepted (including `resolve`), you can pass
either the full id or any unique prefix (minimum 3 characters,
case-insensitive). Ambiguous prefixes fail with an error listing the
matching ids; this is what makes the random ids ergonomic to use.

### `supervise attach`

`attach` connects the local terminal to a running supervisor as a
"dumb pipe with two side-channels": stdin → PTY master, master →
stdout, plus a SIGWINCH → resize side-channel. The local termios is
put into raw mode (no echo, no line buffering, no `Ctrl-C` →
local SIGINT) so signal generation happens on the *remote* slave
ldisc, not locally — the user's `Ctrl-C` reaches the supervised
child as expected.

Multiple attaches can drive the same session simultaneously. The
PTY winsize is negotiated as `min(cols)`, `min(rows)` across all
connected attaches; the negotiated size is broadcast back to every
attach as a `Size` frame whenever it changes.

Initial replay modes:

- **full replay (default)** — pump the entire `<dir>/<key>/pty.log`
  to the client before switching to live, race-free at the cutover
  (the recorder offset is captured atomically with subscribing to
  the live tee). Gives the local terminal native scrollback.
- **`--no-full-replay`** — send a libghostty-formatted snapshot of
  the current screen + scrollback as the first frame, then live.
  Faster attach; no native scrollback.

Inside an attach, a tmux-style prefix key introduces commands. The
default is `C-b` (overridable via `--prefix-key C-a`, `--prefix-key
0x1c`, …). Printable bytes are rejected at flag-parse time.

| sequence              | action                                            |
| --------------------- | ------------------------------------------------- |
| `<prefix> d`          | detach (clean exit 0)                             |
| `<prefix> ?`          | print one-line help on stderr, stay attached     |
| `<prefix> <prefix>`   | send literal prefix byte to remote                |

Exit codes: `0` clean detach, `1` protocol/dial error, `2` argument
error, `130` terminated by SIGINT/SIGTERM/SIGHUP we trapped.

### `supervise serve`

`serve` is a fan-out HTTP server over the same `--state-dir`. It
exposes a session-keyed REST + WebSocket surface that an external
UI can drive without dialing each session's `rpc.sock` directly
(the browser cannot speak unix-socket).

```sh
supervise serve --port 8080 --state-dir ~/.supervise [--prefix /api]
```

Routes (mounted at the bare path and — if `--prefix` is set — at
`<prefix><path>` too, so the same binary works behind a
`strip_prefix=false` proxy and on direct probing):

| Method | Path                        | Behaviour                                                        |
|--------|-----------------------------|------------------------------------------------------------------|
| GET    | `/sessions`                 | `{sessions: [{...StateFile, alive}]}`                            |
| POST   | `/sessions`                 | `{cmd \| argv, key?}` → forks `supervise __supervise`            |
| GET    | `/sessions/{key}`           | state.json (alias of `/state`)                                  |
| DELETE | `/sessions/{key}`           | SIGTERM the supervisor by pid (204)                              |
| GET    | `/sessions/{key}/state`     | state.json                                                       |
| GET    | `/sessions/{key}/events`    | SSE proxy of upstream `/events`                                  |
| GET    | `/sessions/{key}/attach`    | WebSocket bridge to upstream `/attach`                           |
| GET    | `/healthz`                  | liveness                                                         |

The WebSocket bridge speaks browser-friendly framing —
binary frames carry PTY bytes both directions, and a text frame
`{"type":"resize","cols":N,"rows":N}` is translated into an
`attachwire.MsgSize` upstream — and terminates the
`supervise-attach/1` HTTP-Upgrade protocol on the rpc.sock side.

### Example dashboard

`packages/supervisor-webui/` is an example React app that renders
the session list and a live terminal pane against `supervise
serve`. To run it locally with vite + the API behind a single
proxy port, point [`devportv3`](https://github.com/hayeah/devportv3)
at the included `devport.toml`:

```sh
make build           # → bin/supervise
pnpm install
devport up           # foreground; or `--daemon` for background
# proxy URL is printed in the up summary
```

The `@hayeah/supervisor-termui` package extracts the terminal
widget (ghostty-web canvas + AttachStream byte-pipe contract) so
it can be reused in other dashboards.

## Future work (out of scope here)

- A `--render` mode for `attach` that embeds libghostty client-side
  and composites the child's virtual screen into a sub-region of
  the local terminal (so per-attach winsize can diverge from the
  negotiated min).
- Reconnect / resume.
- Remote HTTP access.

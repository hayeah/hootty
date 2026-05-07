# hootty

A small Go library + reference CLI that manages a single child process.
The child is spawned on a libghostty-backed PTY; its output is recorded as an
asciicast v2 JSONL file for replay; and the current screen state is exposed through an
embeddable `http.ServeMux` (plain text / HTML / raw VT).

Project home: <https://hootty.dev>.

This is the slimmer cousin of `~/github.com/hayeah/dotfiles/libs/hayeah-go/session`,
extracted so it can stand on its own. The major shift from the dotfiles
version: tmux is no longer a spawn backend. The session opens its own
PTY and runs Ghostty's VT parser against the byte stream
([mitchellh/go-libghostty](https://github.com/mitchellh/go-libghostty)).

## Status

In progress — see `docs/tasks/extract-hootty-library-with-pty-libghostty-drop-tmux-spawn/spec.md`.

## Build

`libghostty-vt` is a Zig library reachable via cgo + pkg-config. Build it
once locally (clone, `make build`), then point this repo at it via
`PKG_CONFIG_PATH`. The `Makefile` defaults to
`$LIBGHOSTTY/build/_deps/ghostty-src/zig-out/share/pkgconfig` where
`$LIBGHOSTTY` is `~/github.com/mitchellh/go-libghostty` by default.

```sh
make build      # → bin/hoot
make test
```

To build a Linux `amd64` binary from macOS, see
[`docs/cross-compile.md`](docs/cross-compile.md).

Prereqs (one-time):

- Zig 0.15.2 (`mise use -g zig@0.15.2` — Zig 0.16 does NOT work)
- CMake
- pkg-config

## Library shape

```go
import session "github.com/hayeah/hootty"

ptyImpl, _ := session.NewLibghosttyPTY(master, cols, rows,
    session.WithRecorder(recorderFile))

run := session.New(session.SessionConfig{
    StateDir: ".state",
    Key:      "demo",
    Service:  myService,   // implements session.Service
    PTY:      ptyImpl,
})
err := run.Run(ctx)        // blocks; serves rpc.sock
```

The `Runner.Mux()` http.ServeMux is the public surface. Mount it standalone
(as `hoot` does over a unix socket) or embed it into a larger Go HTTP
server.

### HTTP endpoints contributed by `LibghosttyPTY`

| Path              | Method | Description                                                   |
| ----------------- | ------ | ------------------------------------------------------------- |
| `/pty/text`       | GET    | Plain-text formatter dump. `?scrollback=1` to include history.|
| `/pty/html`       | GET    | HTML fragment dump (libghostty `FormatterFormatHTML`).        |
| `/pty/vt`         | GET    | Self-contained VT replay of the current screen.               |
| `/pty/stream`     | GET    | Chunked binary: VT snapshot + live PTY bytes.                 |
| `/pty/input`      | POST   | Body bytes → PTY master. `?paste=on` wraps in DEC 200~/201~ (409 if receiver mode disabled). 1 MiB cap. |
| `/pty/resize`     | POST   | `{cols, rows}` JSON → resize.                                 |
| `/attach`         | GET    | HTTP/1.1 Upgrade → binary attach protocol (see below).        |

Plus the always-present library routes:

| Path              | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `/state`          | JSON snapshot of `state.json`.                       |
| `/events`         | SSE stream of state-change events.                   |

## `hoot` CLI

```sh
hoot run     [--remote <url>] [--state-dir <dir>] [--key <key>] [--attach]
                  [--no-reconnect] [--prefix-key <key>] -- <cmd> [args...]
hoot list    [--remote <url>] [--state-dir <dir>]
hoot resolve [--remote <url>] [--state-dir <dir>] <id-or-prefix>
hoot clone   [--remote <url>] [--state-dir <dir>] [--key <new-key>]
                  [--env NAME[=VALUE]] [--env-file <path>]
                  [--attach|--detach] <id-or-prefix>
hoot attach  [--remote <url>] [--state-dir <dir>] [--no-reconnect]
                  [--prefix-key <key>] [--strict] [<id-or-pattern>]
hoot kill    [--remote <url>] [--state-dir <dir>] [-s <signal>] <id-or-prefix>
hoot detach  [--remote <url>] [--state-dir <dir>] <session-prefix>[/<attachment-prefix>]
hoot write   [--remote <url>] [--state-dir <dir>] [--paste] [--input-file FILE]
                  <id-or-prefix> [DATA...]
hoot serve   [--state-dir <dir>] --bind <host:port|unix:/path.sock> [--prefix /api]
```

`--state-dir` defaults to `~/.hoot` for every subcommand.

`--remote` targets another host for session-facing subcommands:

- `host`, `host:port`, `user@host`, `user@host:port`, or
  `ssh://user@host:port` shell out to OpenSSH, start a per-invocation
  remote `hoot serve --bind unix:<sock>`, forward a local Unix socket
  to it, and then use the same HTTP API. A bare destination with no
  URL scheme defaults to `ssh://`, so `hoot run --remote m4mini -- bash -l`
  Just Works.
- `http://host:port`, `https://host:port` talk to a running `hoot serve`
  directly over HTTP.

The ssh transport inherits the user's OpenSSH config, agent,
ProxyJump, hardware-key, and ControlMaster behavior. `hoot` uses a
stable hashed `ControlPath` for each `user@host:port`, so a long-running
remote attach keeps the OpenSSH master connection warm and later
one-shot commands to the same target reuse that master transparently.
Those later commands still create their own short-lived mux client,
forward socket, and remote `hoot serve`, but they do not need a fresh
SSH handshake/authentication while the master is alive. `hoot` must be
in the remote login shell's `PATH`; no persistent remote server is
required.

`hoot list` emits JSONL — one line per session, each line a
serialized `session.StateFile` (the same shape `<dir>/<key>/state.json`
holds on disk). Pipe through `jq -s` if you want an array.

`run` opens a PTY pair, forks an internal session process (with the
master on fd 3 and stdio = slave), and serves the mux on
`<dir>/<key>/rpc.sock`. The session holds a flock on `<dir>/<key>/`
for the lifetime of the managed child. New state files include
`session.argv` and `session.cwd`; remove old state dirs when upgrading
across this schema change.

Each session directory also contains `pty.cast`, an asciicast v2 JSONL
recording with a header line followed by output (`"o"`) and resize
(`"r"`) events. It is valid input for `asciinema play`, `agg`, and
`asciinema-player`. Input (`"i"`) events are not recorded.

If `--key` is omitted, `hoot` generates a random short id (3–8
chars drawn from `0-9a-z` minus `l` and `o`, collision-checked against
existing sessions). The chosen key is printed on stdout.

Pass `--attach` to `run` to attach the current terminal to the new
session immediately after spawn readiness. Detaching with
`<prefix>.` leaves the session running in the background, the same as
running `hoot run -- ...` followed by `hoot attach <key>`. With
`--remote`, the session is spawned through the remote `hoot serve`
transport and the attach phase uses that same transport. The attach
phase accepts the same prefix-key and reconnect flags as
`hoot attach`; `--no-reconnect` only affects remote attach.

Anywhere a session key is accepted (including `resolve`), you can pass
either the full id or any unique prefix (minimum 3 characters,
case-insensitive). Ambiguous prefixes fail with an error listing the
matching ids; this is what makes the random ids ergonomic to use.

### `hoot clone`

`clone` spawns a fresh sibling session in the same state dir using the
source session's recorded `session.argv` and `session.cwd`. It does not
fork the running child and does not copy a live PTY; it repeats the
original spawn now.

```sh
hoot clone abc
hoot clone --key work2 abc
hoot clone --attach abc
hoot clone --env FEATURE=on --env-file .env.clone abc
hoot clone --remote ssh://devbox abc
```

The cloned process inherits from the new `hoot __session` environment
at clone time and then goes through the usual child env cleanup
(`TMUX`/`TMUX_PANE` stripped, `TERM=xterm-256color`). `--env NAME=VALUE`
sets a one-shot override for the clone; `--env NAME` copies `NAME` from
the cloning CLI process; `--env-file` loads simple `NAME=VALUE` lines.
Overrides are not persisted to state and are not replayed by
clone-of-clone unless passed again.

Bare `hoot clone` is detached by default and prints the new key on
stdout, matching `hoot run`. `--attach` switches into the cloned
session after creation.

### `hoot kill`

`kill` sends a Unix signal to the foreground process group of a session's
PTY — the same target the kernel uses for keyboard-generated signals like
Ctrl-C. For an interactive shell session this routes the signal to whatever
job the user has in the foreground; for a non-interactive child it lands
on that child directly.

```sh
hoot kill abc                  # send SIGTERM (default)
hoot kill -s INT abc           # interrupt the foreground job
hoot kill -s KILL abc          # force-kill; supervisor tears down
hoot kill -s USR1 abc          # arbitrary signal by name
hoot kill -s 9 abc             # or by number
hoot kill --remote ssh://devbox abc
```

Mechanism: the CLI POSTs `{"signal":"TERM"}` to `/signal` on the session's
`rpc.sock` (local) or to `/sessions/{key}/signal` on a `hoot serve`
multiplexer (remote, `--remote`). The supervisor reads the PTY's
`tcgetpgrp` and delivers `kill(-fgpgid, sig)`; if no foreground process
group is reported it falls back to signaling the supervised child PID.

Signal names accept the standard POSIX set with or without the `SIG`
prefix (case-insensitive), or a small positive integer.

Exit codes: `0` delivered, `1` I/O error, `2` usage / unknown signal,
`3` no such session or ambiguous prefix, `5` session exists but child
not running.

### `hoot detach`

`detach` force-closes one or all attachments on a session — the
admin counterpart to `<prefix>.` from inside an attach. The
positional argument is either a bare session prefix (close every
attachment on that session) or a `<sess>/<att>` pair (close one
specific attachment). Both halves accept full ids or unique
prefixes.

```sh
hoot detach abc                # close every attachment on session "abc"
hoot detach abc/x9k            # close just attachment "x9k" on "abc"
hoot detach --remote ssh://devbox abc/x9k
```

Each attachment carries a short id minted at Hello time and stored
in `state.json` under `session.attachments[]` along with the
client-reported `cols×rows` and best-effort origin metadata
(`host`, `term`, `term_program`, `term_program_version`, `user`).
`hoot list` surfaces the same data so you can pick the attachment
to target. The id namespace is per-session: two sessions can
coincidentally mint the same short id without conflict, which is
why the CLI requires the session-qualified `<sess>/<att>` form.

Mechanism: the CLI issues `DELETE /attachments` (close-all) or
`DELETE /attachments/{id}` on the session's `rpc.sock` (local) or
the equivalent route on a `hoot serve` multiplexer (remote). The
supervisor closes the underlying conn after pushing a final yellow
`[detached by hoot detach]` notice through the wire, so the user
on the detached terminal sees a reason rather than a silent EOF.

Exit codes: `0` detach delivered, `1` I/O error, `2` usage error,
`3` no such session/attachment or ambiguous prefix, `5` session
exists but is not alive.

### `hoot write`

`write` sends bytes to the supervised PTY's master FD without
attaching. Useful for agents driving a session from outside, scripted
input, and one-shot commands.

```sh
hoot write sess "ls -l\r"                   # type a command and run it
hoot write sess "\x03"                      # Ctrl-C
hoot write sess "\x1b[B\x1b[B\r"            # Down, Down, Enter (TUI menu)
echo -n "data" | hoot write sess            # pipe stdin verbatim
hoot write sess --input-file script.sh      # send file contents verbatim
cat patch.diff | hoot write sess --paste    # safe paste (review, then \r)
hoot write --remote http://lab:8484 sess "y\r"
```

Input source precedence:

- `--input-file FILE` — bytes from `FILE`, **verbatim, no escape parsing**.
- `DATA...` positional — concatenated with no separator, then run
  through Go's string-literal interpreter (`strconv.Unquote`).
  You get exactly Go's `"…"` grammar: `\xHH`, `\uXXXX`, `\UXXXXXXXX`,
  `\NNN` octal, `\a \b \f \n \r \t \v`, `\\`, `\"`, `\'`. Anything
  else is a parse error.
- Stdin (when not a TTY) — bytes verbatim, no escape parsing.

The argv-vs-stdin asymmetry mirrors `printf` (interpret-argv) vs
`cat` (verbatim-stdin). If you want escape interpretation on a piped
file, run `printf` upstream first.

`--paste` wraps the bytes in DEC bracketed-paste markers
(`\e[200~ … \e[201~`) so the receiver collects them as one logical
paste rather than executing each embedded `\r`/`\n`. The supervisor
checks `term.ModeGet(ModeBracketedPaste)` first; if the receiving
program does not have DECSET 2004 enabled, the request returns 409
and the CLI exits 2 with `bracketed paste not enabled on receiver`
— **no silent fallback to raw bytes**. Payloads containing the
end marker (`\e[201~`) are rejected with 400.

Cheat sheet (`hoot write --help` carries the same):

```
Enter      \r            Tab        \t
Backspace  \x7f          Escape     \x1b   (NOTE: no \e — Go grammar)
Ctrl-A..Z  \x01..\x1a    Ctrl-C     \x03
Up         \x1b[A        Down       \x1b[B
Right      \x1b[C        Left       \x1b[D
Home       \x1b[H        End        \x1b[F
PageUp     \x1b[5~       PgDn       \x1b[6~
F1..F4     \x1bOP \x1bOQ \x1bOR \x1bOS
F5..F12    \x1b[15~ \x1b[17~ ... \x1b[24~
```

Multi-step flows compose in shell — `write` is a primitive, not an
expect harness:

```sh
hoot write sess "/help\r"
sleep 0.2
hoot text sess | grep -q "Available commands" || exit 1
hoot write sess "submit\r"
```

Mechanism: `POST /pty/input` on the session's `rpc.sock` (local) or
`POST /sessions/<key>/input` on a `hoot serve` multiplexer (remote).
Body is the bytes to send; the optional `?paste=on` query toggles
the bracketed-paste wrap. A `writeMu` in the supervisor serializes
the underlying `master.Write` so concurrent attaches and `hoot write`
calls cannot interleave at the byte level. Body cap is 1 MiB.

Exit codes: `0` write delivered, `1` I/O / unexpected server error,
`2` usage / parse error / paste-not-enabled / payload contains
`\e[201~` / body too large, `3` no such session or ambiguous prefix.

### `hoot attach`

#### Picker (no-args / fuzzy fallback)

`hoot attach` with no positional argument launches an interactive
fzf picker over the available sessions. The argv form is:

```
hoot attach                       # picker over all sessions
hoot attach <id-prefix>           # exact id-prefix → attach (today's behavior)
hoot attach <pattern>             # id-prefix first; on miss, fzf with --query=<pattern>
                                  # --select-1 --exit-0 (unique fuzzy hit auto-attaches)
hoot attach --strict <id-prefix>  # id-prefix only — no fzf, no picker (scripts)
```

Picker line format (tab-separated; the matcher sees everything from
`[id]` rightward — the leading 1-based index column is hidden via
`--with-nth=2..`):

```
1  [a3fxxx]  @local      ~/proj/api          bash --login          *attached
2  [k7qzzz]  @m4mini     ~/Dropbox/notes     nvim spec.md
3  [zz9aaa]  @local      ~/code/hootty       go test ./...         (dead)
```

Field markers double as natural fzf query prefixes:

| query           | scope                                                |
| --------------- | ---------------------------------------------------- |
| `m4mini`        | substring anywhere                                   |
| `@m4`           | host (since `@` only appears before the host token)  |
| `[a3`           | id prefix                                            |
| `*`             | live attached sessions only                          |
| `'nvim`         | exact word-prefix match (fzf disables fuzzy)         |
| `^[a3fxxx]`     | exact id (line-anchored)                             |
| `!notes`        | negate                                               |
| `nvim \| vim`   | OR                                                   |

Sessions are sorted by spawn time ascending so the empty-query view
shows the oldest first; `--no-sort` keeps that order until the user
types something. Up/Down moves the cursor, Enter attaches, Esc / Ctrl-C
cancels (exit 130).

The picker requires `fzf` on `PATH`; missing fzf prints a one-line
install hint and exits 2. `--strict` bypasses the picker entirely
for scripted use.

For `--remote`, the picker runs **client-side**: hoot fetches the
session list from the remote `hoot serve` via `GET /sessions`, and
fzf runs locally on the user's machine — the remote box does not
need fzf installed.

#### Pipe semantics

`attach` connects the local terminal to a running session as a
"dumb pipe with two side-channels": stdin → PTY master, master →
stdout, plus a SIGWINCH → resize side-channel. The local termios is
put into raw mode (no echo, no line buffering, no `Ctrl-C` →
local SIGINT) so signal generation happens on the *remote* slave
ldisc, not locally — the user's `Ctrl-C` reaches the managed
child as expected.

Multiple attaches can drive the same session simultaneously. The
PTY winsize is negotiated as `min(cols)`, `min(rows)` across all
connected attaches; the negotiated size is broadcast back to every
attach as a `Size` frame whenever it changes.

Initial replay is phased. The server sends two libghostty-formatted
snapshot frames: `MsgSnapshotScrollback` for history, then
`MsgSnapshotScreen` for the visible viewport plus cursor position,
active SGR style, and non-default modes. The client writes a plain
`[connected. <session> @ <host>]` banner, prints the scrollback into
local history, clears only the local viewport with `ESC[H ESC[2J`,
then paints the visible screen and switches to live `MsgOutput`
frames. This keeps pre-attach local scrollback intact while preventing
stale local viewport rows from bleeding through short remote screens.

If the child is currently on an alternate screen (tmux, vim, less,
etc.), attach currently preserves the legacy full-snapshot fallback in
the visible-screen phase. That builds both screen buffers in the
attaching terminal, so the later live alternate-screen exit restores
the recorded primary scrollback rather than the attach client's
previously empty local primary. The snapshot subscribe pair is
race-free — both happen on the dispatcher goroutine, so live chunks
delivered after subscribe carry no overlap with the snapshot.

On attach, the server snapshot restores terminal mode state, including
kitty keyboard and modify-other-keys state for TUIs that need richer
key reports. If a snapshot applies kitty keyboard state, the client
first creates a local stack frame so detach can pop only the frame hoot
owns. On detach, the client locally unwinds observed kitty keyboard and
modify-other-keys state, disables focus events, bracketed paste, and
mouse tracking, then leaves alt screen defensively, resets SGR, shows
the cursor, clears only the viewport, and prints
`[disconnected. <session> @ <host>]` before exiting. None of these
cleanup bytes are written to the remote PTY or other attached clients.

Attach restores recent context via the libghostty snapshot — the
emulator's parsed grid (scrollback + visible viewport, with cursor,
SGR, and mode state) is serialized as VT-replayable bytes and
repainted on the attaching terminal. The asciicast playback path
that used to re-stream raw recorded bytes after the snapshot was
removed: it visibly re-animated past TUI frames (cursor moves,
alt-screen toggles, in-place updates) on top of an already-correct
snapshot. The recorder still writes `pty.cast` to disk for offline
tooling.

The previous `--no-full-replay` flag and the alternative `pty.log`
replay arm were also removed. Replaying raw recorded bytes re-issued
every terminal-query escape the child ever sent (`DA`, `DSR`, `OSC
11 ?`, …) which the user's real terminal would dutifully answer back
into the child's stdin — the same root cause as the live "junk chars
after tmux detach" symptom, just spread across the whole session.

Live fanout strips terminal-query escape sequences before sending
to the user's real terminal (the libghostty emulator on the
session already auto-replies, so a second answer from the user's
terminal would inject duplicate bytes into the child's stdin). The
recorder + emulator branch sees raw bytes; only the user-facing
fanout is filtered. See `vtquery_stripper.go`.

Inside an attach, a mosh-style prefix key introduces commands. The
default is `C-^` (Ctrl-^, 0x1e — same as mosh; chosen so it does not
collide with tmux's `C-b`). Override via `--prefix-key C-a`,
`--prefix-key 0x1c`, …; printable bytes are rejected at flag-parse time.

| sequence              | action                                            |
| --------------------- | ------------------------------------------------- |
| `<prefix> .`          | detach (clean exit 0)                             |
| `<prefix> ^`          | send literal prefix byte to remote                |
| `<prefix> Ctrl-Z`     | suspend `hoot attach` (SIGTSTP; `fg` resumes) |
| `<prefix> c`          | clone current session and attach to the clone     |
| `<prefix> ?`          | print one-line help on stderr, stay attached      |

Exit codes: `0` clean detach, `1` protocol/dial error, `2` argument
error (or `404`/`409` from a remote `attach-raw`), `130` terminated by
SIGINT/SIGTERM/SIGHUP we trapped.

#### Remote sessions (`--remote`)

With `--remote http://host:port`, `list`, `resolve`, `run`, and
`attach` talk to a running `hoot serve` instead of the local state dir.

A `--remote` value with no URL scheme is treated as `ssh://` — so
`--remote m4mini`, `--remote me@m4mini`, and `--remote me@m4mini:2222`
all dial over ssh. Use an explicit `http://` / `https://` prefix to
hit a running `hoot serve` directly.

With `--remote ssh://host` (or any bare host shorthand), the local CLI
starts a short-lived remote `hoot serve` over OpenSSH and forwards a
local Unix socket to it. The
remote only needs `hoot` in `PATH`; the user does not start `hoot serve`
by hand. OpenSSH ControlMaster reuse is opportunistic: if another
`hoot` process, such as a long-running remote `attach`, already has a
healthy master connection for the same `user@host:port`, a new `list`,
`resolve`, `run`, or `clone` opens a new mux channel over that master.
If no master exists, the one-shot command opens one itself and still
works standalone.

```sh
hoot list --remote ssh://devbox
hoot run --remote ssh://devbox -- bash
hoot resolve --remote ssh://devbox abc
hoot attach --remote ssh://devbox abc

hoot attach --remote m4mini:20000 abc          # bare host:port
hoot attach --remote http://m4mini:20000 abc   # explicit scheme
hoot attach --remote https://m4mini:443  abc   # TLS
```

Auto-reconnect (default on `--remote` attach, opt out with
`--no-reconnect`):

- Backoff: 1s, 2s, 4s, 8s, 16s, 30s — capped at 30s, retries forever.
- A status line on stderr counts down each tier in place: `[hoot:
  reconnecting in 5s — press any key to retry now]`. Pressing any
  non-prefix key wakes the backoff and retries immediately.
- Termios stays raw across drops. On reconnect the client sends a fresh
  `Hello` with the current cols/rows; the server snapshot repaints the
  screen so the disconnect line is overwritten naturally.
- `<prefix>.` detaches cleanly even mid-disconnect.
- A 404 (no session matched) or 409 (ambiguous prefix) on the upgrade
  response surfaces as exit 2 with the server's message.

### `hoot serve`

`serve` is a fan-out HTTP server over the same `--state-dir`. It
exposes a session-keyed REST + WebSocket surface that an external
UI can drive without dialing each session's `rpc.sock` directly
(the browser cannot speak unix-socket).

```sh
hoot serve --bind 127.0.0.1:8080 --state-dir ~/.hoot [--prefix /api]
hoot serve --bind unix:$HOME/.hoot/api.sock --state-dir ~/.hoot
```

Routes (mounted at the bare path and — if `--prefix` is set — at
`<prefix><path>` too, so the same binary works behind a
`strip_prefix=false` proxy and on direct probing):

| Method | Path                        | Behaviour                                                        |
|--------|-----------------------------|------------------------------------------------------------------|
| GET    | `/sessions`                 | `{sessions: [{...StateFile, alive}]}`                            |
| POST   | `/sessions`                 | `{cmd \| argv, key?}` → forks `hoot __session`            |
| GET    | `/sessions/{key}`           | state.json (alias of `/state`)                                  |
| POST   | `/sessions/{key}/clone`     | `{key?, env?}` → forks a sibling from `session.argv`/`cwd`       |
| GET    | `/sessions/{key}/resolve`   | resolve full id or unique prefix                                |
| DELETE | `/sessions/{key}`           | SIGTERM the session by pid (204)                              |
| GET    | `/sessions/{key}/state`     | state.json                                                       |
| GET    | `/sessions/{key}/events`    | SSE proxy of upstream `/events`                                  |
| GET    | `/sessions/{key}/attach`    | WebSocket bridge to upstream `/attach` (browser)                 |
| GET    | `/sessions/{key}/attach-raw`| HTTP/1.1 Upgrade pass-through to upstream `/attach` (CLI)        |
| GET    | `/healthz`                  | liveness                                                         |

The WebSocket bridge (`/attach`) speaks browser-friendly framing —
binary frames carry PTY bytes both directions, and a text frame
`{"type":"resize","cols":N,"rows":N}` is translated into an
`attachwire.MsgSize` upstream — and terminates the
`hoot-attach/1` HTTP-Upgrade protocol on the rpc.sock side.

The Upgrade pass-through (`/attach-raw`) is for the CLI: it resolves
`{key}` (full id or unique prefix) server-side, hijacks the client
connection, and `io.Copy`s bytes both ways with no transcoding. The
client speaks the same `hoot-attach/1` framing it uses against a
local `rpc.sock`. 404 on no-match, 409 on ambiguous prefix.

### Example dashboard

`packages/hootty-webui/` is an example React app that renders
the session list and a live terminal pane against `hoot
serve`. To run it locally with vite + the API behind a single
proxy port, point [`devportv3`](https://github.com/hayeah/devportv3)
at the included `devport.toml`:

```sh
make build           # → bin/hoot
pnpm install
devport up           # foreground; or `--daemon` for background
# proxy URL is printed in the up summary
```

The `@hayeah/hootty-termui` package extracts the terminal
widget (ghostty-web canvas + AttachStream byte-pipe contract) so
it can be reused in other dashboards.

## Future work (out of scope here)

- A `--render` mode for `attach` that embeds libghostty client-side
  and composites the child's virtual screen into a sub-region of
  the local terminal (so per-attach winsize can diverge from the
  negotiated min).

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
hoot                                 # spawn local default shell, attach
hoot @<host>                         # spawn shell on remote (ssh://<host>), attach
hoot <host>                          # same as @<host> (subcommand fallthrough)

hoot run     [--remote <url>] [--state-dir <dir>] [--key <key>] [--attach]
                  [--no-reconnect] [--prefix-key <key>]
                  [--cwd <path>] [--env NAME[=VALUE]] [--env-file <path>]
                  -- <cmd> [args...]
hoot list    [--remote <url>] [--state-dir <dir>] [--json] [--all]
hoot resolve [--remote <url>] [--state-dir <dir>] <id-or-prefix>
hoot clone   [--remote <url>] [--state-dir <dir>] [--key <new-key>]
                  [--env NAME[=VALUE]] [--env-file <path>]
                  [--attach|--detach] [--strict] [<id-or-pattern>]
hoot attach  [--remote <url>] [--state-dir <dir>] [--no-reconnect]
                  [--prefix-key <key>] [--strict] [--restorer <name>] [<id-or-pattern>]
hoot kill    [--remote <url>] [--state-dir <dir>] [-s <signal>] [--strict] [<id-or-pattern>]
hoot detach  [--remote <url>] [--state-dir <dir>] [--strict] [<session-prefix>[/<attachment-prefix>]]
hoot write   [--remote <url>] [--state-dir <dir>] [--paste] [--input-file FILE]
                  <id-or-prefix> [DATA...]
hoot serve   [--state-dir <dir>] --bind <host:port|unix:/path.sock> [--prefix /api]
```

### Quick shell

`hoot` with no arguments spawns the user's default shell (resolved
via `$SHELL`, falling back to `/bin/sh`) and attaches immediately.
It's the bare-hands form for "open me a session here":

```sh
hoot                              # local zsh/bash/whatever, attached
hoot @m4mini                      # ssh://m4mini, remote shell, attached
hoot me@m4mini:2222               # user@host:port also works as @host
hoot m4mini                       # subcommand fallthrough — same as @m4mini
hoot @m4mini --no-reconnect       # flags pass through after the host
```

`@<host>` is sugar for `--remote ssh://<host>`. The fallthrough form
(`hoot m4mini`) only triggers when the first positional argument
isn't a known subcommand — `hoot list`, `hoot run …`, etc. keep their
existing meanings. For remote shells the empty argv is sent across
the wire and the remote `hoot serve` resolves its own `$SHELL`, so
`hoot @m4mini` opens whatever the remote user's login shell is, not
the caller's.

The shell is invoked bare (no `-l`, no `-i`) — bash and zsh
auto-promote to interactive on a tty, and skipping `-l` avoids
re-running profile files that the parent already loaded. Use
`hoot run -- bash -l` if you need a true login shell.

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

`hoot list` prints one human-readable line per **live** session by
default — the same line shape the fzf picker matches against, so
pattern intuition is shared across `hoot ls`, `hoot attach <pat>`,
`hoot kill <pat>`, etc.:

```
[<id>]	@<host>	<cwd>	<argv...>	[*attached|(dead)]
```

Fields are tab-separated; `<host>` is `local` for local sessions or
the literal `--remote` target string. The trailing tag is `*attached`
when at least one client is connected, `(dead)` for sessions whose
flock probe fails (only visible with `--all`), and empty otherwise.

Flags:

- `--all` — also list exited sessions (rendered with the `(dead)` tag).
- `--json` — emit JSONL of `session.StateFile` instead of pretty
  lines (one line per session, same shape `<dir>/<key>/state.json`
  holds on disk). Pipe through `jq -s` if you want an array. Honors
  `--all` the same way as the default output: alive only by default,
  add `--all` for dead too.

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

`--cwd <path>` overrides the working directory for the spawned command;
relative paths are resolved against the caller's cwd (or the server's
cwd, for `--remote`). `--env NAME=VALUE` sets a one-shot override;
`--env NAME` copies `NAME` from the calling process's environment;
`--env-file <path>` loads simple `NAME=VALUE` lines (`#` comments and
blank lines ignored). Both `--env` and `--env-file` are repeatable; they
match `hoot clone`'s flag shape so a clone can be reproduced as a fresh
`run` with the same overrides.

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
hoot clone                       # picker over all sessions
hoot clone abc
hoot clone --key work2 abc
hoot clone --attach abc
hoot clone --env FEATURE=on --env-file .env.clone abc
hoot clone --remote ssh://devbox abc
hoot clone --strict abc          # id-prefix only — no fzf, no picker (scripts)
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

The positional id selecting the source session shares its routing
matrix with `hoot attach`: no arg + tty drops into an fzf picker; a
pattern that doesn't id-prefix-match falls back to fzf preseeded with
`--query=<arg> --select-1 --exit-0`; non-tty contexts require an
unambiguous id (or `--strict`). See `hoot attach -h` for the picker
line format and fzf query vocabulary.

### `hoot kill`

`kill` sends a Unix signal to the foreground process group of a session's
PTY — the same target the kernel uses for keyboard-generated signals like
Ctrl-C. For an interactive shell session this routes the signal to whatever
job the user has in the foreground; for a non-interactive child it lands
on that child directly.

```sh
hoot kill                      # picker over all sessions
hoot kill abc                  # send SIGTERM (default)
hoot kill -s INT abc           # interrupt the foreground job
hoot kill -s KILL abc          # force-kill; supervisor tears down
hoot kill -s USR1 abc          # arbitrary signal by name
hoot kill -s 9 abc             # or by number
hoot kill --remote ssh://devbox abc
hoot kill --strict abc         # id-prefix only — no fzf, no picker (scripts)
```

The positional id selecting the target session shares its routing
matrix with `hoot attach` — no arg + tty drops into the picker; a
pattern that doesn't id-prefix-match falls back to fzf preseeded; use
`--strict` to skip fzf entirely. See `hoot attach -h` for the picker
line format and fzf query vocabulary.

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
hoot detach                    # picker over all sessions; close-all on the picked session
hoot detach abc                # close every attachment on session "abc"
hoot detach abc/x9k            # close just attachment "x9k" on "abc"
hoot detach --remote ssh://devbox abc/x9k
hoot detach --strict abc       # id-prefix only — no fzf, no picker (scripts)
```

The session-prefix half participates in the same picker routing as
`hoot attach`: no arg + tty drops into the picker; a bare `<sess>`
that doesn't id-prefix-match falls back to fzf preseeded; `--strict`
disables both. The `<attachment-prefix>` half (after the slash) keeps
the strict shortid-prefix semantics — attachment ids are short
generated random ids you copy from `hoot list`, not fuzzy-matchable
terms, so they're never run through fzf. See `hoot attach -h` for the
picker line format and fzf query vocabulary.

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
`POST /sessions/<key>/pty/input` on a `hoot serve` multiplexer (remote).
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
key reports. The client claims a Hoot-owned kitty keyboard stack frame
unconditionally on attach so the snapshot's kitty SET bytes land in
that frame and detach can pop only what hoot pushed.

On detach, the client emits a comprehensive cleanup sequence: kitty
keyboard pop, modifyOtherKeys off, OSC 9;4;0 (Ghostty/ConEmu progress
clear), and unconditional resets for every Ghostty mode that's safe to
reset without a save/restore lease — every mouse mode (?9, ?1000-1006,
?1015, ?1016), keyboard input modes (cursor keys, app keypad, focus
events, bracketed paste, KAM, IRM, LNM, etc.), display modes (reverse
colors, origin, wraparound, slow scroll, reverse wrap, 132-column,
synchronized output, grapheme cluster, color-scheme reports, in-band
size reports), layout (left-right margin, scroll region, cursor shape),
charset (G0=ASCII), and all three alt-screen variants (?47, ?1047,
?1049). Then SGR reset, cursor show, clear viewport, and the
`[disconnected. <session> @ <host>]` banner. None of these cleanup
bytes are written to the remote PTY or other attached clients.

The detach cleanup is owned by a `TerminalRestorer` interface; pick
between two implementations with `--restorer`:

- `--restorer=hoot` (default) — the comprehensive cleanup above.
- `--restorer=dtach` — tracks `crigler/dtach` upstream byte-for-byte:
  clear-on-attach, cursor-show-on-detach, nothing else. Escape state
  (kitty kbd, mouse modes, OSC progress, alt screen) intentionally
  leaks across detach. Useful as an experimental control to compare
  against the default; not for production use.

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
| `<prefix> ?`          | print the session HUD line + chord help on stdout |

#### Session HUD (terminal title + `<prefix> ?`)

When `hoot attach` connects, it sets the terminal window title to a
one-line summary of the session and saves the prior title via the
xterm title stack so it can be restored on detach. The format is the
same string the `<prefix> ?` chord prints into the terminal:

```
🦉 <id> [@host] <cwd> [<cmd...>]
```

`@host` is omitted for local sessions (the bare `🦉 <id> ...` form);
the joined argv is truncated at 60 runes with a trailing `…`. Example:

```sh
$ hoot attach a3f                # title becomes: 🦉 a3fxxx ~/proj/api bash --login
# (inside the session, after Ctrl-^ ?)
🦉 a3fxxx ~/proj/api bash --login
[hoot] commands: "." detach · "^" literal prefix · "Ctrl-Z" suspend · "c" clone · "?" help
```

Mechanism: title push/set on attach uses xterm window-manipulation
`CSI 22;2t` (push) followed by an `OSC 2 ; <text> BEL` (set window
title only — leaves the icon name alone, important for iTerm2 /
Terminal.app). Detach pops with `CSI 23;2t`. Modern xterm, iTerm2,
Ghostty, kitty, Wezterm, and Alacritty all implement the title
stack; on a terminal that doesn't, the sequences parse-and-drop and
the user's shell reasserts the title on next prompt redraw.

The title is set once at attach-start and not refreshed mid-session
— inner TUIs (vim, claude code, tmux, …) commonly set their own
title and we don't fight that. The `<prefix> ?` chord is the
escape hatch when you need a reminder of which session you're in;
it prints the HUD line and the chord vocabulary onto the local
terminal (stdout, dim SGR, leading + trailing CRLF) without
disturbing the inner program's cursor more than necessary.

Title emission is gated on stdout being a tty, so scripted attaches
(e.g. piped output) don't see stray control bytes.

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

| Method | Path                              | Behaviour                                                                  |
|--------|-----------------------------------|----------------------------------------------------------------------------|
| GET    | `/sessions`                       | `{sessions: [{...StateFile, alive}]}`                                      |
| POST   | `/sessions`                       | `{cmd \| argv, key?}` → forks `hoot __session`                            |
| GET    | `/sessions/{key}`                 | state.json (disk read)                                                     |
| DELETE | `/sessions/{key}`                 | SIGTERM the session by pid (204)                                           |
| GET    | `/sessions/{key}/resolve`         | resolve full id or unique prefix                                           |
| GET    | `/sessions/{key}/attach`          | WebSocket bridge to upstream `/attach` (browser)                           |
| GET    | `/sessions/{key}/attach-raw`      | reverse proxy → upstream `/attach` (CLI HTTP/1.1 Upgrade)                  |
| *      | `/sessions/{key}/{path...}`       | reverse proxy → rpc.sock `/<path...>` (signal, clone, events, pty/*, ...)  |
| GET    | `/healthz`                        | liveness                                                                   |

The catch-all `{path...}` is one `httputil.ReverseProxy` over a unix-socket
transport. Resolve happens server-side once (full id or unique
prefix → 404 / 409 on miss), then ReverseProxy forwards everything:
JSON RPCs round-trip, SSE flushes per chunk via `FlushInterval: -1`,
and HTTP/1.1 Upgrade is handled natively by Go ≥ 1.20. Adding a new
session verb means one more `super.Mux().HandleFunc(...)` in the
session library — no serve-side change.

The WebSocket bridge (`/attach`) speaks browser-friendly framing —
binary frames carry PTY bytes both directions, and a text frame
`{"type":"resize","cols":N,"rows":N}` is translated into an
`attachwire.MsgSize` upstream — and terminates the
`hoot-attach/1` HTTP-Upgrade protocol on the rpc.sock side. CLI
clients use `/attach-raw` (which the catch-all rewrites to upstream
`/attach`) so `hoot attach --remote` keeps speaking
`hoot-attach/1` end-to-end without colliding with the WS handshake.

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

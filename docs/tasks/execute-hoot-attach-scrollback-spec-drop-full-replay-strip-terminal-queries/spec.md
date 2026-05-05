---
overview: Spec for fixing hoot's attach scrollback and the related "strange chars after tmux detach" bug. Covers root cause (doubled terminal-query responses), what the libghostty VT formatter actually emits (it includes scrollback — verified empirically), and a two-part fix.
repo: ~/github.com/hayeah/hootty
tags:
  - spec
---

# Hoot: attach scrollback strategy

## Problem

Two related symptoms surfaced while playing with `hoot attach`:

**Symptom 1 — junk chars after tmux detach.** Inside an attached zsh, run `tmux attach`, do some work, then detach tmux. A burst of unintelligible bytes appears at the zsh prompt — things like `>|ghostty 1.3.1`, `10;rgb:ffff/ffff/ffff`, `;1768;2928t`, `62;22;52c`, `1;10;0c`, `97;2n`.

**Symptom 2 — no real scrollback story.** On reattach, the user wants to see what was previously on screen, including history. Today the attach handler offers two replay modes:

- `full` (default): stream the entire `pty.log` to the client.
- `snapshot` (`--no-full-replay`): emit `libghostty.Snapshot()` once.

It wasn't clear whether snapshot mode actually preserves scrollback or just paints the visible viewport, and the comment in `pty_libghostty.go:247` ("formats the whole terminal — there is no row-range API") reads ambiguously.

## Root cause of symptom 1

The visible junk chars are **terminal-query responses** that the inner program (tmux) didn't consume, getting echoed by the line discipline once zsh resumes.

A "terminal query" is a small RPC protocol where the program writes an escape sequence to stdout and the terminal answers by writing bytes back into the program's stdin. Examples tmux fires on startup:

| Query (program → terminal) | Reply (terminal → program) | Meaning |
|---|---|---|
| `ESC [ c` (DA1) | `ESC [ ? 62 ; 22 ; 52 c` | Primary device attributes |
| `ESC [ > c` (DA2) | `ESC [ > 1 ; 10 ; 0 c` | Secondary DA — terminal type + version |
| `ESC [ = c` (DA3) | `ESC P ! \| ... ESC \\` | Tertiary DA — unit ID |
| `ESC [ > q` (XTVERSION) | `ESC P > \| ghostty 1.3.1 ESC \\` | Terminal name + version |
| `ESC ] 10 ; ? ESC \\` (OSC 10) | `ESC ] 10 ; rgb:ffff/ffff/ffff ESC \\` | Foreground color |
| `ESC ] 11 ; ? ESC \\` (OSC 11) | `ESC ] 11 ; rgb:2828/2c2c/3434 ESC \\` | Background color |
| `ESC [ 14 t` | `ESC [ 4 ; pixH ; pixW t` | Window size in pixels |
| `ESC [ 18 t` | `ESC [ 8 ; rows ; cols t` | Window size in cells |
| `ESC [ 5 n` (DSR) | `ESC [ 0 n` | Status |
| `ESC [ 6 n` (CPR) | `ESC [ row;col R` | Cursor position |

Programs use these for capability detection. They're machine-readable, never meant for human eyes.

### The double-responder

`readLoop` in `pty_libghostty.go:204` reads bytes from the child PTY master and:

1. tees to the recorder (`pty.log`)
2. feeds libghostty's emulator — which auto-replies to queries via `WithWritePty` at `pty_libghostty.go:115`, writing replies back into the master (so the child receives them on stdin)
3. fans out the same raw chunk to every attach subscriber (the user's real terminal)

Step 3 is the leak: the child's queries also reach the user's real ghostty, which dutifully answers — those bytes flow `attach client → server → master.Write → child stdin`. Tmux now receives **two** replies for every query: one from libghostty, one from real ghostty. It consumes one set; the duplicate sits in the PTY input buffer.

When tmux detaches, the line discipline returns to cooked mode, zsh reads those leftover bytes, and the terminal echoes them as visible text. That's the junk.

The same mechanism corrupts `full` replay mode: replaying `pty.log` re-issues every query the child ever sent → real terminal answers each one → reply bytes inject into the child stdin during/after replay. Same disease, larger blast radius.

## libghostty formatter — what it actually emits

The Go binding doc says `NewFormatter` "creates a formatter for the given terminal's active screen." The phrasing is misleading — "active screen" means *primary vs alternate*, not *viewport vs history*. To verify, I ran two experiments against `libghostty.NewTerminal(WithSize(80, 10), WithMaxScrollback(1000))`.

Experiment source (drop into `~/github.com/hayeah/hootty/` and `go test -run TestScrollbackExperiment -v .`):
[`tmp/215117_246-scrollback_experiment_test.go`](tmp/215117_246-scrollback_experiment_test.go)

### Experiment 1 — primary screen, history overflow

Fed 50 numbered lines. Visible viewport is 10 rows; lines 1–40 scrolled into history.

```
[plain] len=399  line-01=true  line-41=true  line-50=true
[vt]    len=448  line-01=true  line-41=true  line-50=true
```

Both formatter modes emitted **all 50 lines**. The formatter walks the full scrollback ring up to `max_scrollback`. There is no row-range API and there doesn't need to be one — the ring cap is the knob.

### Experiment 2 — alt screen (tmux model)

Wrote 50 lines into the primary, then entered alt screen with `ESC[?1049h` and painted a 3-row "tmux" UI. Formatted while on alt:

```
[ALT-plain] len=20  has_line01=false  has_TMUX=true
            "TMUX-PANEL\nrow2\nrow3"
[ALT-vt]    len=22  has_line01=false  has_TMUX=true
            "TMUX-PANEL\r\nrow2\r\nrow3"
```

Only the alt screen content. Primary scrollback is preserved internally but invisible while alt is active — matches what a real terminal would show.

After leaving alt screen with `ESC[?1049l`:

```
[POST-ALT vt] len=448  has_line01=true  has_TMUX=false
```

Full primary scrollback restored, alt content gone. tmux-detach correctly returns history.

### Conclusion

`Snapshot()` does the right thing **for the active screen**.

## Known limitation: alt-screen attach loses primary scrollback

If a client attaches while the child is on alt screen (tmux, vim, less, etc.), only alt-screen content is sent. When the child later exits alt, the client's terminal restores *its own* empty primary — hoot's recorded primary scrollback never reaches it.

**Not fixing in this round.** The libghostty formatter API has no inactive-screen selector, so a fix requires either a parallel primary-only emulator (~2× cost) or a destructive toggle on the live emulator (race-prone). Bite the bullet for now; flag it in code and move on.

Full analysis and the Option A/B/C tradeoff is captured in [`hoot-alt-screen-scrollback-gap_claude.md`](hoot-alt-screen-scrollback-gap_claude.md).

## What Snapshot() does NOT include by default

`Snapshot()` is `formatBytes(FormatterFormatVT)` and emits scrollback up to `max_scrollback` (default 10,000 rows). Items it does **not** include by default:

- **Cursor position** — fix with `WithFormatterExtraCursor(true)` (emits CUP at the end).
- **Active SGR** — fix with `WithFormatterExtraStyle(true)`.
- **Terminal modes** (DECCKM, bracketed paste, mouse) — fix with `WithFormatterExtraModes(true)`.

The `pty_libghostty.go:247` comment should be corrected; it reads as "no scrollback" but the actual behavior is "scrollback included, no row-range slicing."

## Strategy

The two symptoms intervene at different points in the lifecycle and are not alternatives.

### Part 1 — replace full-replay with snapshot, then remove full-replay

`Snapshot()` already gives reasonable scrollback. The recommendation is to **delete full-replay mode entirely**, not just flip the default.

Why removal, not just default flip:

- Full-replay is actively wrong for the user-facing case. It re-issues every terminal query the child ever sent, and the user's real terminal answers each one — those replies inject as input into the child's stdin. Same disease as the live-bug, spread across the session's lifetime.
- It scales poorly. `pty.log` grows unboundedly across the session lifetime; a single zsh+tmux session here was already 548 KB. Snapshot is bounded by `max_scrollback` × cols.
- Snapshot dominates on every axis a user cares about: faithful viewport + history, bounded size, no probe leakage, render-only by construction.
- The only remaining argument for full-replay is "exact byte-for-byte fidelity for debugging the recorder" — which is a developer tool, not a user feature, and the recorder writes `pty.log` to disk where it can be inspected directly with `cat`/`less` without involving the attach path.

Concretely:

- `cmd/hoot/attach.go:77` — drop the `--no-full-replay` flag.
- `cmd/hoot/attach.go:95-97` — drop the mode toggle; always send `replayMode = "snapshot"`.
- `attach_handler.go:121-129` (the `switch hello.ReplayMode` arms) — remove the `"full"` arm; reject anything that isn't `"snapshot"` (or drop the field entirely from the wire protocol — see `internal/attachwire/wire.go:45`).
- `internal/attachwire/wire.go:45` — once nothing sends `"full"`, the `ReplayMode` field can go too. Wire bumps are cheap on a personal project; not worth carrying a deprecated mode for backward compat.

Add formatter extras to the Snapshot path so the cursor lands correctly and SGR doesn't bleed:

```go
WithFormatterExtraCursor(true)
WithFormatterExtraStyle(true)
WithFormatterExtraModes(true)  // optional, evaluate cost
```

The `pty.log` file stays — it's still useful as a recording artifact for offline analysis, just not as the source for attach replay.

### Part 2 — strip terminal queries from the live subscriber fanout

This is the actual fix for the reported tmux-detach junk. Snapshot-on-attach does not address the live stream — `readLoop` still ships raw child bytes (queries included) to attach subscribers, and the real terminal still responds.

Add a filter on the subscriber-fanout path in `pty_libghostty.go:204` `readLoop` (recorder + emulator continue to receive raw bytes; only `for ch := range p.subs` gets the scrubbed chunk).

Sequences to drop:

- `CSI c` / `CSI ? c` / `CSI > c` / `CSI = c` — primary/secondary/tertiary DA
- `CSI > q` — XTVERSION
- `CSI 5 n` — DSR
- `CSI 6 n` / `CSI ? 6 n` — CPR / DECXCPR
- `CSI ? Pn $ p` — DECRQM (mode query)
- `CSI 14 t` / `CSI 16 t` / `CSI 18 t` / `CSI 19 t` — window/cell/screen size queries
- `OSC 10 ; ? ST` / `OSC 11 ; ? ST` / `OSC 4 ; n ; ? ST` / `OSC 12 ; ? ST` — color queries (BEL or ST terminator)
- `ESC Z` — DECID

The set is finite and stable. Ghostty's Zig parser already enumerates all of these — borrow the list rather than reinventing.

### Where to lift from in ghostty

Cloned at `~/github.com/ghostty-org/ghostty`. The query set is concretely identifiable as the actions that have **effect-based handlers** rather than "terminal-modifying" handlers in ghostty's own dispatch. From `src/terminal/stream_terminal.zig:245-255` (the dispatch switch in the "Effect-based handlers" comment block):

```zig
.bell => self.bell(),
.device_attributes => self.reportDeviceAttributes(value),    // CSI c, > c, = c
.device_status => self.deviceStatus(value.request),          // CSI n  (DSR / CPR / DECXCPR)
.enquiry => self.reportEnquiry(),                            // ENQ (0x05)
.kitty_keyboard_query => self.queryKittyKeyboard(),          // CSI ? u
.request_mode => self.requestMode(value.mode),               // CSI ? Pn $ p  (DECRQM)
.request_mode_unknown => self.requestModeUnknown(value.mode, value.ansi),
.size_report => self.reportSize(value),                      // CSI 14/16/18/19 t
.window_title => self.windowTitle(value.title),              // CSI 21 t  (title query subset)
.xtversion => self.reportXtversion(),                         // CSI > q
```

Bell is not a query (skip it); window_title at `CSI 21 t` is a title push/pop side, but `CSI t` with size args 14/16/18/19 is what we care about — see `csi.SizeReportStyle`.

The Action union is defined at `src/terminal/stream.zig:34-127` (`pub const Action = union(Key)`). The Key tags above are the canonical query enumeration — a Go port can mirror the same set.

OSC color queries are dispatched through `color_operation` (`src/terminal/stream.zig:126`, `pub const ColorOperation = struct` at line 390). The OSC color parser lives at `src/terminal/osc/parsers/color.zig` — request entries with a `?` payload are the queries (`OSC 4 ; n ; ?`, `OSC 10 ; ?`, `OSC 11 ; ?`, `OSC 12 ; ?`, etc.); without `?` they are *sets*, which we must NOT strip from the fanout (a child legitimately setting bg color should reach the user's terminal).

CSI sequences and their final-byte/intermediate keys are wired up in the parser switch starting around `src/terminal/stream.zig:1832` (e.g. the `'?' => self.handler.vt(.kitty_keyboard_query, {})` at line 1832 dispatches `CSI ? u`).

Also relevant: `src/terminal/device_attributes.zig` (DA1/DA2/DA3 request kinds) and `src/terminal/csi.zig` for `SizeReportStyle`.

### Suggested Go port shape

A minimal stripper doesn't need to fully parse — it needs to **recognize-and-skip** a known finite set of byte patterns. A small DFA over CSI / OSC / single-byte (ENQ) entries:

- `0x05` (ENQ) — drop the byte.
- `ESC [ ... <final>` where the parameters + intermediates + final match one of the query shapes (DA `c`, DSR `n`, DECRQM `$p`, size report `t` with leading `14`/`16`/`18`/`19`, XTVERSION `>q`, kitty kbd query `?u`) — drop the whole sequence.
- `ESC ] <Ps> ; ? <ST>` (OSC with `?` parameter, terminated by BEL `0x07` or `ESC \\`) for `Ps` in `{4,10,11,12}` — drop. OSC 4 takes `n;?` form.

Buffer partial sequences across read-chunk boundaries; passthrough anything not in the recognized set unchanged.

The unit tests in `~/go/pkg/mod/github.com/mitchellh/go-libghostty@*/formatter_test.go` and ghostty's `src/terminal/stream_terminal.zig` (search for `test "xtversion ..."` etc., starting around line 1485) are good fixture sources for both query bytes and expected (post-strip) passthrough bytes.

Implementation note: the filter needs a small streaming state machine because escape sequences can split across reads. Easiest approach: pass each chunk through a stripper that buffers any partial sequence at the chunk boundary.

### Part 3 — doc cleanup

- Update the comment at `pty_libghostty.go:247` to reflect that the formatter dumps scrollback (the `lines` parameter being ignored is real, but the conclusion a reader draws from "no row-range API" is the wrong one).
- Add a comment near `Snapshot()` documenting the alt-screen scrollback gap, pointing at `~/Dropbox/notes/2026-05-02/hoot-alt-screen-scrollback-gap_claude.md`.

### Out of scope

A larger redesign would push libghostty's render output to clients instead of raw child bytes — eliminating the double-responder problem by construction (libghostty becomes the only terminal in the loop). Worth considering later; not needed for the present bug.

## Verification plan

After the fix:

1. `hoot attach <key>`, run `tmux attach`, do work, detach. No junk bytes at zsh prompt.
2. `hoot attach <key>` to a session with substantial history → scrollback is visible above the viewport (up to `max_scrollback`).
3. `hoot attach <key>` while child is in alt screen → repaint shows the alt screen, not stale primary history. (Known limitation: primary scrollback won't reappear when alt exits — see linked gap note.)
4. Detach + reattach mid-tmux session → tmux UI repaints, no probe leakage.

## Files touched (anticipated)

- `cmd/hoot/attach.go` — flip default `replayMode`.
- `pty_libghostty.go` — formatter extras on `Snapshot()`; subscriber fanout filter; corrected comment.
- New: a small VT-query stripper (probably its own file with focused unit tests).

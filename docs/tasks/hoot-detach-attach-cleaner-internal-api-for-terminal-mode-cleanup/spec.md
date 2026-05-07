# Hoot detach/attach — cleaner internal API for terminal-mode cleanup

## Goal

Refactor `cmd/hoot/attach.go`'s detach-time terminal-cleanup machinery into a small `TerminalRestorer` interface with two interchangeable implementations, AND do the cleanup as **comprehensively as is correct**: scour Ghostty's `src/terminal/modes.zig` plus the non-ModeState terminal state Ghostty handles, and decide each entry one at a time. No "Future Work" — anything we should be cleaning up but aren't, we add now; anything we *can't* safely clean up unconditionally, we name explicitly with the reason and leave out.

The two restorers:

- **`hootRestorer`** — Hoot's default. Comprehensive enumerated cleanup of every Ghostty mode that's safe to reset unconditionally, plus observer-driven cleanup of state with stack/save semantics (kitty keyboard, modifyOtherKeys), plus the new `OSC 9;4;0` Ghostty progress-bar clear from the codex spec.
- **`dtachRestorer`** — Tracks `crigler/dtach`'s actual attach/detach behavior. On attach: `\x1b[H\x1b[2J` (clear screen). On detach: `\x1b[?25h` (cursor show). Termios is already handled by `runAttachLoop`. Nothing else; escape state set by the inner program (kitty kbd, mouse, alt-screen, OSC progress, etc.) is *intentionally* allowed to leak across detach the same way it would under dtach.

Selectable per-attach via a new `--restorer {hoot,dtach}` flag (default `hoot`).

The structural refactor lands in **two new files**: `cmd/hoot/attach_restorer.go` (~250 lines, larger than the prior rev because the comprehensive cleanup means more constants + a longer `Cleanup()`) and `cmd/hoot/attach_restorer_test.go`. `cmd/hoot/attach.go` shrinks by ~250 lines (the `seqXxx` block, `terminalModeTracker`, helpers). `cmd/hoot/attach_term_modes_test.go` is deleted (subsumed). Net: roughly +0 to +50 lines in `cmd/hoot/`, plus a much cleaner shape, the comprehensive cleanup, and the new flag.

The point of `dtachRestorer` is *experimental control*: by running `--restorer=dtach` alongside `--restorer=hoot` in real use, we A/B test which pieces of `hootRestorer`'s enumerated cleanup are actually load-bearing. dtach is the right anchor for the control because it's a battle-tested baseline ("transparent pipe + local termios guard, nothing else") that we can credibly compare against.

## Background

The codex spec ([Appendix](#appendix-codex-diagnosis)) diagnosed why Ghostty's progress strip stays on after `hoot detach`: Claude Code emits `OSC 9;4;1;N` while `/usage` is open, and `OSC 9;4;0` when it exits via ESC. Forced detach skips the clear because Claude Code keeps running on the supervisor; the user's local Ghostty surface never gets the remove. The fix is one byte sequence, `\x1b]9;4;0;\x07`, on the client side.

Codex's spec proposed two things in tension. First, a small fix: add `seqOSCProgressClear` to the existing flat constants block and slot it into `resetTermModes`. Second, a larger refactor: `TerminalRestorer` interface with `MinimalTerminalRestorer` + `BestEffortTerminalRestorer`, plus a server-side typed-event scanner with a new wire frame, plus per-mode files. This rev takes the middle path *plus* widens the cleanup to be comprehensive instead of slot-in-one-mode.

### Current shape (what we're refactoring)

In `cmd/hoot/attach.go`:

- A flat block of `seqXxx` constants (lines 679–732), one per terminal mode.
- A `resetTermModes` constant concatenating them in fixed order.
- A `terminalModeTracker` (lines 760–880) that observes streamed CSI bytes, counts kitty kbd pushes/pops, and flips a `modifyOtherKeys` bool. Its `detachReset()` returns a *prefix* prepended to `resetTermModes` at detach.
- A `pushKittyKbdForSnapshot` method that scans incoming snapshots for kitty kbd SET bytes and conditionally writes `CSI > u`.
- `containsKittyKbdSet` + `parseCSIParamDefault` helpers.
- `emitDetach` (line 752) weaves them together.

The state, the bytes, the observation, and the assembly are tangled across one function and one struct in one giant file. The refactor cuts these along the policy boundary (one struct per restorer policy, methods own the bytes inline) instead of along the mode boundary, AND fills in the missing modes that `terminalModeTracker` never covered.

## Comprehensive cleanup catalogue

This is the load-bearing section of the spec. Every entry in `repos/github.com/ghostty-org/ghostty/src/terminal/modes.zig:251` (the canonical mode catalogue Ghostty exposes) plus the non-ModeState state Ghostty also handles, with a decision for each.

Decisions key:

- **emit** — `hootRestorer.Cleanup()` includes this byte sequence unconditionally.
- **observe** — observer-driven; emitted at detach only if observed during attach.
- **skip-default** — Ghostty default is "set" (or the typical pre-attach terminal default is "set"), so a reset would be a regression. Skip.
- **skip-cursor-side-effect** — emitting the reset would visibly move the cursor. Skip.
- **skip-niche** — niche enough that no real-world TUI is observed setting it; the cost of an extra escape sequence outweighs the benefit.
- **skip-lease-required** — would need save/restore (DECRQM-style) to be correct, because the user's pre-attach terminal may already have it set. See [What we explicitly skip and why](#what-we-explicitly-skip-and-why).
- **already** — covered by today's hoot cleanup; preserved in the new `hootRestorer.Cleanup()`.

### ANSI modes (CSI N h/l, no ? prefix)

| Value | Name (Ghostty) | Default | Decision | Reset bytes | Notes |
| --- | --- | --- | --- | --- | --- |
| 2 | `disable_keyboard` (KAM) | false | emit | `CSI 2 l` | NEW. If a TUI sets this and crashes, user's shell becomes input-locked. |
| 4 | `insert` (IRM) | false | emit | `CSI 4 l` | NEW. Affects how the shell renders typed characters. |
| 12 | `send_receive_mode` (SRM) | **true** | skip-default | — | Default is on; resetting would un-echo. |
| 20 | `linefeed` (LNM) | false | emit | `CSI 20 l` | NEW. |

### DEC modes (CSI ? N h/l)

| Value | Name (Ghostty) | Default | Decision | Reset bytes | Notes |
| --- | --- | --- | --- | --- | --- |
| 1 | `cursor_keys` (DECCKM) | false | emit | `CSI ? 1 l` | NEW. App cursor keys → normal. |
| 3 | `132_column` (DECCOLM) | false | emit | `CSI ? 3 l` | NEW. Ghostty gates DECCOLM behind `?40`; safe to emit unconditionally. |
| 4 | `slow_scroll` (DECSCLM) | false | emit | `CSI ? 4 l` | NEW. |
| 5 | `reverse_colors` (DECSCNM) | false | emit | `CSI ? 5 l` | NEW. Highly visible if left set. |
| 6 | `origin` (DECOM) | false | emit | `CSI ? 6 l` | NEW. Affects cursor positioning relative to scroll region. |
| 7 | `wraparound` (DECAWM) | **true** | emit-set | `CSI ? 7 h` | NEW. Default-on; if a TUI turned it off, restore. |
| 8 | `autorepeat` (DECARM) | false | skip-niche | — | Keyboard autorepeat; terminal-wide, not session-scoped. |
| 9 | `mouse_event_x10` | false | emit | `CSI ? 9 l` | NEW. We currently reset 1000/1006 but not 9. |
| 12 | `cursor_blinking` | false | skip-niche | — | Cosmetic; terminal default varies. |
| 25 | `cursor_visible` (DECTCEM) | **true** | already (emit-set) | `CSI ? 25 h` | Existing. |
| 40 | `enable_mode_3` | false | skip-niche | — | Gates DECCOLM; rarely toggled. |
| 45 | `reverse_wrap` | false | emit | `CSI ? 45 l` | NEW. |
| 47 | `alt_screen_legacy` | false | emit | `CSI ? 47 l` | NEW. Oldest alt-screen toggle; some TUIs still use it. |
| 66 | `keypad_keys` (DECNKM) | false | emit | `CSI ? 66 l` | NEW. App keypad → normal (DEC private form). |
| 67 | `backarrow_key_mode` (DECBKM) | false | emit | `CSI ? 67 l` | NEW. If a TUI flipped this, backspace returns the wrong byte to the shell. |
| 69 | `enable_left_and_right_margin` (DECLRMM) | false | emit | `CSI ? 69 l` | NEW. |
| 1000 | `mouse_event_normal` | false | already | `CSI ? 1000 l` | Existing. |
| 1002 | `mouse_event_button` | false | emit | `CSI ? 1002 l` | NEW. |
| 1003 | `mouse_event_any` | false | emit | `CSI ? 1003 l` | NEW. |
| 1004 | `focus_event` | false | already | `CSI ? 1004 l` | Existing. |
| 1005 | `mouse_format_utf8` | false | emit | `CSI ? 1005 l` | NEW. |
| 1006 | `mouse_format_sgr` | false | already | `CSI ? 1006 l` | Existing. |
| 1007 | `mouse_alternate_scroll` | **true** | skip-default | — | Default on; resetting would regress. |
| 1015 | `mouse_format_urxvt` | false | emit | `CSI ? 1015 l` | NEW. |
| 1016 | `mouse_format_sgr_pixels` | false | emit | `CSI ? 1016 l` | NEW. |
| 1035 | `ignore_keypad_with_numlock` | **true** | skip-default | — | |
| 1036 | `alt_esc_prefix` | **true** | skip-default | — | |
| 1039 | `alt_sends_escape` | false | emit | `CSI ? 1039 l` | NEW. |
| 1045 | `reverse_wrap_extended` | false | emit | `CSI ? 1045 l` | NEW. |
| 1047 | `alt_screen` | false | emit | `CSI ? 1047 l` | NEW. Legacy alt-screen toggle. |
| 1048 | `save_cursor` | false | skip-cursor-side-effect | — | `?1048 l` actively *moves* the cursor to the saved position. Skip on its own; 1049 covers the typical use. |
| 1049 | `alt_screen_save_cursor_clear_enter` | false | already | `CSI ? 1049 l` | Existing. |
| 2004 | `bracketed_paste` | false | already | `CSI ? 2004 l` | Existing. |
| 2026 | `synchronized_output` | false | emit | `CSI ? 2026 l` | NEW. If left on, terminal queues updates indefinitely. |
| 2027 | `grapheme_cluster` | false | emit | `CSI ? 2027 l` | NEW. |
| 2031 | `report_color_scheme` | false | emit | `CSI ? 2031 l` | NEW. Stops light/dark scheme reports. |
| 2048 | `in_band_size_reports` | false | emit | `CSI ? 2048 l` | NEW. |

### Non-ModeState terminal state Ghostty handles

| Source ref | Decision | Reset bytes | Notes |
| --- | --- | --- | --- |
| Kitty keyboard stack (`stream.zig:1827`) | already (observe) | `CSI < N u` | Existing observer-driven; pop count = initial 1 (always-push at attach) + observed live pushes − observed live pops. |
| modifyOtherKeys (`formatter.zig:383`) | already (observe) | `CSI > 4 ; 0 m` | Existing observer-driven. Emit only if seen enabled. |
| OSC 9;4 progress (`apprt/gtk/class/surface.zig:1001`) | emit | `OSC 9;4;0; BEL` | NEW. The Ghostty progress-bar bug fix. Always-emit; on terminals that never set progress it's a no-op. |
| App keypad (DECKPNM, `ESC >`) | emit | `ESC >` | NEW. Distinct from `?66`; some TUIs use `ESC =` (DECKPAM) without the DEC private form. |
| Cursor shape (DECSCUSR, `CSI N SP q`) | emit | `CSI 0 SP q` | NEW. Resets to terminal-default cursor shape. |
| Top/bottom scroll region (DECSTBM, `CSI N ; M r`) | emit | `CSI r` | NEW. `CSI r` with no params = full screen. |
| SGR (graphic rendition) | already | `CSI 0 m` | Existing. |
| Cursor home + clear screen | already | `CSI H CSI 2 J` | Existing. cosmetic; clears residual alt-screen flash on `?1049 l`. |
| Charset G0 selection (SCS) | emit | `\x0f \x1b ( B` | NEW. SI + `ESC ( B` selects G0=ASCII; safe even if TUI never touched charsets. |

### What we explicitly skip and why

These are NOT filed as Future Work. They are real terminal-state hazards Ghostty handles, but each requires a different cleanup model than "always-emit a reset" — typically save/restore via DECRQM-style queries, which is out of scope for this section.

| Hazard | Why we skip |
| --- | --- |
| OSC 4/5/10–19 (palette + dynamic colors set) and OSC 104/105/110–119 (resets) | A TUI can set palette/colors via OSC 4/10/11/12. If we always-emit the OSC 104/110/111/112 resets at detach, we wipe out palette/colors that the **user's shell** may have set before attach (theme-aware shells do this). Observation alone isn't sufficient: if both shell *and* TUI set the same color, resetting drops to terminal default, not back to shell value. Correct cleanup requires querying pre-attach state and restoring it (DECRQM-style lease model, codex's "principled lease" addendum). Out of scope. |
| OSC 0/1/2 (window title/icon) | We don't know the pre-attach title. Resetting to empty would clobber whatever the user's shell PROMPT_COMMAND was setting. Lease model required. |
| OSC 7 (working directory) | Same lease problem as title — shell PROMPT_COMMAND territory. |
| OSC 8 (hyperlinks) | A TUI can leave a hyperlink "open" with `OSC 8 ; ; uri ST`. We could emit `OSC 8 ;; ST` to close it unconditionally, but observation is needed to know if any open. The more common pattern is TUIs close their own hyperlinks on exit; the failure mode is rare. Re-evaluate if reports surface. |
| OSC 22 (mouse shape) | Same lease problem: user's shell may set a custom shape via terminal config or prompt hook. Lease required. |
| OSC 52 (clipboard) | One-way effect, not persistent state. Nothing to clean. |
| OSC 9;1, 9;2, 9;5, 9;6, 9;7, 9;8 (ConEmu sleep/message-box/wait-input/macro/run-process/env-out) | Effects, not persistent state. Nothing to clean. |
| OSC 9;3 (ConEmu tab title) | Persistent, but same lease problem as OSC 0/1/2 (window title). |
| OSC 9;10 (ConEmu xterm emulation toggle) | Persistent terminal mode, but extremely niche; no real-world repro. Skip until reports. |
| OSC 21 (kitty color protocol) | Lease problem like OSC 4/10/11. |
| OSC 66 (kitty text sizing) | Persistent if applied; lease problem. |
| OSC 133 (semantic prompt) | Shell-integration territory, not our place. |
| OSC 777 / OSC 9 notifications | Effect, not persistent state. |
| Kitty graphics (image storage) | Stored in terminal RAM; no defined "clear all" sequence universal across terminals. Niche for our use. |
| `OSC 3008` (hierarchical context) | Niche. |
| Tab stops (`CSI 3 g` clear / `HTS` set) | TUIs rarely modify; resetting all tabs would drop the standard 8-column defaults. |

The principle: **observation-based reset is unsafe for state the user's shell may independently set** (colors, title, pwd, mouse shape). Without a query/restore lease, observation alone doesn't restore correctly. We'd rather leak state than clobber user state. That's a real, defensible choice — not a punt.

## Design

### The interface

```go
// TerminalRestorer manages local-terminal cleanup for one hoot attach
// session. Implementations are stateful and live for the duration of
// runAttachLoop; lifecycle methods Attach/Observe/Cleanup span reconnects.
type TerminalRestorer interface {
    // Name identifies the restorer in --restorer and in tests.
    Name() string

    // Attach is invoked exactly once per process, when the local terminal
    // first starts receiving remote state. The gate is in runServerLoop
    // (an atomic.Bool false→true transition on the first snapshot frame),
    // not in the restorer — implementations do not need sync.Once.
    //
    // Setup writes go to w: e.g. hootRestorer pushes a kitty kbd stack
    // frame; dtachRestorer writes a screen clear. If the write errors,
    // implementations should NOT update bookkeeping state that Cleanup
    // depends on (e.g. don't increment kittyPops if the push write failed)
    // so Cleanup emits a consistent state.
    //
    // Across reconnects, Attach is NOT re-called: hoot's "stay raw,
    // repaint snapshot, transparent retry" model means each drop+reconnect
    // is invisible to the user. See the "Reconnects are transparent retry"
    // decision below.
    Attach(w io.Writer) error

    // Observe is invoked for every chunk of remote PTY output written to
    // the local terminal, including snapshot payloads. Implementations
    // may sniff CSI/OSC bytes to update internal state for Cleanup.
    Observe(payload []byte)

    // Cleanup writes the detach-time bytes to w. Called exactly once from
    // emitDetach. Symmetric with Attach: both take io.Writer, no
    // accumulator type. Must not panic; safe to call from a defer.
    //
    // Implementations write each cleanup sequence as a separate Write call
    // to keep the source readable; the io.Writer is the same one runAttachLoop
    // is using for stdout (already line-buffered or raw-tty unbuffered),
    // so coalescing isn't needed.
    Cleanup(w io.Writer) error
}
```

### Selecting the restorer

```go
// In cmdAttach:
restorerFlag := fs.String("restorer", "hoot",
    "terminal restorer policy:\n"+
    "  hoot   (default) comprehensive enumerated cleanup of every Ghostty\n"+
    "         mode that's safe to reset unconditionally, plus observer-driven\n"+
    "         cleanup of kitty keyboard + modifyOtherKeys, plus OSC 9;4;0\n"+
    "         (Ghostty progress-bar clear).\n"+
    "  dtach  tracks crigler/dtach: clear-on-attach + cursor-show-on-detach.\n"+
    "         Intentionally leaks escape state across detach so we can\n"+
    "         A/B test which pieces of the hoot restorer are load-bearing.")
```

### `hootRestorer` (the default)

```go
// hootRestorer is the default restorer.
//
// On Attach it claims a kitty kbd stack frame so the libghostty snapshot's
// kitty kbd SET bytes (CSI = ... u) land in a Hoot-owned frame rather than
// the user's pre-attach terminal frame. While attached it observes live CSI
// bytes for additional kitty pushes/pops and for modifyOtherKeys changes.
//
// On Cleanup it emits the comprehensive enumerated reset listed in the
// catalogue section of spec.md, plus the conditional state-derived prefix
// (kitty pop count, modifyOtherKeys off if observed) and the OSC 9;4;0
// progress clear that fixes the Ghostty stuck-progress-bar bug after
// `hoot detach` while Claude Code's /usage was open.
type hootRestorer struct {
    kittyPops       atomic.Int32
    modifyOtherKeys atomic.Bool

    // CSI streaming-parser scratch; only touched from Observe (which
    // runs single-threaded on runServerLoop's goroutine).
    csiBuf []byte
}

func (*hootRestorer) Name() string { return "hoot" }

func (r *hootRestorer) Attach(w io.Writer) error {
    // Push an empty Hoot-owned kitty kbd frame. Symmetric pop in
    // Cleanup. Sent unconditionally; on terminals without kitty kbd
    // support the unrecognized private-CSI parses-and-drops with no
    // visible effect.
    //
    // No sync.Once: runServerLoop's attached.CompareAndSwap gates this
    // to once-per-process. If the write errors, kittyPops stays at 0 so
    // Cleanup won't emit a pop for a push that didn't happen.
    if _, err := w.Write([]byte(seqKittyKbdPushEmpty)); err != nil {
        return err
    }
    r.kittyPops.Add(1)
    return nil
}

func (r *hootRestorer) Observe(payload []byte) {
    // Streaming CSI parser. Same logic as today's terminalModeTracker.observe.
    // Updates r.kittyPops on CSI > u / CSI < N u and r.modifyOtherKeys on
    // CSI > 4 ; N m. (~50 lines, copied verbatim with field rename.)
}

func (r *hootRestorer) Cleanup(w io.Writer) error {
    write := func(s string) error {
        _, err := io.WriteString(w, s)
        return err
    }

    // 1. Conditional state-derived prefix.
    if n := r.kittyPops.Load(); n > 0 {
        var s string
        if n == 1 {
            s = seqKittyKbdPop
        } else {
            s = fmt.Sprintf("\x1b[<%du", n)
        }
        if err := write(s); err != nil { return err }
    }
    if r.modifyOtherKeys.Load() {
        if err := write(seqModifyOtherKeysOff); err != nil { return err }
    }

    // Always-emit sequences. Each is an independently safe DECRST or
    // private-CSI; terminals that don't implement the mode parse-and-drop.
    for _, s := range []string{
        // 2. GUI affordance clear (the codex fix).
        seqOSCProgressClear,

        // 3. Mouse modes.
        seqMouseX10Off,           // ?9
        seqMouseNormalOff,        // ?1000
        seqMouseButtonOff,        // ?1002
        seqMouseAnyOff,           // ?1003
        seqMouseFmtUTF8Off,       // ?1005
        seqMouseFmtSGROff,        // ?1006
        seqMouseFmtUrxvtOff,      // ?1015
        seqMouseFmtSGRPixelsOff,  // ?1016

        // 4. Keyboard input modes.
        seqAppCursorKeysOff,      // ?1
        seqAppKeypadDECOff,       // ?66
        seqAppKeypadESCOff,       // ESC >
        seqBackarrowOff,          // ?67
        seqFocusEventsOff,        // ?1004
        seqBracketedPasteOff,     // ?2004
        seqAltSendsEscapeOff,     // ?1039
        seqDisableKeyboardOff,    // CSI 2 l (KAM)
        seqInsertModeOff,         // CSI 4 l (IRM)
        seqLinefeedModeOff,       // CSI 20 l (LNM)

        // 5. Display modes.
        seqReverseColorsOff,      // ?5
        seqOriginModeOff,         // ?6
        seqWraparoundOn,          // ?7 h (default-on)
        seqSlowScrollOff,         // ?4
        seqReverseWrapOff,        // ?45
        seqReverseWrapExtOff,     // ?1045
        seq132ColumnOff,          // ?3
        seqSyncOutputOff,         // ?2026
        seqGraphemeClusterOff,    // ?2027
        seqReportColorSchemeOff,  // ?2031
        seqInBandSizeReportsOff,  // ?2048

        // 6. Layout / margins / scrolling.
        seqLeftRightMarginOff,    // ?69
        seqScrollRegionReset,     // CSI r
        seqCursorShapeDefault,    // CSI 0 SP q

        // 7. Charset.
        seqCharsetG0Ascii,        // SI + ESC ( B

        // 8. Alt screens (in DECSET-number order).
        seqAltScreen47Off,        // ?47
        seqAltScreen1047Off,      // ?1047
        seqAltScreen1049Off,      // ?1049

        // 9. Visual reset.
        seqSGRReset,              // CSI 0 m
        seqCursorShow,            // ?25 h
        seqClearScreen,           // CSI H CSI 2 J
    } {
        if err := write(s); err != nil { return err }
    }
    return nil
}
```

That's the full enumeration. Roughly 35 byte-string emits totaling ~150 bytes per detach. On terminals that don't implement a particular mode, the unrecognized CSI parses-and-drops; net effect is zero.

### `dtachRestorer` (tracks crigler/dtach)

```go
// dtachRestorer tracks crigler/dtach's actual attach/detach behavior.
// Useful as an experimental control: with this restorer, escape state
// set by the inner program (kitty keyboard, alt-screen, mouse modes,
// OSC progress, etc.) leaks across detach exactly the way it would
// under dtach. Comparing --restorer=dtach against --restorer=hoot in
// real use tells us which pieces of hootRestorer's enumerated cleanup
// are load-bearing.
//
// dtach upstream (github.com/crigler/dtach):
//
//   - main.c:279       tcgetattr(0, &orig_term)
//   - attach.c:208     switch local terminal to raw mode
//   - attach.c:220     clear screen on attach: ESC [ H ESC [ 2 J
//   - attach.c:196     atexit(restore_term)
//   - attach.c:38      restore_term: tcsetattr orig_term + write \x1b[?25h
//
// Termios save/raw/restore is already handled by runAttachLoop
// (term.MakeRaw / term.Restore around the whole loop), matching dtach
// upstream. This restorer covers the display-side bytes only:
// clear-on-attach + cursor-show-on-detach.
type dtachRestorer struct{}

func (*dtachRestorer) Name() string { return "dtach" }

func (*dtachRestorer) Attach(w io.Writer) error {
    // dtach/attach.c:220
    _, err := w.Write([]byte(seqClearScreen))
    return err
}

func (*dtachRestorer) Observe(_ []byte) {}

func (*dtachRestorer) Cleanup(w io.Writer) error {
    // dtach/attach.c:38 — restore_term emits this on exit.
    _, err := io.WriteString(w, seqCursorShow)
    return err
}
```

Three lines of behavior. That's the whole impl.

### Constants

Kept in the same file. Each constant is independently safe to send: DECRST and the keyboard-protocol forms use private-use CSI prefixes, so terminals that don't implement the mode parse-and-drop the bytes without visible effect.

```go
const (
    // Conditional / observer-driven state.
    seqKittyKbdPop          = "\x1b[<u"          // pop one kitty kbd stack level
    seqKittyKbdPushEmpty    = "\x1b[>u"          // push a Hoot-owned kitty kbd frame
    seqModifyOtherKeysOff   = "\x1b[>4;0m"       // disable xterm modifyOtherKeys mode 2

    // GUI affordance.
    seqOSCProgressClear     = "\x1b]9;4;0;\x07"  // OSC 9;4;0 — ConEmu/Ghostty progress remove

    // Mouse modes.
    seqMouseX10Off          = "\x1b[?9l"
    seqMouseNormalOff       = "\x1b[?1000l"
    seqMouseButtonOff       = "\x1b[?1002l"
    seqMouseAnyOff          = "\x1b[?1003l"
    seqMouseFmtUTF8Off      = "\x1b[?1005l"
    seqMouseFmtSGROff       = "\x1b[?1006l"
    seqMouseFmtUrxvtOff     = "\x1b[?1015l"
    seqMouseFmtSGRPixelsOff = "\x1b[?1016l"

    // Keyboard input modes.
    seqAppCursorKeysOff     = "\x1b[?1l"
    seqAppKeypadDECOff      = "\x1b[?66l"
    seqAppKeypadESCOff      = "\x1b>"            // DECKPNM
    seqBackarrowOff         = "\x1b[?67l"
    seqFocusEventsOff       = "\x1b[?1004l"
    seqBracketedPasteOff    = "\x1b[?2004l"
    seqAltSendsEscapeOff    = "\x1b[?1039l"
    seqDisableKeyboardOff   = "\x1b[2l"          // KAM (ANSI, no ?)
    seqInsertModeOff        = "\x1b[4l"          // IRM (ANSI, no ?)
    seqLinefeedModeOff      = "\x1b[20l"         // LNM (ANSI, no ?)

    // Display modes.
    seqReverseColorsOff     = "\x1b[?5l"
    seqOriginModeOff        = "\x1b[?6l"
    seqWraparoundOn         = "\x1b[?7h"         // default-on
    seqSlowScrollOff        = "\x1b[?4l"
    seqReverseWrapOff       = "\x1b[?45l"
    seqReverseWrapExtOff    = "\x1b[?1045l"
    seq132ColumnOff         = "\x1b[?3l"
    seqSyncOutputOff        = "\x1b[?2026l"
    seqGraphemeClusterOff   = "\x1b[?2027l"
    seqReportColorSchemeOff = "\x1b[?2031l"
    seqInBandSizeReportsOff = "\x1b[?2048l"

    // Layout.
    seqLeftRightMarginOff   = "\x1b[?69l"
    seqScrollRegionReset    = "\x1b[r"
    seqCursorShapeDefault   = "\x1b[0 q"

    // Charset.
    seqCharsetG0Ascii       = "\x0f\x1b(B"       // SI + ESC ( B

    // Alt screens.
    seqAltScreen47Off       = "\x1b[?47l"
    seqAltScreen1047Off     = "\x1b[?1047l"
    seqAltScreen1049Off     = "\x1b[?1049l"

    // Visual reset.
    seqSGRReset             = "\x1b[0m"
    seqCursorShow           = "\x1b[?25h"
    seqClearScreen          = "\x1b[H\x1b[2J"
)
```

### Integration with `runAttachLoop` and `runServerLoop`

The `attach.go` diff is mostly deletions:

- Delete the `seqXxx` constant block (679–732); the new file has its own (larger).
- Delete `resetTermModes` (742–750).
- Delete `terminalModeTracker` struct + methods (760–880).
- Delete helpers `containsKittyKbdSet`, `parseCSIParamDefault`.

Adds:

- `attachOptions` grows a `Restorer string` field; `attachOptionsFromFlags` parses it.
- `cmdAttach`: add `--restorer` flag.
- `runAttachLoop` constructs a `TerminalRestorer` via `newRestorer(opts.Restorer)`.
- `runConnectLoop` / `runSession` / `runServerLoop` take `restorer TerminalRestorer` instead of `modeTracker *terminalModeTracker`. Same number of args; pure rename + type change.

Edits in `runServerLoop`. The once-only Attach gate is the existing `attached` atomic, transitioned via CompareAndSwap so the first arriving snapshot wins:

```go
case attachwire.MsgSnapshotScrollback, attachwire.MsgSnapshotScreen:
    if !connectEmitted {
        emitConnect(writers.stdout, label)
        connectEmitted = true
    }
    if attached.CompareAndSwap(false, true) {
        if err := restorer.Attach(writers.stdout); err != nil {
            return err
        }
    }
    if typ == attachwire.MsgSnapshotScreen {
        // Per-snapshot pre-paint clear; distinct from dtachRestorer's
        // once-per-attach clear.
        if _, werr := writers.stdout.Write([]byte("\x1b[H\x1b[2J")); werr != nil {
            return werr
        }
    }
    if len(payload) > 0 {
        if _, werr := writers.stdout.Write(payload); werr != nil {
            return werr
        }
    }
    restorer.Observe(payload)

case attachwire.MsgOutput:
    if _, werr := writers.stdout.Write(payload); werr != nil {
        return werr
    }
    restorer.Observe(payload)
```

`connectEmitted` stays per-session (resets per reconnect, so the banner re-emits on each reconnect — same as today). `attached` is process-scoped via `CompareAndSwap(false, true)` — only the very first snapshot ever calls `restorer.Attach`. Across reconnects, `Attach` is NOT re-called.

`emitDetach` simplifies to:

```go
func emitDetach(w io.Writer, label attachLabel, restorer TerminalRestorer) {
    if restorer != nil {
        _ = restorer.Cleanup(w)
    }
    fmt.Fprintf(w, "\r\n[disconnected. %s @ %s]\r\n", label.Session, label.Host)
}
```

The disconnect banner stays in `emitDetach`, *not* in any restorer. We swallow `Cleanup`'s error: this is the final write before the deferred function returns, and any failure mode (closed stdout, broken pipe) means the user already isn't seeing output anyway.

### Tests (one file: `attach_restorer_test.go`)

```
TestNewRestorer                                    // valid names return correct types; invalid name errors out
TestDtachRestorer_AttachClearsScreen               // Attach writes "\x1b[H\x1b[2J" to a bytes.Buffer
TestDtachRestorer_Cleanup                          // Cleanup writes "\x1b[?25h" to a bytes.Buffer
TestDtachRestorer_ObserveIsNoOp                    // Observe doesn't change Cleanup output
TestHootRestorer_AttachPushesKittyFrame            // Attach writes "\x1b[>u"; kittyPops becomes 1
TestHootRestorer_AttachWriteErrorPreservesState    // Attach with failing writer returns err and leaves kittyPops at 0
TestHootRestorer_CleanupAfterAttach                // captured cleanup bytes match the catalogue-ordered concatenation including OSC 9;4 and the initial kitty pop
TestHootRestorer_CleanupClearsOSCProgress          // explicit Ghostty regression test, named for the bug
TestHootRestorer_CleanupContainsAllCatalogueEntries // for each catalogue "emit" decision, assert the corresponding seqXxx is in the captured Cleanup output
TestHootRestorer_ObservesKittyPushPop              // live CSI > u + CSI < u balance
TestHootRestorer_ObservesModifyOtherKeys           // CSI > 4 ; 2 m sets; CSI > 4 ; 0 m clears
TestHootRestorer_ObservesSplitCSI                  // \x1b[> + 1u arrives in two Observe calls
TestEmitDetachUsesCleanupBeforeBanner              // restorer cleanup bytes precede the [disconnected.] line
TestEmitDetachWithNilRestorer                      // safe (back-compat with the old emitDetach(_, _, nil) test path)
```

`TestHootRestorer_CleanupContainsAllCatalogueEntries` is the catalogue-coverage guard: it iterates a list of `(name, seqXxx)` pairs derived from the catalogue table and asserts each appears in `Cleanup()`'s output. Adding a new catalogue entry without updating Cleanup will fail this test.

## Decisions and alternatives considered

### Why the comprehensive enumeration, not "just OSC 9;4"

The human's framing earlier: "as we keep adding modes, this is going to get messier." The previous spec rev landed an obvious-one-line-per-mode shape but kept the actual cleanup list at today's behavior + OSC 9;4. The human's redirect this rev: scour Ghostty, no Future Work.

Reasoning that motivates "do it now":

- The `seqXxx`-block + `resetTermModes` shape was bad partly because adding modes was awkward. With the new shape (one `Cleanup()` method, clear sections, named constants), adding 25 more modes is mechanical. The cost is low.
- Each mode that's "obviously safe to reset unconditionally" but isn't reset today is a latent bug waiting to be hit. The codex spec's Future Work list (app cursor keys, OSC 104/110/111/112) is half-right and half-wrong: app cursor keys IS safe to reset unconditionally; OSC 104/110/111/112 is NOT (lease problem). Treating them as one bucket of "Future Work" misses that distinction.
- Going through `modes.zig` line-by-line is the right level of rigor for a "scour" pass. Doing it once and recording the decision in the catalogue means future contributors don't re-litigate "should we reset DECCKM."

### Why we skip OSC color/title/clipboard explicitly (not Future Work)

These are NOT deferred work. They're a different shape of problem (lease model required). Calling them "Future Work" sets up the next contributor to add them in the same `hootRestorer.Cleanup()` shape, which would clobber user state. The catalogue's "[What we explicitly skip and why](#what-we-explicitly-skip-and-why)" section names each so the decision is recorded.

If someone wants to build the lease model in a follow-up, that's a different design (DECRQM-style query/save/restore on attach + restore on detach), and at that point OSC palette/colors/title slot in. Not coupled to this spec.

### `dtachRestorer` faithfully tracks dtach upstream

Three plausible cuts for the dtach-side restorer:

1. *Track dtach faithfully (picked).* `Attach` writes `\x1b[H\x1b[2J`; `Cleanup` writes `\x1b[?25h`. Matches `crigler/dtach/attach.c:220` and `attach.c:38` byte-for-byte. The control is anchored to a real, reviewable upstream.
2. *Cursor-show only.* What an earlier rev called `minimalRestorer`. Drops the on-attach screen-clear. Slightly cleaner from hoot's POV but stops being a real dtach comparison — it's now just "the smallest thing we could do."
3. *Cursor + kitty pop (since hoot pushes a frame at attach anyway, dtach mode could pop one for free).* Doesn't track dtach. Discard.

Picked (1) on the principle that an experimental control should anchor to a real reference, not a designed-by-us minimum.

**Subtlety: `dtachRestorer.Attach()`'s clear erases the `[connected.]` banner.** dtach has no banner upstream; cosmetically odd from hoot's POV. Fix-if-needed = swap call order in `runServerLoop`.

### Why two impls behind an interface

The human wants to experiment with two attach/detach modes; that's the second consumer the previous rev's "no second consumer, YAGNI" pushback was missing.

What's right about the per-restorer-policy interface (vs the abandoned per-`Mode` interface): the actual variation is across whole policies (hoot vs dtach), not across modes within a policy. Inside `hootRestorer`, modes are just lines in `Cleanup()`.

### Why one file (not many)

Two files (`attach_restorer.go` + test). The earlier per-mode-per-file shape was ten files for what's really one logical unit. Even with the comprehensive enumeration, one file is right: the `Cleanup()` body reads top-to-bottom as the catalogue, the constants block is the same shape, the diff is cohesive.

### Always-push the kitty frame at attach (codex's simplification)

Codex's proposal: drop the snapshot-byte-sniff; always push at attach time. Costs 4 extra bytes on attaches against terminals that never had kitty state; saves ~30 lines of `containsKittyKbdSet` + per-snapshot push-detection logic.

Adopted. The 4-byte tax is invisible on terminals without kitty kbd support; the simpler model removes a class of "scan missed something" failure modes.

### OSC 9;4: always-emit vs sniff-and-conditional

Always-emit. On terminals that never set progress, no-op state transition. Sniff-conditional would require extending the observer to OSC parsing for zero observable benefit.

### Reconnects are transparent retry, not fresh attach/restore cycles

`Attach` is called exactly once per process (gated in `runServerLoop` by `attached.CompareAndSwap(false, true)`). `Cleanup` is called exactly once at outer exit. Drops in between are *not* full attach/detach cycles.

Why not fresh-cycle-per-reconnect:

- Hoot's reconnect philosophy is "stay raw, repaint snapshot, transparent retry" (see `attach.go:217`'s "stays raw across reconnects so we don't flicker between drops" comment). Emitting `Cleanup()` on every drop would write ~150 bytes including alt-screen-leave and `\x1b[H\x1b[2J` — the user's pre-attach shell history flashes briefly back, then gets cleared, then the reconnect snapshot repaints. That's flicker on every transient network blip.
- The reconnect snapshot itself re-establishes inner-program state (libghostty's `formatter.zig:383` re-emits `CSI > 4 ; 2 m` if modifyOtherKeys was set; kitty kbd flags get restored via `CSI = ... u`; alt-screen gets re-entered). A clear-then-resnapshot round-trip is wasted bytes — the snapshot was going to undo the clear.

A *partial* cycle would be cheap and might be worth doing if observation drift turns up: pop-and-re-push the kitty kbd frame on each drop (8 invisible bytes; symmetry guarantee that every Hoot push is paired with exactly one pop, regardless of how long the session runs). Filed as a possible refinement, not done now — there's no evidence pop-count drift happens in practice.

The interface as specified makes this easy to revisit: if we ever want session-scoped cleanup, we add an `EndSession(w io.Writer) error` method to `TerminalRestorer` and call it from `runConnectLoop` on each drop. No refactor of `hootRestorer`'s state model required.

### Why a runtime flag, not build tag or env var

`--restorer` is per-attach, easy to flip in a single shell. Build tags require recompile; env vars are global to a shell session. Runtime flag is the right grain for "experiment."

### Why no server-side typed-event scanner

Wire-protocol changes are expensive; the CLI already parses output bytes; no second consumer for typed events today. Out of scope.

### Why the disconnect banner stays out of the interface

Banner is local CLI UX copy, not terminal-state hygiene. `emitDetach` owns the banner; restorer owns the bytes-to-the-terminal cleanup.

## Implementation surface (file-by-file)

### New files

- `cmd/hoot/attach_restorer.go` — `TerminalRestorer` interface, `newRestorer`, `dtachRestorer`, `hootRestorer`, all `seqXxx` constants. Estimated ~250 lines.
- `cmd/hoot/attach_restorer_test.go` — 13 tests listed above.

### Edits

- `cmd/hoot/attach.go`:
  - `attachOptions`: add `Restorer string`.
  - `attachOptionsFromFlags`: thread `restorerName` through.
  - `cmdAttach`: add `--restorer` flag.
  - Delete `seqXxx` constant block, `resetTermModes`, `terminalModeTracker` struct + methods, `containsKittyKbdSet`, `parseCSIParamDefault`.
  - `runAttachLoop`: replace `modeTracker := &terminalModeTracker{}` with `restorer, err := newRestorer(...)`.
  - `runConnectLoop` / `runSession` / `runServerLoop`: rename param `modeTracker *terminalModeTracker` → `restorer TerminalRestorer`.
  - `runServerLoop`: replace `pushKittyKbdForSnapshot` callsites with `restorer.Attach`; replace `modeTracker.observe` with `restorer.Observe`.
  - `emitDetach`: replace `tracker.detachReset() + resetTermModes` with `restorer.Cleanup()`.

### Deletions

- `cmd/hoot/attach_term_modes_test.go` — subsumed. Mapping for the reviewer:
  - `TestResetTermModesOrder` → `TestHootRestorer_CleanupAfterAttach` + `TestHootRestorer_CleanupContainsAllCatalogueEntries`.
  - `TestEmitDetachWritesModeResetBeforeBanner` → `TestEmitDetachUsesCleanupBeforeBanner`.
  - `TestTerminalModeTrackerPushesSnapshotKittyState` → behavior changed (always-push). Replaced by `TestHootRestorer_AttachPushesKittyFrame`.
  - `TestTerminalModeTrackerBalancesLiveKittyPushPop` → `TestHootRestorer_ObservesKittyPushPop`.
  - `TestTerminalModeTrackerLivePopCanConsumeSnapshotFrame` → covered by the combined attach+observe+cleanup tests.
  - `TestTerminalModeTrackerObservesSplitKittyPush` → `TestHootRestorer_ObservesSplitCSI`.
  - `TestTerminalModeTrackerObservesModifyOtherKeys` → `TestHootRestorer_ObservesModifyOtherKeys`.

### Total expected diff

- ~250 lines added (`attach_restorer.go`).
- ~200 lines added (`attach_restorer_test.go`).
- ~250 lines deleted from `attach.go`.
- ~100 lines deleted (`attach_term_modes_test.go`).
- Net: roughly +100 lines in `cmd/hoot/`, plus the comprehensive cleanup, two restorer policies, and a new flag.

## Verification

Unit tests:

```bash
cd repos/github.com/hayeah/hootty
go test ./cmd/hoot/ -run 'TestNewRestorer|TestDtachRestorer|TestHootRestorer|TestEmitDetach'
go test ./cmd/hoot/   # full package
go test ./...         # full repo
```

**Primary verification: e2e libghostty test** (`cmd/hoot/attach_restorer_e2e_test.go::TestHootRestorer_E2EClearsCatalogueModes`). Drives a full attach-detach cycle through libghostty on both ends:

- Supervisor (libghostty PTY) is fed raw escape bytes that SET a representative slice of the catalogue: alt screen, focus events, mouse normal, mouse button, mouse SGR, bracketed paste, synchronized output, cursor-hidden, modifyOtherKeys, kitty keyboard push, OSC 9;4 progress.
- A `LocalTerminal` (libghostty user-side) attaches; the snapshot serializes the supervisor's mode state into the user-side terminal. Mid-attach we assert via libghostty's native `ActiveScreen()` / `CursorVisible()` queries that the modes did transfer (otherwise the post-cleanup assertions would be vacuously true).
- The session is closed and `emitDetach(local, ..., restorer)` runs `hootRestorer.Cleanup` against the user-side libghostty.
- We assert post-cleanup state via libghostty's native queries: `active=primary`, `visible=true`, and the formatter's VT-with-modes serialization no longer contains `[?1004h`, `[?1000h`, `[?1002h`, `[?1006h`, `[?2004h`, `[?2026h`, `[?1049h`.

This is stronger than asserting on the bytes the restorer emits: it asserts that those bytes, when applied to a real libghostty terminal, actually move it back to defaults. If we ever change a constant from `\x1b[?1004l` to a typo'd or wrong sequence, the unit-test catalogue check still passes (the constant is in the slice) but THIS test fails (the user terminal still has focus events on after cleanup).

Manual smoke (optional, only when the e2e test isn't enough):

- **Hoot smoke (the codex Ghostty regression):** Hoot session + Claude Code in Ghostty. `/usage` opens; progress strip appears. `hoot detach <key>` from another shell. Expected: attach exits, strip clears.
- **dtach smoke (the experimental control):** `hoot attach --restorer=dtach <key>`. Expected: attach clears screen; banner wipes; on detach only cursor-show emitted; state intentionally leaks.

## Open questions

1. **Catalogue completeness.** The catalogue is comprehensive against today's `modes.zig` (40 entries) plus the non-ModeState state Ghostty handles. If Ghostty adds a mode after this lands, the catalogue table in spec.md should be updated and the test `TestHootRestorer_CleanupContainsAllCatalogueEntries` should fail until the new mode is decided. Is this discipline OK, or should we generate the catalogue from the Ghostty header file (`include/ghostty/vt/modes.h`) automatically? My read: manual is fine — additions are rare and the per-row decision IS the work.

2. **`dtachRestorer.Attach()` clears the connect banner.** Faithful to upstream dtach (no banner). If the rfc reviewer prefers banner preserved in dtach mode, fix is a one-line swap in `runServerLoop`.

3. **Visibility of `--restorer` in `--help`.** Default to visible; user is expected to flip during experiments.

4. **Cursor-shape reset (`CSI 0 SP q`).** Most terminals interpret this as "use terminal default shape"; some (older) terminals interpret 0 as "blink-block." If reports surface, we can change to `CSI 1 SP q` (explicit blink-block) or omit. Flagging because this is the only catalogue entry where I'm not 100% sure of cross-terminal behavior.

5. **Should we observe-and-conditionally-emit any of the catalogue's "emit" entries?** The default is "always-emit" because it's simpler and the bytes-on-the-wire cost is invisible. If during smoke any emit causes a visible glitch (e.g. cursor-shape reset misbehaves), the fallback is to make that one entry observer-driven (sniff CSI for "TUI set this mode" → emit reset only if seen). The infra is already there for kitty + modifyOtherKeys.

## Appendix: codex diagnosis

The diagnosis sections of the original codex spec are unchanged and authoritative.

### Ghostty's indicator is OSC 9;4 progress

- `repos/github.com/ghostty-org/ghostty/src/terminal/osc.zig:123` defines `conemu_progress_report`.
- `repos/github.com/ghostty-org/ghostty/src/terminal/osc.zig:199` defines states: `remove`, `set`, `error`, `indeterminate`, `pause`.
- `repos/github.com/ghostty-org/ghostty/src/terminal/osc/parsers/osc9.zig:144` parses OSC 9;4:
  - `OSC 9;4;0;...` → `.remove`.
  - `OSC 9;4;1;N` → `.set` with optional progress 0–100.
  - `OSC 9;4;2;N` → `.error`.
  - `OSC 9;4;3` → `.indeterminate`.
  - `OSC 9;4;4;N` → `.pause`.
- `repos/github.com/ghostty-org/ghostty/src/apprt/gtk/class/surface.zig:1001` applies it to the GTK surface's `progress_bar_overlay`.

### What clears OSC 9;4 in Ghostty

```text
ESC ] 9 ; 4 ; 0 ; BEL
```

(or ST-terminated equivalent). RIS (`ESC c`) also clears, but is too broad. Targeted OSC 9;4;0 is the right tool.

### What Ghostty does on PTY EOF

PTY EOF does *not* run terminal reset or progress clear. In the Hoot repro, Ghostty's child is the user's shell; `hoot attach` exits back to the shell, so there is no Ghostty PTY EOF. The progress state remains because Claude Code on the supervisor never gets a chance to emit the remove.

### dtach upstream (what `dtachRestorer` tracks)

- `repos/github.com/crigler/dtach/main.c:279` — `tcgetattr(0, &orig_term)`.
- `repos/github.com/crigler/dtach/attach.c:208` — switch local terminal to raw mode.
- `repos/github.com/crigler/dtach/attach.c:220` — clear screen on attach.
- `repos/github.com/crigler/dtach/attach.c:38` — `restore_term`: tcsetattr + write `\x1b[?25h`.
- `repos/github.com/crigler/dtach/attach.c:196` — `restore_term` registered with `atexit`.

Insight: dtach's model is "transparent pipe + local termios guard." It does not solve OSC progress, bracketed paste, mouse modes, kitty keyboard, alternate screen, or color/title leakage. Hoot is different: libghostty formatting means Hoot intentionally materializes remote terminal state into the local terminal. `hootRestorer` defines exactly which terminal modes hoot owns during attach and releases them; `dtachRestorer` is the dtach control variant.

### Ghostty mode source-of-truth

- `repos/github.com/ghostty-org/ghostty/src/terminal/modes.zig:251` — `entries` array; the catalogue in this spec is line-for-line aligned with that table.

## Design notes

- 2026-05-07T13:30Z — Original rev: proposed a `Mode` interface + named-field `attachModes` aggregator with one file per mode (~10 files total). Rejected codex's `TerminalRestorer` interface as YAGNI ("no second consumer").

- 2026-05-07T15:10Z — Pivoted to `TerminalRestorer` interface + two impls in one file. Two corrections from human feedback:
  - "Two attach/detach modes" IS the second consumer. Codex was right.
  - "Breaking into so many fragments is too fine-grained." Walked back per-mode-per-file shape; modes are just lines in `Cleanup()`.

- 2026-05-07T15:14Z — Adopted codex's always-push kitty simplification: drop snapshot-byte-sniff in favor of unconditional push at attach.

- 2026-05-07T15:35Z — Renamed restorers to `hoot` and `dtach`, anchoring the control to a real upstream reference rather than a designed-by-us "minimal." `dtachRestorer.Attach()` matches `dtach/attach.c:220` (clear screen); `dtachRestorer.Cleanup()` matches `dtach/attach.c:38` (cursor show).

- 2026-05-07T16:30Z — Two cleanups to the interface contract, both prompted by human pushback:
  - **Dropped `sync.Once` from `Attach`'s contract.** Previous spec said implementations must be idempotent (Attach called per-snapshot, gate with `sync.Once`). Cleaner: move the once-only gate to `runServerLoop`'s existing `attached` atomic via `CompareAndSwap(false, true)`. The restorer's contract becomes "Attach exactly once, then Observes, then Cleanup exactly once" — no idempotency burden, no `sync.Once` field on either restorer struct.
  - **`Cleanup() string` → `Cleanup(w io.Writer) error`.** Symmetric with `Attach(w io.Writer) error`; no accumulator allocation; more honest typing for "raw bytes destined for an io.Writer." `string` was a lazy holdover from the existing code's `const resetTermModes string`. Tests use `bytes.Buffer` to capture (which they already would have).
  - Considered briefly: `Cleanup() []byte`. Better than `string` (more honest), worse than direct-write (still allocates a buffer). Discarded.
- 2026-05-07T16:32Z — Documented the "transparent retry" reconnect model explicitly in a new Decisions subsection. Human asked whether reconnect should be a fresh attach/restore cycle. Answer: no — hoot's existing intent is "stay raw, repaint snapshot, no flicker between drops" (see `attach.go:217`), and full cleanup-on-drop would write ~150 bytes including alt-screen-leave and clear-screen, flashing pre-attach shell history visibly. The reconnect snapshot already re-establishes inner-program state via libghostty serialization; clearing-then-resnapshotting wastes bytes. Filed a partial-cycle refinement (kitty pop+push round-trip on every drop, 8 invisible bytes) as a possible follow-up if pop-count drift ever surfaces; the interface design supports adding `EndSession(w io.Writer) error` later without touching `hootRestorer`'s state model.

- 2026-05-07T16:05Z — **Comprehensive cleanup pass.** Human pivot: no Future Work; do it as thoroughly as is correct. Scoured `repos/github.com/ghostty-org/ghostty/src/terminal/modes.zig:251` (40 mode entries) plus the non-ModeState terminal state Ghostty handles. Built the catalogue table; per-row decision (emit / observe / skip-default / skip-cursor-side-effect / skip-niche / skip-lease-required / already).
  - Net new emits in `hootRestorer.Cleanup()`: ~25 byte-strings. Largest categories: mouse modes (was 2, now 8), keyboard input modes (was 2, now 11), display modes (0 → 11). Roughly +150 bytes per detach.
  - Categorically *not* added: OSC 4/5/10–19 + 104/105/110–119 (palette + dynamic colors). These require a save/restore lease because the user's shell may set them pre-attach and observation alone doesn't restore correctly. Same reasoning excludes OSC 0/1/2 (window title), OSC 7 (pwd), OSC 22 (mouse shape), OSC 21 (kitty colors). Recorded in [What we explicitly skip and why](#what-we-explicitly-skip-and-why) so the next contributor doesn't reach for "always-emit OSC 110 0 ST" thinking it's safe.
  - **Why the lease-model class is documented separately, not as Future Work:** "Future Work" implies "same approach, just not yet." The lease model is a different approach (DECRQM-style query before set, restore on detach) and slotting OSC palette resets into `hootRestorer.Cleanup()` would actively clobber user state. Naming it "skipped + reason" rather than "future" prevents future contributors from defaulting to the wrong shape.
  - Catalogue lives in spec.md, with `TestHootRestorer_CleanupContainsAllCatalogueEntries` as the coverage guard. Adding a new Ghostty mode → spec table grows + one new constant + one new line in `Cleanup()`. The discipline is "decide once, record the decision, test enforces."

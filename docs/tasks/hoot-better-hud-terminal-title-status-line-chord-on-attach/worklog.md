---
status: done
section: Hoot — better HUD: terminal title + status-line chord on attach
slug: hoot-better-hud-terminal-title-status-line-chord-on-attach
mode: worktree
spec:
created: 2026-05-07T07:15:57Z
---

> ## Hoot — better HUD: terminal title + status-line chord on attach
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`. Direct implementation (boss todo). 
>
> Note: there's a parallel section in flight (`hoot-detach-attach-cleaner-internal-api-for-terminal-mode-cleanup`, slug `pga`) refactoring the terminal-mode cleanup machinery. **Do not** depend on that refactor; do whatever fits today's shape. If both land, a follow-up can fold any title-restore logic into the new `Mode` aggregator. If pga lands first and conflicts, rebase and adapt.
>
> ### Goal
>
> When inside a `hoot attach` session, the user often loses track of *which* session they're in. Two affordances:
>
> 1. **Terminal title** set on attach to a one-line summary of the session, in the same vocabulary as the fzf picker line (without the markers — just the human-readable bits):
>    ```
>    🦉 [id] @host path cmd...
>    ```
>    - `[id]` is the short id.
>    - `@host` only appears if not local (`@local` is suppressed).
>    - `path` is the tildified cwd.
>    - `cmd...` is argv joined with spaces, truncated tastefully if long (the user's `...` suggests this is intentional).
>    - The 🦉 prefix is the brand mark and is always there.
>    - Set via OSC 0 (icon + window title) or OSC 2 (window title only). OSC 2 is more polite — leaves the icon name alone. Pick one and document.
> 2. **`prefix + ?` chord** prints a status line into the local terminal (one line, doesn't disturb the inner program's cursor more than necessary). Same content/format as the title above. Use the same prefix-key plumbing as the existing chord set (see `prefix.go` / wherever the chord dispatcher lives).
>
> ### Reuse
>
> The fzf picker line is built by `internal/sessionpick.Format`. Either:
> - Add a sibling `FormatHUD(...)` that drops the markers (`@host` only when non-local, no `[id]` brackets if you want, no `*attached` tag, etc.), OR
> - Add a more raw constructor and let both Format and FormatHUD compose from it.
>
> Pick whichever is cleaner. Don't duplicate the field-extraction logic.
>
> ### Title lifecycle
>
> - **Set on attach** — somewhere after the connection is established and before live forwarding starts.
> - **Restore on detach** — what to restore to? Options:
>   - Save the prior title with `OSC 21 ; "" ST` (CSI title push) and restore with `OSC 22 ; "" ST` on detach. Modern xterm/iTerm/Ghostty/kitty all support the title stack.
>   - Just clear with `OSC 2 ; ; ST` and let the shell reassert on next prompt redraw.
>   - Pick the title-stack approach if the test/manual smoke shows it works on Ghostty + iTerm2 + Terminal.app; fall back to clear-on-detach otherwise.
> - **Don't fight the inner program**: many TUIs (vim, claude code, etc.) set their own titles. Setting hoot's title at attach-start is fine — if the inner program overrides it, that's their prerogative; we're not in a refresh loop. The `?` chord is the user's escape hatch when they need a reminder.
>
> ### Status-line chord (`prefix + ?`)
>
> - Find the existing prefix-key dispatcher (likely `cmd/hoot/prefix.go` or similar).
> - Add `?` as a new chord that emits the HUD line to local stdout, prefixed/suffixed with `\r\n` so it lands on a fresh line. Format: same string as the title (without the title-set escape — just the visible text).
> - The line should be terse enough to not disturb a TUI repaint badly. Most TUIs repaint on next input anyway.
>
> ### CLI / docs
>
> - Update `hoot attach -h` to mention the title behavior and the `?` chord.
> - README under the attach section: one paragraph + an example.
> - No new flags unless something forces one (e.g. `--no-title` if reviewer demands; default off until asked).
>
> ### Tests
>
> - Unit test for the HUD-line constructor — given a session shape (id, host, cwd, argv), assert the formatted string. Cover: local vs remote host, long argv truncation, missing argv, special chars in cwd.
> - Unit test for the title-set bytes — given a HUD line, the OSC sequence has the right shape (`\x1b]2;<text>\x07` or `\x1b]2;<text>\x1b\\`).
> - Unit test for the title push/pop bytes if we go that route.
> - Unit test for the `?` chord dispatcher: when the chord fires, the expected bytes go to stdout.
> - Smoke: spawn a session, attach from inside Ghostty/iTerm, eyeball the title; hit `prefix + ?`; verify the status line appears; detach, verify the title restores or clears.
>
> ### Surface
>
> - [ ] implement and verify
>   - HUD-line constructor (likely in `internal/sessionpick/`)
>   - title set on attach + restore/clear on detach
>   - `prefix + ?` chord
>   - `-h` + README updates
>   - tests above
>   - evidence: `go test ./...` tail + a smoke transcript / screenshot description

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Add `FormatHUD(SessionWithMeta) string` to `internal/sessionpick/` (🦉 [id] @host path cmd... — drop @local, truncate cmd at ~60 chars)
- [x] Unit tests for `FormatHUD` (local/remote, long argv truncation, missing argv, special chars)
- [x] Add `cmd/hoot/title.go` with `hudTitleSetBytes`, `hudTitlePushBytes`, `hudTitlePopBytes` helpers (OSC 2 + CSI 22;2t / 23;2t)
- [x] Unit tests for title bytes
- [x] Wire HUD line into `cmdAttach`: fetch `SessionWithMeta` for resolved key, compute HUD string, pass into `runAttachLoop`
- [x] Push+set title on attach start (only when stdout is a tty); pop title in `emitDetach`
- [x] Repurpose `?` chord to print the HUD line + command help on stdout (replaces bare-help; same chord byte)
- [x] Unit test for `?` chord dispatching the HUD-print action
- [x] Update `hoot attach -h` to mention title behavior + `?` chord
- [x] Update README attach section: paragraph + example for title + `?`
- [x] `go test ./...` green; capture tail for evidence
- [x] Smoke transcript: `hoot run` → `hoot attach`, eyeball title; `prefix+?`; detach; verify clear

## Agent log
- 2026-05-07T07:20Z FormatHUD + tests landed (e9bbf1f)
- 2026-05-07T07:26Z Title + status-chord wired through attach (10afe1f)
- 2026-05-07T07:32Z Docs + smoke landed (e79af95, b1f06ea); evidence written; setting done

## Boss log

## Evidence

### Commits

- `e9bbf1f` Add FormatHUD to sessionpick for terminal title + status chord
- `10afe1f` Wire HUD title + status chord into hoot attach
- `e79af95` Document HUD title + status chord in -h and README
- `b1f06ea` Sequence HUD onAttach after term.MakeRaw

### `go test ./...` (uncached)

```
ok  	github.com/hayeah/hootty	7.008s
ok  	github.com/hayeah/hootty/cmd/hoot	8.455s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	1.727s
ok  	github.com/hayeah/hootty/internal/shortid	1.845s
ok  	github.com/hayeah/hootty/internal/sshtransport	2.014s
```

### New unit tests

- `internal/sessionpick/sessionpick_test.go::TestFormatHUD` — local vs remote, missing argv, long argv truncation (60-rune cap with "…"), unicode in cwd, spaces in argv, nil state, rune-safe truncation across multi-byte runes.
- `cmd/hoot/title_test.go::Test{HudTitleSetBytes, HudTitleSetBytesSanitizesControl, HudTitleSetBytesPreservesUTF8, HudTitlePushPopConstants, SanitizeTitleHotPath, HUDStateOnAttachEmitsPushAndSet, HUDStateOnAttachIdempotentPush, HUDStateOnDetachOnlyPopsIfPushed, HUDStateNonTTYNoEmit, HUDStateEmptyLineNoEmit, HUDStatePrintChord, HUDStatePrintChordEmpty}`.

### Smoke (`tmp/smoke_attach.py` against a local session)

The harness pty-forks `hoot attach`, sends `<prefix>?` then `<prefix>.`, captures stdout. All five assertions passed (post-`b1f06ea`, no canonical-mode echo of typed prefix):

```
=== captured length: 534 bytes ===
[22;2t]2;🦉 smoke1 ~/Dropbox/.../hootty bash -lc while true; do sleep 1; done
[2m🦉 smoke1 ~/Dropbox/.../hootty bash -lc while true; do sleep 1; done[0m
[2m[hoot] commands: "." detach · "^" literal prefix · "Ctrl-Z" suspend · "c" clone · "?" help[0m

[connected. smoke1 @ local]
[H[2J[0m[1;1H[?1004l[?2004l[?1006l[?1000l[?1049l[0m[?25h[H[2J
[disconnected. smoke1 @ local]
[23;2t
=== assertions ===
✅ title push (CSI 22;2t)
✅ OSC 2 set-title with owl
✅ HUD chord output (🦉 ... after prefix+?)
✅ chord help line (detach,clone)
✅ title pop (CSI 23;2t) on detach
```

Full log: `tmp/smoke_attach.log`. Harness: `tmp/smoke_attach.py`.

### `hoot attach -h` excerpt

```
Inside an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte to the remote; <prefix>Ctrl-Z suspends hoot
attach (resume with fg); <prefix>c clones the session; <prefix>?
prints the HUD line for the current session and the chord help.

On attach the terminal window title is set to a one-line summary in
the form `🦉 <id> [@host] <cwd> [<cmd...>]` (`@host` omitted for local
sessions, argv truncated at 60 runes). The previous title is saved
via the xterm title stack (CSI 22;2t) and restored (CSI 23;2t) on
detach. Inner TUIs that set their own title will override hoot's;
press `<prefix> ?` for a reminder of which session you're in.
```

## Trouble report

- The `<prefix> ?` chord previously printed a one-line help on **stderr**. The new behavior prints the HUD line plus the same chord help on **stdout** (per spec wording: "emits the HUD line to local stdout"). Spec said "Same content/format as the title above"; I kept the chord-help line as well since dropping it would be a regression for users who hit `?` expecting docs. Combined output is two lines, both dim-SGR, leading + trailing CRLF.
- `loadHUDLine` is best-effort — if the StateFile read or `/sessions` GET fails, we attach with an empty HUD line, which suppresses both the title set and the chord print (cosmetic-only feature, never blocks attach). For the remote path it does add one extra `GET /sessions` round-trip on attach; updated `TestCmdRunRemoteAttachSpawnsThenAttaches` to handle it.
- Title push was initially emitted **before** `term.MakeRaw`, so an early-typed prefix could get echoed by canonical-mode line discipline. Smoke transcript showed this as `^^?` near the title bytes. Moved the `hud.onAttach` call to right after `MakeRaw` (commit `b1f06ea`); title still appears immediately on attach (before runConnectLoop), no echo.
- Diagnostic `errorsastype` and `minmax` lints fire on pre-existing code in `attach.go`; not touched in this section.

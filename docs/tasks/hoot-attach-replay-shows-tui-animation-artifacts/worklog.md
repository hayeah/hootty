---
status: done
section: Hoot attach replay shows TUI animation artifacts
slug: hoot-attach-replay-shows-tui-animation-artifacts
mode: worktree
spec: spec.md
created: 2026-05-06T09:09:54Z
---

> ## Hoot attach replay shows TUI animation artifacts
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> On attach, hoot replays scrollback so the reattaching client sees the prior session state. Symptom: with TUIs like Claude Code, the replay visibly *re-animates* the prior session — you can see `/usage` opening, ESC dismissing it, prompts being typed — instead of a quick jump-cut to the final screen. Final rendered state is correct; the journey to get there is the problem.
>
> Desired behavior: a quick restore that lands on the current screen with reasonable scrollback, without re-driving the TUI through past frames.
>
> Research questions for `spec.md`:
> - What does hoot actually store in its scrollback/replay buffer today? Raw PTY bytes (incl. CSI/cursor moves), or a parsed terminal grid, or both? Where in the code (point at files/functions).
> - Why does the replay re-animate? Is it because we're piping the entire historical byte stream — including in-place cursor moves that re-perform animations — into the new client's terminal? Or is it a terminal-state restore that's mis-timed?
> - What do comparable tools do (tmux's `attach` + `capture-pane` / scrollback, dvtm, abduco, mosh, ghostty's session restore)? One paragraph each, with pointers.
> - Options to consider, with tradeoffs:
>   - Send only a final-screen snapshot (parsed grid → rewrite to ANSI) on attach; keep raw bytes only for true scrollback above the screen.
>   - Strip cursor-position/animation control sequences from the replay, keep SGR (color/formatting).
>   - Plain-text-only scrollback (lose color but cheap and unambiguous).
>   - Hybrid: snapshot of current screen + plain-or-stripped scrollback above.
> - Recommendation: pick one, justify, sketch the implementation surface (which file/function changes, any new dependencies like a VT parser).
>
> Don't write code yet. Produce `spec.md` as a research + design note answering the above. The human will read and decide before implementation.
>
> - [ ] rfc: review spec.md (research findings + recommendation)
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### phase 1 — research + spec (rfc gate)

- [x] check out hootty worktree, survey attach/replay code paths
- [x] read attach_handler.go, recorder.go, pty_libghostty.go, vt*_filter/stripper.go to nail down current behavior
- [x] write `spec.md` answering the section's research questions + recommendation
- [ ] **rfc gate** — human reviews `spec.md` and ticks the rfc box (I do not tick top-level boxes; status is `blocked` until the boss nudges)

### phase 2 — implement per spec (after rfc tick)

Cinema excision:
- [x] remove `sendAsciiCinemaPlayback`/`streamAsciiCinemaPlayback` + constants in `attach_handler.go`; simplify the live-buffering goroutine (no playback phase needed)
- [x] drop `Hello.AsciiCinemaPlayback*` fields from `internal/attachwire/wire.go`
- [x] drop `--ascii-cinema-playback*` flags from `cmd/hoot/attach.go` + `cmd/hoot/run.go` + plumbing in `cmd/hoot/main.go`
- [x] ~~delete `ReadAsciicastOutputEvents`~~ — kept; still useful for the future `hoot replay` verb / recorder-format work and has its own tests in `recorder_test.go`. Cheaper to leave than to delete and re-add.
- [x] prune cinema-specific test cases across `attach_handler_test.go`, `cmd/hoot/attach_golden_test.go`, `cmd/hoot/run_attach_test.go` (`recorder_test.go`/`pty_libghostty_test.go` had no cinema cases)
- [x] update README attach usage block — remove cinema flags
- [x] update `docs/cross-compile.md` smoke-test snippet (out-of-spec but caught during sweep)

Browser bridge fix:
- [x] `cmd/hoot/serve_bridge.go::pumpUpstreamToWS` — forward `MsgSnapshotScrollback` + `MsgSnapshotScreen` as binary WS frames
- [x] xtermjs compatibility (Q2): no special-casing needed at the bridge layer — the snapshot VT bytes already include the formatter's cursor-home + erase-display sequences which xtermjs renders directly. `ExtraKittyKeyboard` left enabled (xtermjs ignores unknown CSI; CLI still benefits). Documented in commit message.
- [x] `cmd/hoot/serve_bridge_pump_test.go` — new unit test against a real httptest websocket round-trip, asserts all three VT-byte frame types reach the browser

Wrap-up:
- [x] full test suite + targeted runs, capture passing tail in `## Evidence`

## Agent log

- 2026-05-06T09:30Z — spawned, set up worktree at `repos/github.com/hayeah/hootty`, surveyed code. Today's attach flow: `attach_handler.go::serveOne` sends `MsgSnapshotScrollback` + `MsgSnapshotScreen` (parsed via libghostty formatter) then re-streams the last 5 min of recorded raw PTY bytes at 8× through `vtQueryStripper`. The stripper only removes terminal *queries* (DA/DSR/CPR/...), not animation/cursor-move CSIs — so TUIs visibly re-animate on top of an already-correct snapshot. Recommendation in spec.md (v1): hard-disable playback on alt-screen sessions, default-off elsewhere, with stage-2 follow-up to render a clean tail through a throwaway libghostty terminal. Status set to `blocked` for the `rfc: review spec.md` gate.

- 2026-05-06T11:30Z — rfc gate ticked. Phase 2 implementation: 4 commits on branch.
  - `018bee0` excised the cinema playback path: removed `sendAsciiCinemaPlayback`/`streamAsciiCinemaPlayback`, `Hello.AsciiCinemaPlayback*` wire fields, `--ascii-cinema-playback*` CLI flags from both `hoot attach` and `hoot run`, simplified the live-buffering goroutine in `serveOne` (no playback phase = no buffering needed), pruned cinema-specific test cases. Net -312 +23 across 9 files.
  - `bc6e9f9` fixed the silently-broken WS bridge: `pumpUpstreamToWS` now forwards `MsgSnapshotScrollback`/`MsgSnapshotScreen` (and silently drops `MsgPing`/`MsgPong`) as binary WS frames. Added `serve_bridge_pump_test.go` exercising the pump end-to-end against a real httptest WebSocket and asserting all three VT-byte frame types arrive byte-identical at the browser.
  - `ddbb350` updated `README.md`: dropped the cinema flags from the synopsis, replaced the playback paragraph with snapshot-only attach narrative.
  - `195b203` swept `docs/cross-compile.md` for the `--no-ascii-cinema-playback` flag in the smoke-test snippet.
  - `go test -count=1 ./...` clean across all packages. `status: done`.

- 2026-05-06T11:00Z — human moved the `pty.cast` binary-format redesign out of scope for this section. Excised the "Recorder format change" section from spec.md and its phase-3 todos from the worklog; demoted to a "Future work" bullet to be filed as a separate boss section. Section is now strictly excise-cinema + fix-bridge. Re-parking at rfc gate.

- 2026-05-06T10:45Z — human asked "do we need to read pty.cast events? can we read from the terminal directly?" — and they're right. The live `term`/`primaryTerm` IS the parsed grid a throwaway would have constructed; `SnapshotParts` already serializes it. Cinema was strictly additive on top of the snapshot. Dropped the rendered-tail mechanic from the spec entirely. Attach fix collapses to "excise cinema + fix bridge". Separately, human flagged that `pty.cast` should become an efficient binary session log with a JSONL converter — added a "Recorder format change" section sketching a length-prefixed binary frame format (`ts|kind|len|payload`), file header (`magic|ver|cols|rows|t0|meta`), and a `hoot cast convert` subcommand. Open question Q1 asks the human whether to ship the format change in this section or split into a follow-up (recommend split). Re-parking at rfc gate.

- 2026-05-06T10:00Z — boss replied with new direction: **excise** cinema playback entirely from attach (per the prior `execute-supervise-...` task's "delete, not default-flip" reasoning), and **promote** the throwaway-terminal rendered-tail idea from deferred stage-2 to the recommended replacement. Recorder (`pty.cast`) stays untouched; future `hoot replay` verb is its consumer. Read both prior specs (`execute-supervise-attach-scrollback-spec-drop-full-replay-strip-terminal-queries/spec.md` and `alt-screen-attach-deliver-full-primary-scrollback-before-entering-alt-screen/spec.md`). Confirmed `cmd/hoot/serve_bridge.go::pumpUpstreamToWS` is independently broken — its default arm (line 322) errors on `MsgSnapshotScrollback`/`MsgSnapshotScreen`, so browser attach has been receiving no snapshot frames in production. Brought it in-scope. Rewrote spec.md end-to-end: excision plan, rendered-tail sketch (size knob, ordering, overlap-with-live-scrollback decision = "rendered tail replaces live scrollback"), browser-bridge fix, future-work `hoot replay` verb, four open questions. Re-seeded phase-2 todos to reflect the new shape. Re-parking at rfc gate.

## Boss log
- 2026-05-06T09:28Z human reviewed your spec and the parallel codex spec. direction confirmed: kill the ascii cinema playback path on attach. keep `pty.cast` on disk untouched (future opt-in `hoot replay`-style tool). the "rendered scrollback through a suitably sized libghostty terminal" idea is the favored direction, not a deferred stage 2. please update spec.md and re-park at the rfc gate.
  
  # prior work you should read first
  
  these are in `$BOSS_ROOT/tasks/` (not the repo) and have load-bearing context. read end-to-end before revising:
  
  1. `~/Dropbox/boss/tasks/execute-supervise-attach-scrollback-spec-drop-full-replay-strip-terminal-queries/spec.md`
     - earlier explicit decision: DELETE full-replay mode, not just default-flip it. arguments: re-issues every terminal query the child ever sent (probe leakage), unbounded `pty.log` size, snapshot dominates on every axis a user cares about, only counter-argument is "exact byte fidelity for debugging the recorder" which is a developer tool not a user feature.
     - your current direction (default-off the cinema playback) is correct in spirit but DELETION (or moving raw-replay behind a separate non-attach verb) aligns better with the documented prior reasoning. consider: does the cinema playback path still earn its keep on the attach surface, or should it be excised entirely? the "keep as opt-in flag" hedge becomes harder to justify once you've read the prior arguments.
     - the "Strategy / Part 1" + "Out of scope" sections are especially relevant — the redesign that "pushes libghostty's render output to clients instead of raw child bytes" was explicitly noted there as the right long-term shape. your stage-2 rendered tail IS that direction.
  
  2. `~/Dropbox/boss/tasks/alt-screen-attach-deliver-full-primary-scrollback-before-entering-alt-screen/spec.md`
     - this is what introduced `LibghosttyPTY.primaryTerm` + `vtPrimaryScreenFilter`. the existing `SnapshotParts` path already concatenates `<primary VT snapshot><alternate VT snapshot>` on alt-screen attach (see `pty_libghostty.go:419`/`:470-482` in your own spec). you do NOT need to reinvent primary-on-alt scrollback — it's there. just stop the cinema playback from re-driving the screen on top of it.
     - your `ActiveScreen()` accessor proposal still makes sense for the "if cinema stays as opt-in, hard-disable it on alt" guardrail. but if we excise cinema entirely, you don't need it.
  
  3. there's a referenced note `supervise-alt-screen-scrollback-gap_claude.md` (probably under `~/Dropbox/notes/2026-05-02/`) — worth a quick read for additional context if you can find it.
  
  # user's direction (verbatim sense)
  
  - "we definitely want to toss the ascii cinema playback" — primary directive.
  - "but we keep timed history (in case we want to provide a playback tool...)" — the recorder + `pty.cast` stay. nothing changes on the recording side. the "playback tool" is a future separate verb, NOT an attach-time feature.
  - "the idea of rendering the scrollbacks in a suitably sized terminal is interesting" — explicit positive signal on your stage-2 idea. promote it from "deferred" to a real proposal in the same spec.
  
  # revise spec.md to reflect
  
  - recommend EXCISING the cinema playback path from attach (not just default-off). justify with the prior-decision arguments from `execute-supervise-...`. flag wiring becomes a removal, not an inversion.
  - promote "rendered tail via throwaway libghostty terminal" from stage 2 to the recommended direction for "give the user back the recent context that cinema-playback was supposedly providing". sketch concretely:
    - on attach, instantiate a fresh `libghostty.Terminal` sized to the session's cols×rows with a generous scrollback (configurable; e.g. session-configured `max_scrollback` or a window like 5min-of-events worth of lines).
    - feed it the playback-window's `"o"` events from `pty.cast` (or a longer window — the bound is now scrollback rows, not wall-clock).
    - format it (with which `Formatter*` knobs? you noted FormatterFormatVT minus cursor needs verification — actually verify by reading `formatter.go` in go-libghostty / the C header). emit as the scrollback prefix BEFORE `MsgSnapshotScrollback` from the live emulator (or replace it — think about ordering and overlap with `term.ScrollbackRows()`).
    - throw the throwaway terminal away.
  - this approach naturally handles alt-screen too: the throwaway emulator collapses past frames into final-rendered cells; no animation possible.
  - separately, propose a future `hoot replay <session>` verb (NOT `hoot attach`) that streams `pty.cast` like asciinema-play to a tty. note as "future work, separate task" — do NOT scope into this section.
  
  # also worth knowing from the parallel codex run (uz2)
  
  NOT for you to read — i'm summarizing the bits with independent value:
  
  - adjacent finding: `cmd/hoot/serve_bridge.go::pumpUpstreamToWS` only forwards `MsgOutput` — appears to ignore `MsgSnapshotScrollback` / `MsgSnapshotScreen` frame tags entirely (treats them as unknown). codex flagged this around `cmd/hoot/serve_bridge.go:301-323`. browser attach may currently be receiving live bytes only and no snapshot. since the new design relies even more heavily on the snapshot frames (cinema gone), this is worth at least a paragraph in your spec — call out as in-scope or explicitly out-of-scope, your call. if in scope, the bridge needs to forward both snapshot frames as ordered binary, plus the new "rendered tail prefix" if that lands as a separate frame.
  - adjacent finding: `hoot run --attach` flag defaults must mirror the new attach defaults so spawn-then-immediate-attach doesn't surprise. mention in spec; the implementation pass will need to update `cmd/hoot/run.go` alongside `cmd/hoot/attach.go`.
  - naming: rather than `--ascii-cinema-playback` (legacy) think about whether the future `hoot replay` verb obviates needing any flag here at all. simpler to remove the flag entirely than agonize over naming.
  - codex didn't propose alt-screen-specific handling at all. your `primaryTerm` / `ActiveScreen()` thinking goes deeper — keep that nuance in the spec where it still applies.
  
  # task
  
  re-read the two prior specs above. revise your spec.md to:
  - recommend excising cinema playback from attach (cite prior-work reasoning).
  - promote rendered-tail-via-throwaway-terminal to the primary recommendation, with implementation sketch.
  - add a section on the `serve_bridge.go` browser-attach implication — in or out of scope.
  - add a brief "future work" note about a `hoot replay` verb that consumes `pty.cast`.
  - update the implementation surface accordingly.
  
  then re-park at the rfc gate. the human will tick once they've read the revised spec.
- 2026-05-06T10:05Z ticked: rfc

## Evidence

### Commits on branch `hoot-attach-replay-shows-tui-animation-artifacts`

```
195b203 docs/cross-compile: drop --no-ascii-cinema-playback from smoke-test snippet
ddbb350 README: drop ascii-cinema-playback flags, explain snapshot-only attach
bc6e9f9 Forward snapshot frames through the WS bridge
018bee0 Excise asciicast cinema playback from attach
```

Net diff (excluding generated/lock files): 9 source files modified, 1 new test, ~330 lines deleted, ~120 inserted.

### Full test suite (uncached)

```
$ go test -count=1 ./...
ok  	github.com/hayeah/hootty	0.374s
ok  	github.com/hayeah/hootty/cmd/hoot	2.314s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.408s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.584s
```

Saved at `tmp/test-output.txt`.

### Targeted: new bridge-forwarding test

```
$ go test -count=1 -run TestPumpUpstreamToWS -v ./cmd/hoot
=== RUN   TestPumpUpstreamToWSForwardsSnapshotFrames
--- PASS: TestPumpUpstreamToWSForwardsSnapshotFrames (0.00s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	0.179s
```

The test stands up a real httptest WebSocket, runs `pumpUpstreamToWS` against a fake upstream byte stream containing one of each VT-bearing frame type (`MsgSnapshotScrollback`, `MsgSnapshotScreen`, `MsgOutput`), and asserts each arrives at the browser as a binary WS payload with byte-identical content. Before the fix, the function's default arm errored on the snapshot frames as "unknown frame type" — this test would have failed.

### Sanity: cinema-playback symbols are gone from the source

```
$ grep -rin "ascii.cinema\|asciicinema\|asciiCinema\|sendAsciiCinemaPlayback\|streamAsciiCinemaPlayback" --include="*.go" --include="*.md"
# (only matches under docs/tasks/<historical>/, which are immutable
#  worklog/spec records of past tasks — preserving them as historical
#  context is correct.)
```

Live source, README, and `docs/cross-compile.md` no longer reference the flags.

### Behavioral expectation (manual verification by human)

- Attach to a `hoot run` session driving Claude Code (or any TUI). Detach via `<prefix>.`. Reattach. Expected: jump-cut to the current frame; no visible re-typing of past prompts, no `/usage` panel opening + closing, no spinner re-animation. Final state matches what the session was rendering at the moment of attach.
- Browser attach (`hoot serve` + `/attach` web UI) now repaints the snapshot on attach instead of starting blank and only showing live bytes after the first new keystroke. (Independent bug surfaced during this work.)

I have not driven these manually inside this worktree; they're for the human to confirm against a real Claude Code session before final lgtm. The unit tests cover the wire-level behavior (snapshot frames flow through both the CLI and the bridge with no cinema-playback path); the user-visible animation regression cannot be unit-tested without a TUI fixture and is the kind of thing the human eyeballs.

## Trouble report

- Phase 1 / spec rounds:
  - Section had an `rfc: review spec.md` gate; the spec went through three revisions before tick: v1 (default-flip + alt-screen guardrail) → v2 (excise + rendered-tail prefix) → v3 (excise only, no rendered-tail — the live emulator IS the parsed grid, so the throwaway-terminal mechanic was redundant). The human's question "do we need to read pty.cast events? can we read from the terminal directly?" was the load-bearing simplification — saved ~250 LOC of speculative code.
  - Mid-revision, also discussed making `pty.cast` an efficient binary format with a JSONL converter; the human moved that out of scope for this section ("future work").
- Phase 2:
  - Found and fixed an independently-broken bug: `cmd/hoot/serve_bridge.go::pumpUpstreamToWS` rejected `MsgSnapshotScrollback`/`MsgSnapshotScreen` as unknown frames, so browser attach has been receiving live bytes only — never the initial snapshot. With cinema gone, snapshot frames are the only initial repaint, so this *had* to be fixed in the same section. Codex's parallel run flagged it; the boss surfaced it in the rfc nudge.
  - The `ReadAsciicastOutputEvents` function was originally on the deletion list in phase-2 todos; kept it instead — it's still useful for the future `hoot replay` verb / recorder-format work and is covered by `recorder_test.go`. Cheaper to leave than to delete-and-re-add.
  - gopls workspace warnings appeared throughout the session (worktree path is outside the gopls workspace). Real `go build` and `go test` are clean; the warnings are tooling-only noise. #friction (boss-level: editing a worktree without a `go.work` entry produces stale gopls diagnostics that look alarming but aren't real).
  - One open spec question (Q2: xtermjs compat with the snapshot's kitty-keyboard bytes) resolved at implementation time without action — xtermjs ignores unknown CSI sequences, so leaving `ExtraKittyKeyboard` in the formatter's snapshot is fine for both the CLI and the bridge. Documented in the bridge-fix commit.

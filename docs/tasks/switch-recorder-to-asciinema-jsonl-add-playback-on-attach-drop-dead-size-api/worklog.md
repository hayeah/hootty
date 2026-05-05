---
status: done
section: Switch recorder to asciinema JSONL; add playback on attach; drop dead size API
slug: switch-recorder-to-asciinema-jsonl-add-playback-on-attach-drop-dead-size-api
mode: worktree
spec: spec.md
created: 2026-05-05T04:36:06Z
---

> ## Switch recorder to asciinema JSONL; add playback on attach; drop dead size API
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Two changes, one section:
>
> ### Part 1 — drop the dead `Recorder.Size()` / offset path
>
> The recorder's offset return is vestigial. `attach_handler.go:181` already does `liveCh, _, cancelSub := h.pty.SubscribeAtRecord()` — the offset is discarded. Replay switched to libghostty snapshot, which doesn't need the offset. Net:
>
> - `Recorder.Size()` is called inside `SubscribeAtRecord` (cheap but pointless), exposed on the public API, and referenced by one test that also discards it via `_`.
> - Drop the `int64` from `SubscribeAtRecord`'s return.
> - Drop `Recorder.Size()` from the public surface.
> - Update the one test (`pty_libghostty_strip_test.go:38`) and any other callers.
> - Snapshot-based attach keeps working unchanged.
>
> This is cleanup; do it before Part 2 to keep diffs clean.
>
> ### Part 2 — record as asciinema (asciicast v2) and add playback on attach
>
> Replace the raw `pty.log` recorder with an asciinema asciicast v2 writer:
>
> - Header line: JSON object with `version: 2`, `width`, `height`, `timestamp` (unix seconds), maybe `command`, `env`, `title` if cheap.
> - Subsequent lines: `[seconds_since_start, "o", chunk_string]` JSONL events. `"o"` = output, `"i"` = input (we may or may not record input — call this out in the spec.md).
> - Resize events: `[t, "r", "COLSxROWS"]` when SIGWINCH lands on the master.
> - Filename can stay `pty.log` or change to `pty.cast` — your call. Keep it under the existing state-dir.
> - The file must be valid input for `asciinema play`/`agg`/`asciinema-player`, so a passing `asciinema play <state-dir>/<key>/pty.cast` is part of the evidence.
>
> **Playback feature on attach** (default ON): after sending the libghostty snapshot, the server *also* streams the asciicast back to the client at original timing (or at a sped-up rate — pick a reasonable default and justify in spec.md), so the user can watch recent history play out before live takes over. The libghostty snapshot is the "where we are now" frame; the asciinema playback gives the "how we got here" replay.
>
> - `--no-ascii-cinema-playback` flag on `hoot attach` disables the playback (snapshot only, then live).
> - Server side: a new attachwire frame, or extend `Hello` with a `playback_mode` field — pick what's minimal.
> - Decide: is playback bounded (e.g. last N minutes of history) or full session? Long-lived sessions could have huge `pty.cast` files; replaying a 12-hour session at original speed is unusable. Spec your default and the override.
> - Ensure the live cutover is still byte-clean — playback ends, live begins, no missed bytes (same atomicity property as the original full-replay design).
>
> ### Open design questions for the spec.md
>
> - Bounded playback default (last 5 min? last N events? on-demand only?). Justify.
> - Playback speed default (1x? 2x? as-fast-as-possible burst?). Justify.
> - Wire shape: new frame type vs extending `Hello` with playback metadata.
> - Does playback survive reconnect? Probably no replay on reconnect (too noisy).
> - Interaction with the recently-shipped query-stripper: playback bytes go through the same fanout filter so the client doesn't get probe replies.
>
> The agent should write `spec.md` with concrete decisions, then implement and verify end-to-end. Solve it; we'll review the landed package together.
>
> - [ ] investigate, write spec.md, then implement and verify end-to-end
>   - evidence: `asciinema play` works against a real session's `pty.cast`
>   - evidence: attach with playback shows recent history then transitions cleanly to live
>   - evidence: `--no-ascii-cinema-playback` skips the playback
>   - evidence: reattach mid-tmux session still works (query stripper + playback both involved)

## Todos

- [x] Remove `Recorder.Size` and the `SubscribeAtRecord` offset return
- [x] Implement asciicast v2 recorder output and resize events
- [x] Switch hootty sessions from `pty.log` to `pty.cast`
- [x] Add cast playback reader and attach-handler playback streaming
- [x] Add attach CLI flags and `Hello` playback fields
- [x] Update README and focused tests
- [x] Run full automated tests
- [x] Produce real-session `asciinema play` evidence
- [x] Verify attach playback and no-playback attach end to end
- [x] Verify tmux reattach with query stripping and playback
- [x] Rebase onto master phased snapshot protocol and resolve conflict preserving master features
- [x] Add/extend golden attach harness coverage for asciicast playback
- [x] Re-run tests and refresh evidence after rebase

## Agent log
- 2026-05-05T04:42:13Z — Checked out `github.com/hayeah/hootty` into worktree slot `000`, inspected recorder/attach/CLI surfaces, and wrote `spec.md` with concrete playback defaults and wire-shape decisions.
- 2026-05-05T04:43Z Dropped recorder offset API in hootty commit 45a70a2; go test ./... passes.
- 2026-05-05T04:46Z Recorded PTY output as asciicast in hootty commit 40c3f1d; go test ./... passes.
- 2026-05-05T04:49Z Added attach asciicast playback and flags in hootty commit e31fe34; go test ./... passes.
- 2026-05-05T04:50Z Updated README and CLI docs for pty.cast/playback in hootty commit 47ea618; go test ./... passes.
- 2026-05-05T04:50Z Full automated verification passed: go test ./...
- 2026-05-05T04:54Z E2E tmux playback exposed CSI ? 996 n; fixed stripper in hootty commit 7656d34 and recorded the design note.
- 2026-05-05T04:56Z Done: implementation committed through 7656d34; worklog evidence covers go test, asciinema play, playback/no-playback attach, and tmux query-strip playback.
- 2026-05-05T05:12Z Rebased on master phased snapshot protocol preserving master features. Playback now runs after `MsgSnapshotScreen` and before live `MsgOutput`; added raw attach golden coverage in `c7db7d9`; refreshed tests and e2e evidence.
- 2026-05-05T05:15Z Done after rebase: preserved master phased snapshot/golden harness, added playback raw golden c7db7d9, refreshed tests/e2e, and set status back to done.

## Boss log
- 2026-05-05T05:05Z rebase conflict on master during lgtm. aborted — worktree is clean.
  
  textual conflict in: pty_libghostty_test.go (UU)
  auto-merged (no conflict): cmd/hoot/main.go, cmd/hoot/hoot.go, pty_libghostty.go, recorder.go, recorder_test.go (A)
  
  commits that landed on master since you branched:
  
  - 338c132 "Split attach snapshots into paint phases" — attach_handler.go, cmd/hoot/attach.go, internal/attachwire/wire.go, pty_libghostty.go, pty_libghostty_test.go
  - 32c1daf "Add attach golden snapshot harness" — README.md, cmd/hoot/attach.go, cmd/hoot/attach_golden_test.go (NEW), internal/attachetest/harness.go (NEW), internal/attachetest/testdata/*.golden (NEW)
  
  what landed structurally (this is the context you need most — your design now needs to fit on top of it):
  
  - `Snapshot()` was split into `SnapshotParts() (scrollback, screen []byte, err error)`. The wire protocol got two new frame types `MsgSnapshotScrollback` + `MsgSnapshotScreen` to replace the old single snapshot frame.
  - The client paint is now PHASED: connect banner → scrollback bytes → ESC[H ESC[2J (clear viewport) → visible-screen bytes + cursor restore → live frames. Detach is the inverse.
  - A reusable golden-snapshot harness lives in `internal/attachetest/`. Tests in `cmd/hoot/attach_golden_test.go` drive remote+local libghostty terminals through the attach pipeline and compare against checked-in goldens. Six scenarios covered.
  
  your asciinema playback feature needs to slot INTO this phased paint, not replace it. the natural insertion point is between the visible-screen phase and the live cutover: scrollback → clear → visible-screen + cursor restore → **asciicast playback** → live frames. that way the user sees "where we are now" first, then "how we got here" replays before live takes over. you may want to reconsider whether playback should run before or after visible-screen given the new phasing — flag it in the agent log if you change the design.
  
  resolution requirements (paste verbatim):
  
  > Resolve by PRESERVING features from master, not by taking your side blindly. For each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. If master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. Re-run tests after resolution.
  
  specifically for pty_libghostty_test.go: master added test coverage for the new SnapshotParts/phased paint; your branch added asciicast recorder tests. Both should coexist — adopt master's version and re-add your test cases.
  
  ALSO: please add a golden test (or extend an existing one) under `internal/attachetest/`-style scenarios that covers the playback path — proves your feature integrates with the new harness rather than running in a parallel test world.
  
  when done: update your commit(s) on the branch, append a note to ## Agent log, flip status back to done. i'll re-run boss lgtm.

## Evidence

Commits in `repos/github.com/hayeah/hootty`:

- `0bba2bd` Drop recorder offset API
- `b279e12` Record PTY output as asciicast
- `16b7c2d` Replay asciicast history on attach
- `80732cc` Document asciicast attach playback
- `fd0a398` Strip tmux private DSR queries
- `c7db7d9` Cover asciicast playback in attach goldens

Automated tests:

```sh
$ go test ./...
ok  	github.com/hayeah/hootty	(cached)
ok  	github.com/hayeah/hootty/cmd/hoot	0.466s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
```

Golden attach playback harness:

```text
internal/attachetest/testdata/attach-asciicast-playback-raw.golden:
[connected. playback @ local]
<ESC>[H<ESC>[2Jscreen-now<ESC>[0m<ESC>[1;11Hplayback-a
playback-b
<ESC>[H<ESC>[2Jscreen-now
```

Real asciicast file:

```sh
$ sed -n '1,4p' tmp/e2e-state-rebased/castdemo/pty.cast
{"version":2,"width":80,"height":24,"timestamp":1777957964,"command":"bash -lc printf \"HIST_A\\n\"; sleep 0.1; printf \"HIST_B\\n\"; exec bash --noprofile --norc -i","title":"castdemo"}
[0.011250125,"o","HIST_A\r\n"]
[0.120370833,"o","HIST_B\r\n"]
[0.12324875,"o","\r\nThe default interactive shell is now zsh.\r\nTo update your account to use zsh, please run `chsh -s /bin/zsh`.\r\nFor more details, please visit https://support.apple.com/kb/HT208050.\r\n"]
```

`asciinema play` against that real cast:

```sh
$ script -q tmp/e2e-output-rebased/asciinema-play.txt uvx asciinema play tmp/e2e-state-rebased/castdemo/pty.cast
HIST_A
HIST_B
...
bash-3.2$ exit=0
```

Attach playback vs no playback:

```text
tmp/e2e-output-rebased/attach-playback.out:     [connected.]=1 HIST_A=2 HIST_B=2 LIVE_REBASED=2
tmp/e2e-output-rebased/attach-no-playback.out:  [connected.]=1 HIST_A=1 HIST_B=1 LIVE_REBASED=2 NO_PLAYBACK_REBASED=2
```

Interpretation: playback attach shows the phased connect/snapshot output plus asciicast history replay (history appears twice), then a live command marker. `--no-ascii-cinema-playback` skips the cast replay (history appears once from the snapshot) and still reaches live.

Tmux reattach with playback and query stripping:

```text
tmp/e2e-state-rebased/tmuxdemo/pty.cast contains raw CSI ? 996 n: 1
tmp/e2e-output-rebased/attach-tmux-playback.out: [connected.]=1 TMUX_HISTORY=2 TMUX_LIVE_REBASED=2
query counts in attach output:
  ESC[c      0
  ESC[>c     0
  ESC[>q     0
  OSC 10 ?   0
  OSC 11 ?   0
  ESC[18t    0
  ESC[14t    0
  ESC[?996n  0
```

Interpretation: tmux startup produced query bytes in the recording, playback was involved (`TMUX_HISTORY=2`), the live marker arrived, and the attached terminal did not receive the known query probes.

## Trouble report

- The tmux e2e run exposed a query shape outside the previous stripper set: `CSI ? 996 n`. Fixed in rebased commit `fd0a398` by treating private numeric `CSI ? Pn n` as DSR-style terminal queries; see `spec.md` design note `2026-05-05T04:54Z`.
- Rebase required adapting playback to master’s phased snapshot protocol. Preserved `MsgSnapshotScrollback`/`MsgSnapshotScreen` and moved playback after the screen phase. See `spec.md` design note `2026-05-05T05:11Z`.

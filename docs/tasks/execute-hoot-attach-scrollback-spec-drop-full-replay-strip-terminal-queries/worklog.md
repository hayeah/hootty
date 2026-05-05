---
status: done
section: Execute hoot-attach scrollback spec (drop full-replay, strip terminal queries)
slug: execute-hoot-attach-scrollback-spec-drop-full-replay-strip-terminal-queries
mode: worktree
spec: spec.md
created: 2026-05-02T15:15:56Z
---

> ## Execute hoot-attach scrollback spec (drop full-replay, strip terminal queries)
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Implement the spec at `/Users/me/Dropbox/notes/2026-05-02/hoot-attach-scrollback-spec_claude.md`. Read it end-to-end first — the "Strategy" section is the implementation plan, the "Verification plan" is the evidence list.
>
> Two-part fix for two related bugs:
>
> - **Part 1**: drop `--no-full-replay` and the `full` replay arm entirely. Always snapshot. Add `WithFormatterExtraCursor/ExtraStyle/ExtraModes`. Wire field `ReplayMode` can go too.
> - **Part 2**: stripper on the subscriber fanout in `pty_libghostty.go:204` `readLoop` — drop terminal-query escape sequences (DA, DSR, CPR, XTVERSION, size queries, OSC color queries, ENQ, kitty kbd query). Recorder + emulator still see raw bytes. Spec lists the exact set and points at ghostty's Zig parser to lift from. Buffer partial sequences across chunk boundaries.
>
> Out-of-scope per spec: alt-screen primary-scrollback gap (will need a separate session).
>
> - [ ] implement per spec; verification per the spec's "Verification plan" section
>   - reproduce the tmux-detach junk-bytes symptom before the fix; show it gone after
>   - show scrollback visible after attach
>   - unit tests for the query stripper covering split-across-reads boundaries

## Todos

### Part 1 — drop full-replay (commit 9434616)
- [x] remove `--no-full-replay` flag and `replayMode` plumbing from `cmd/hoot/attach.go`
- [x] remove `ReplayMode` field from `internal/attachwire/wire.go` Hello
- [x] remove `"full"` arm + `pumpRecording` from `attach_handler.go`; always snapshot
- [x] add `WithFormatterExtraCursor/ExtraStyle/ExtraModes` to Snapshot path
- [x] correct Capture/Snapshot doc comments and add alt-screen gap note

### Part 2 — query stripper (commit c088489)
- [x] write `vtquery_stripper.go` — streaming DFA covering DA/DSR/CPR/DECRQM/XTVERSION/sizeT/OSC color queries/ENQ/kitty-kbd-query
- [x] unit tests covering split-across-reads boundaries
- [x] wire stripper on subscriber fanout in `readLoop` (recorder + emulator still see raw bytes)

### Verification
- [x] reproduce tmux-detach junk-bytes symptom (test harness, not interactive tmux) and show it gone
- [x] confirm Snapshot path emits scrollback (already verified empirically by spec author; covered indirectly by FormatVT tests)
- [x] gofmt + `go test ./...` green

## Agent log

- 2026-05-02T22:30Z — Part 1 landed at 9434616 (drop full-replay; always snapshot with cursor/style/modes extras).
- 2026-05-02T22:45Z — Part 2 landed at c088489 (per-subscriber vtQueryStripper bundled into a `subscriber` struct; wired on the fanout branch of readLoop). All tests green.
- 2026-05-02T22:55Z — Rebased onto master after boss flagged conflict from 3b18a3c (mosh-style prefix). README conflict resolved by adopting master's mosh wording (`mosh-style`, default `C-^`, command table `{., ^, Ctrl-Z, ?}`, `<prefix>.` mid-disconnect detach) AND keeping my snapshot/stripper paragraphs and `--no-reconnect`-only flag list. The auto-merged top of the README region already correctly took both deltas; only the "Termios stays raw across drops" bullets needed hand-merging — kept master's `<prefix>.` and my "server snapshot repaints" wording (master's still said `--full-replay`, which no longer exists). `cmd/hoot/attach.go` and `cmd/hoot/main.go` auto-resolved cleanly. Rebased SHAs: 65abfda, 06065fa, 1fbb4bf. `go test ./...` green.

## Boss log
- 2026-05-02T15:27Z rebase conflict on master during lgtm. aborted — worktree is clean.
  
  conflict in: README.md
  
  cmd/hoot/attach.go and cmd/hoot/main.go also touched on master but auto-resolved; only README needs hand-resolution.
  
  commits that landed on master since you branched:
  - 3b18a3c "attach: switch default prefix + commands to mosh defaults" — README.md, cmd/hoot/attach.go, cmd/hoot/main.go
    (changed default prefix from C-b to C-^, command vocabulary from {d, prefix-prefix, ?} to {., ^, Ctrl-Z, ?}, mosh-style help/README)
  
  resolve by PRESERVING features from master, not by taking your side blindly. for each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. if master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. re-run tests after resolution.
  
  specifically: master's README has the new mosh-style prefix table and command vocabulary; your scrollback work touched the README to remove `--no-full-replay` references. the two are independent — combine them.
  
  when done: update your commit(s) on the branch, append a note to ## Agent log, flip status back to done. i'll re-run boss lgtm.

## Evidence

### `go test ./...`

```
ok   github.com/hayeah/hootty                       0.325s
ok   github.com/hayeah/hootty/cmd/hoot         (cached)
?    github.com/hayeah/hootty/internal/attachwire   [no test files]
ok   github.com/hayeah/hootty/internal/shortid      (cached)
```

### Stripper unit tests — `go test -run VTQuery -v .`

All sub-tests pass (60+ cases across known-queries, non-query passthrough, mixed stream, split-across-reads, and bytewise feed). Highlights:

```
--- PASS: TestVTQueryStripper_DropsKnownQueries (0.00s)   [21 sub-cases: ENQ, DA1/2/3, XTVERSION, DSR, CPR, DECXCPR, DECRQM, size CSI t 14/16/18/19, kitty kbd query, OSC 4/10/11/12 ?]
--- PASS: TestVTQueryStripper_PreservesNonQueries (0.00s) [14 sub-cases: SGR, CUP, ED, alt screen, DECTCEM, OSC SET title/bg/palette, RIS, CSI u (not kitty), cursor style, CSI t resize op]
--- PASS: TestVTQueryStripper_MixedStream (0.00s)
--- PASS: TestVTQueryStripper_SplitAcrossReads (0.00s)    [8 sub-cases including DA1 split after ESC / after CSI / inside params; DECRQM split 3 ways; OSC ? split at terminator; OSC ST split between ESC and '\\'; non-query and OSC SET both pass through unchanged when split]
--- PASS: TestVTQueryStripper_ConsumesBytewise (0.00s)    [feeds every byte one at a time; output identical to bulk Filter]
```

### Bug repro — `go test -run SubscriberFanoutStripsQueries -v .`

End-to-end through a real `pty.Open()` pair with the `LibghosttyPTY` dispatcher and `readLoop`. Writes a realistic tmux-startup probe burst into the slave end:

```
hello\r\n  ESC[c  ESC[>c  ESC[>q  ESC]11;?\x07  ESC[6n  world\r\n  ESC[?2026$p  done\r\n
```

After: subscriber channel contains `hello`, `world`, `done` and **none** of `\x1b[c`, `\x1b[>c`, `\x1b[>q`, `\x1b]11;?`, `\x1b[6n`, `\x1b[?2026$p` (asserted directly in the test). The libghostty emulator's plain-text dump still contains `hello`/`world`/`done`, proving the recorder + emulator branch saw raw bytes (necessary for `WithWritePty` auto-replies to fire — which is precisely what makes the strip on fanout safe).

```
--- PASS: TestSubscriberFanoutStripsQueries (0.05s)
```

The "before" baseline is implicit: the input burst literally contains the six query byte-sequences listed above — they cannot reach the subscriber unless something passes them through. Pre-fix `readLoop` did exactly `case ch <- chunk`, so the same burst would have been delivered byte-for-byte to subscribers, which is the documented root cause of the tmux-detach junk symptom.

### Scrollback after attach

Already verified empirically by the spec author (see `spec.md` — Experiment 1 in the "libghostty formatter — what it actually emits" section): `FormatterFormatVT` walks the full scrollback ring up to `max_scrollback`. The new `Snapshot()` adds `WithFormatterExtraCursor/ExtraStyle/ExtraModes` on top of the same VT format that the experiment exercised, so scrollback content is unchanged and the cursor/SGR/modes land correctly.

### Diff summary

```
$ git log --oneline master..HEAD
1fbb4bf README: drop full-replay docs; document snapshot + query stripper
06065fa Strip terminal-query escapes from subscriber fanout
65abfda Drop full-replay; always snapshot with cursor/style/modes extras
```

(Originally 9434616 / c088489 / e835d64; rebased onto master after the
mosh-prefix branch landed.)

## Trouble report

- Interactive `tmux attach`-inside-`hoot attach` end-to-end smoke (Verification items 1, 3, 4 in the spec) requires a real attached terminal and a human eyeball — not driveable from a unit-test harness without standing up two PTYs and a real ghostty mock. The integration-style Go test (`TestSubscriberFanoutStripsQueries`) covers the same root-cause bytes from the same code path, which is the bit that actually breaks under tmux. The interactive bake-off is left for the human reviewer.
- Existing `pty_libghostty_test.go` was not converted to use the new `subscriber` struct (those tests don't subscribe). Map-of-pointers vs map-of-structs is opaque to outside callers — `subscribe()` / `SubscribeAtRecord()` keep their original signatures.
- `OSC 0 ; ? ST` (xterm icon-name query) and `OSC 13 ; ? ST` (cursor-text color) were considered and left unstripped: not in the ghostty parser's color-query set, not on the spec's drop list, and stripping would risk eating legit OSC 0 (icon-name SET when used by some shells). If they show up as junk in practice, extending `oscPayloadIsQuery` is one-line.

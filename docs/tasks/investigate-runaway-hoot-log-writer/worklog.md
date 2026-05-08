---
status: done
section: Investigate runaway hoot log writer
slug: investigate-runaway-hoot-log-writer
mode: worktree
spec: spec.md
created: 2026-05-08T06:11:10Z
---

> ## Investigate runaway hoot log writer
>
> ---
> status:
>   type: open
> ---
>
> A `hoot run` against a runaway process produced a ~1GB pty log whose contents look like the same scrollback repeated over and over — strong smell of a recorder-feedback loop (attach output fed back into the recorder?).
>
> Context: the binary log format landed in `46e3fb2` (`implement-hoot-binary-pty-log-format`) — recorder, frame layout, and `hoot log` viewer all live there. The status-line-into-scrollback work in `bd98b27` is also new; either could plausibly be involved.
>
> Investigate: reproduce the unbounded growth deterministically, trace the actual write path, confirm or refute the recursive-feedback hypothesis, propose a fix shape. Don't implement the fix — that's a follow-up section once the cause is nailed down.
>
> - [ ] rfc: review findings

## Todos
- [x] Check workspace state, read boss loop, and attach the `hootty` repo worktree.
- [x] Locate the recorder write path and related attach/status-line code.
- [x] Review `46e3fb2` and `bd98b27` diffs for plausible feedback paths.
- [x] Build and run a deterministic repro that bounds runtime while demonstrating log growth or absence of growth.
- [x] Decode/inspect generated logs to classify repeated frames and byte provenance.
- [x] Trace runtime writes enough to identify the responsible path.
- [x] Write findings, hypothesis verdict, and proposed fix shape into evidence.
- [x] Implement `cmdLog` self-session guard and `--output FILE` safe override.
- [x] Add regression tests for self-log refusal, `--output`, and logging another session from inside a session.
- [x] Update README and `hoot log` usage text.
- [x] Run focused and full tests, then update evidence and status.

## Agent log
- 2026-05-08T06:18Z started investigation; read boss loop, checked workspace, found no existing spec, checked out `github.com/hayeah/hootty` via `boss checkout`.
- 2026-05-08T06:36Z blocked for planned RFC review: findings and fix shape are in Evidence; no product fix implemented.
- 2026-05-08T10:08Z boss ticked RFC gate; investigation section complete, no product fix implemented per scope.
- 2026-05-08T10:11Z re-read boss note: new implementation box added; switching back to working for cmdLog guard + --output + tests/docs.
- 2026-05-08T10:23Z implemented cmdLog self-session guard + --output, tests, and docs in hootty commit aa45c9d; focused tests and go test ./... pass.
- 2026-05-08T10:18Z boss reported rebase conflicts on master; resolving by reading master's conflicted files first, then rebasing and preserving both master alive-only picker features and this branch's self-log guard.
- 2026-05-08T10:31Z resolved rebase conflicts by combining master `hoot log --all` / `AliveOnly: !all` picker behavior with this branch's `--output` + self-log guard; rebased commit is b2a5004 and go test ./... passes.

## Boss log
- 2026-05-08T10:08Z ticked: rfc
- 2026-05-08T10:08Z rfc green-lit by the human. new top-level box added: "implement per findings".
  
  go ahead and implement the fix you proposed in spec.md:
  - guard cmdLog after target resolution: if $HOOT_SESSION == resolved-key, refuse with a tmux-shaped error message. reuse errIfNestedHootSession (or its building blocks) from cmd/hoot already on master (8c658cc) for consistency with attach/shell/run --attach.
  - expose a safe override path. you proposed `--output FILE` for explicit redirection — implement that and document it as the recommended way to dump a session's log from inside that session.
  - add the e2e regression: spawn a session, run `hoot log $HOOT_SESSION` inside it, assert no self-append. include a sibling test that `hoot log <other-session>` still works inside a session.
  - update README + cmdLog usage text.
  
  flip status to working when you start, done when ready for review.
- 2026-05-08T10:18Z rebase conflict on master during boss lgtm. aborted — worktree is clean.
  
  likely conflict areas (all in your branch's diff and master's diff):
  - cmd/hoot/log.go
  - cmd/hoot/log_test.go
  - README.md
  
  commits that landed on master since you branched (8c658cc):
  - b855361 "hoot: filter the fzf picker to alive sessions by default" — cmd/hoot/{attach,clone,detach,kill,log,picker}.go. adds pickerOptions.AliveOnly. log gets a new `--all` flag that opts back in to dead sessions in the picker. attach/detach/kill/clone are always alive-only.
  - aacffd3 "hoot: tests for alive-only picker filter + log --all" — cmd/hoot/log_test.go, cmd/hoot/picker_resolve_test.go. 5 picker_resolve tests + a log_test that exercises `cmdLog --all` against a dead-only state-dir.
  - 5f6873a "README: document alive-only picker default + hoot log --all" — README.md.
  
  resolve by PRESERVING features from master, not by taking your side blindly. for each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. if master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. re-run tests after resolution.
  
  specifically for cmd/hoot/log.go: master added a `--all` flag and AliveOnly wiring through pickerOptions. your branch added `--output FILE` and the $HOOT_SESSION self-log guard. both flag declarations need to coexist; the picker resolution and the self-session guard need to compose cleanly. neither should drop the other.
  
  specifically for cmd/hoot/log_test.go: master added TestCmdLog_AllFlagFlipsAliveOnly and helpers (e.g. markAlive). your branch added TestCmdLogRefuses... + TestCmdLogOutput... + the e2e tests. all of them need to coexist; share helpers where they overlap.
  
  when done: rebase onto master, update your commit(s), append a note to ## Agent log, flip status back to done. i'll re-run boss lgtm.

## Evidence

### Commands

Built a workspace binary and ran the full suite:

```sh
go test ./...
go build -o /Users/me/Dropbox/boss/tasks/investigate-runaway-hoot-log-writer/tmp/hoot-investigate ./cmd/hoot
bash tmp/repro.sh > tmp/repro_transcript.txt 2>&1
```

`go test ./...` passed:

```text
ok  	github.com/hayeah/hootty	7.308s
ok  	github.com/hayeah/hootty/cmd/hoot	7.528s
ok  	github.com/hayeah/hootty/internal/sessionpick	(cached)
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
ok  	github.com/hayeah/hootty/internal/sshtransport	(cached)
```

Artifacts:

- `tmp/repro.sh` - bounded repro script.
- `tmp/decode_hoot_log.py` - binary log frame summarizer.
- `tmp/repro_transcript.txt` - full transcript.
- `tmp/repro-state/` - generated session state/logs.

### Write Path

The recorder is created at `cmd/hoot/session.go:80` and attached to `LibghosttyPTY` at `cmd/hoot/session.go:85`. Production recorder writes are only:

- `pty_libghostty.go:252` read loop: `master.Read` child output, then `p.rec.Write(chunk)` at `pty_libghostty.go:260`.
- `pty_libghostty.go:407` resize path: `p.rec.RecordResize(cols, rows)` at `pty_libghostty.go:430`.

Attach output does not write to the recorder. The server sends snapshot/live frames in `attach_handler.go:222-243`; only client `MsgInput` writes to the PTY at `attach_handler.go:273-274`. The attach client writes received snapshot/status/live bytes to local stdout at `cmd/hoot/attach.go:760-785`; that stdout is outside the session unless the CLI itself is running as a child inside another hoot session.

### Repro Findings

Attach is not the direct self-recorder loop:

```text
idle log bytes before_attach=26 after_attach_and_exit=69 delta=43
file_bytes=69 frames=4 counts={2: 2, 1: 2}
first_output_samples:
  idle-start
  idle-end
idle attach stdout sample:
  \e[>u\e[24;1H...\e[24S\e[Hidle-start...idle-end...
```

The attach stdout contains HUD/snapshot/cleanup bytes, but the recorder contains only child output plus the resize frame. That refutes ordinary attach replay being fed back into the same recorder.

The deterministic feedback loop is `hoot log` of the current session from inside that session:

```text
## self-log loop inside its own session
file_bytes=979 frames=5 counts={2: 1, 1: 4}
seed-line: count=21
--- iter 1 ---: count=14
--- iter 2 ---: count=4
--- iter 3 ---: count=4
--- iter 4 ---: count=4
--- iter 8 ---: count=1
```

Command shape from `tmp/repro.sh`: session `selflog` prints `seed-line`, then runs `hoot log --state-dir "$STATE" --format plain --strict selflog` eight times. Because that command's stdout is the session PTY, the recorder appends each rendered view of the current log back into `selflog/pty.hootty.log`. A runaway process doing this loop grows without bound and the log contents look like repeated scrollback.

Nested attach is a related but currently guarded case:

```text
## nested run --attach with current guard
payload=hoot run: sessions should be nested with care, unset $HOOT_SESSION to force (already attached to "outer-refuse")
guard prevented inner session log creation
```

Forcing nested attach with `unset HOOT_SESSION` records the inner attach UI into the outer session log, but it was finite in this repro:

```text
outer-forced file_bytes=905 output_payload_bytes=869
inner-forced file_bytes=136 output_payload_bytes=96
```

### Verdict

Confirmed feedback, but not the hypothesized ordinary attach-output-to-same-recorder loop. The actual deterministic self-feedback path is:

`current session child -> hoot log <current-session> -> stdout on current PTY -> LibghosttyPTY.readLoop -> Recorder.Write -> same pty.hootty.log`.

The binary log format (`46e3fb2`) made `hoot log` available for this path. The status-line-into-scrollback change (`bd98b27`) is not the root cause, though nested attach/status bytes can still pollute an outer session when the nested guard is intentionally bypassed.

### Proposed Fix Shape

Follow-up fix should guard `cmdLog` after target resolution:

- Resolve the target key as today.
- Read `$HOOT_SESSION`.
- If `$HOOT_SESSION == key`, refuse by default with an error like: `hoot log: refusing to write this session's log to its own PTY; redirect to a file from outside the session or unset $HOOT_SESSION to force`.
- Prefer a safer explicit path over a broad force flag if product needs it, e.g. `hoot log --output FILE selflog`, because stdout pipelines can still end in the same PTY.
- Add an e2e regression test that spawns a session, runs `hoot log $HOOT_SESSION` inside it, and asserts no self-append occurs by default. Add a separate test that `hoot log other-session` still works inside a session.

### Implementation Evidence

Implemented in `github.com/hayeah/hootty` commit `b2a5004` (`Guard hoot log against self-recording`), rebased onto master `7f2d44d`:

- `cmd/hoot/log.go`: added `--output FILE`, routes all log formats through the selected writer, refuses `$HOOT_SESSION == resolved-key` when output is stdout, and rejects `--output` pointing at the source `pty.hootty.log`.
- `cmd/hoot/nesting.go`: added the self-log guard helper using the same `$HOOT_SESSION` building block as nested attach refusal.
- `cmd/hoot/log_test.go`: added unit coverage for self-log refusal, `--output`, source overwrite rejection, plus e2e tests that spawn a real session and run `hoot log $HOOT_SESSION` / `hoot log other-session` inside it.
- `README.md` and `cmd/hoot/main.go`: documented `--output` and the current-session stdout refusal.
- Rebase resolution preserved master's `hoot log --all` flag and `pickerOptions{AliveOnly: !all}` wiring while keeping the `--output` flag and self-log guard after key resolution.

Focused test command:

```sh
go test ./cmd/hoot -run 'TestCmdLog|TestErrIfNested|TestSanitizeChildEnv|TestCmdAttachRefusesWhenNested|TestCmdShellRefusesWhenNested|TestCmdRunAttachRefusesWhenNested|TestCmdRunWithoutAttachAllowedWhenNested|TestCmdListAllowedWhenNested' -v
```

Relevant passing output:

```text
=== RUN   TestCmdLogRefusesCurrentSessionToStdout
--- PASS: TestCmdLogRefusesCurrentSessionToStdout (0.00s)
=== RUN   TestCmdLogOutputAllowsCurrentSession
--- PASS: TestCmdLogOutputAllowsCurrentSession (0.00s)
=== RUN   TestCmdLogOutputRejectsSourceRecording
--- PASS: TestCmdLogOutputRejectsSourceRecording (0.00s)
=== RUN   TestCmdLogInsideSessionRefusesSelfAppendE2E
--- PASS: TestCmdLogInsideSessionRefusesSelfAppendE2E (1.05s)
=== RUN   TestCmdLogInsideSessionAllowsOtherSessionE2E
--- PASS: TestCmdLogInsideSessionAllowsOtherSessionE2E (1.07s)
=== RUN   TestCmdLog_AllFlagFlipsAliveOnly
--- PASS: TestCmdLog_AllFlagFlipsAliveOnly (0.32s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	3.088s
```

Full suite:

```sh
go test ./...
```

Passing output:

```text
ok  	github.com/hayeah/hootty	(cached)
ok  	github.com/hayeah/hootty/cmd/hoot	9.896s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	(cached)
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
ok  	github.com/hayeah/hootty/internal/sshtransport	(cached)
```

## Trouble report
- Initial repo path guess `~/github.com/hayeah/hoot` did not exist; actual repo appears to be `~/github.com/hayeah/hootty`.
- `go build -o ../../../../tmp/hoot-investigate` from the physical worktree wrote under `/Users/me/github.com/hayeah/hootty/tmp` due the resolved worktree path. Rebuilt with an absolute workspace path to keep artifacts under this task's `tmp/`.
- Very short `hoot run --attach` commands can exit before the parent observes `rpc.sock`, producing `session did not start: timeout waiting for .../rpc.sock` even though the session log exists. The repro script keeps short commands alive with `sleep 1` to avoid that known timing issue.
- The first e2e test helper used a shell script wrapper for the test binary. In the `hoot __session` path, that wrapper could claim the PTY as controlling terminal before the child process started, causing `Setctty` to fail and `rpc.sock` never to appear. Replaced it with a `TestMain` helper mode in the test binary itself.

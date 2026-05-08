# Investigate runaway hoot log writer

## Goal

Determine why a `hoot run` can produce an unbounded `pty.hootty.log` that appears to repeat the same scrollback. The goal is to reproduce the growth deterministically, trace every path that can write to the binary pty log, confirm or refute the suspected recorder feedback loop, and propose a concrete fix shape. Implementing the fix is out of scope for this section.

## Architecture

Repo under investigation: `repos/github.com/hayeah/hootty`.

Relevant areas:

- `recorder.go`: binary log writer, frame batching, decoder helpers.
- `pty_libghostty.go`: PTY read loop, emulator writes, subscriptions, recorder hook.
- `attach_handler.go` and `internal/attachwire`: attach input/output path and replay/snapshot behavior.
- `cmd/hoot/run.go`, `cmd/hoot/attach*.go`, `cmd/hoot/log.go`: CLI paths that create sessions, attach, and render logs.
- Recent commits to compare: `46e3fb2` binary log format and `bd98b27` status-line-into-scrollback work.

## Steps

- Confirm the checkout and locate the exact recorder write path.
- Review `46e3fb2` and `bd98b27` diffs for behavior that could feed rendered output back into the recorded PTY stream.
- Build a deterministic repro harness that starts a session, triggers the suspected path, and measures `pty.hootty.log` growth without needing a 1GB run.
- Decode/inspect the generated binary log to identify whether repeated bytes are true PTY output, attach replay bytes, status-line bytes, or viewer-rendered bytes.
- Trace runtime writes with targeted instrumentation or existing tests to identify the writer responsible for repeated frames.
- Confirm or refute the recursive-feedback hypothesis.
- Propose a fix shape with the smallest responsible ownership boundary and list tests that should accompany the follow-up fix.

## Verification

- Save repro transcripts and any decoded log summaries under `tmp/`.
- Include exact commands used to reproduce and inspect the log.
- Evidence must show:
  - whether log size grows after the child process is idle or exited,
  - which code path writes repeated bytes,
  - whether attach output is being re-entered as recorder input,
  - a concrete follow-up fix outline.

## Open questions

None at the start. If the suspected loop depends on local terminal behavior that cannot be reproduced headlessly, the fallback is to create the smallest synthetic test that exercises the same library-level path.

## Design notes

- 2026-05-08T06:18Z - This section is investigation-only, so the primary deliverables are `spec.md`, `worklog.md`, and `tmp/` artifacts. I will avoid code changes unless a tiny diagnostic test or scratch command is needed to prove the write path; any product fix stays for the follow-up section.
- 2026-05-08T06:36Z - The deterministic feedback path is `hoot log` of the current session from inside that same session, not ordinary attach replay.
  - Recorder writes are narrow:
    - `cmd/hoot/session.go` creates `<state-dir>/<key>/pty.hootty.log`.
    - `pty_libghostty.go` records output only from `readLoop` after `master.Read`, plus resize frames from `Resize`.
    - `attach_handler.go` sends snapshot/live frames to the attach client; it does not write those bytes back to the recorder. Only client `MsgInput` reaches `pty.Write`.
  - Bounded repro:
    - Running `hoot attach` against an idle session wrote a large HUD/snapshot/cleanup stream to attach stdout, but the session log only gained `idle-end` plus the expected resize frame; no snapshot/status bytes appeared in the recorder.
    - Running `hoot log --format plain --strict selflog` eight times from inside session `selflog` appended the rendered current recording back into `selflog/pty.hootty.log`; `seed-line` appeared 21 times and `--- iter 1 ---` 14 times. That matches the repeated-scrollback smell.
  - Fix shape for the follow-up:
    - After `cmdLog` resolves the target key, compare it to `$HOOT_SESSION`.
    - If they match, refuse by default with a message that explains this would append the session log to itself.
    - Consider an explicit escape hatch only if it is safe by construction, e.g. `--output <file>` or `--force` with very clear wording. A bare stdout pipeline is not necessarily safe because downstream programs can still write to the same PTY.
  - Nested attach remains a related but separate hazard. Current master's `$HOOT_SESSION` guard prevents default nested attach paths; forcing it with `unset HOOT_SESSION` records the inner attach UI into the outer session, but the repro showed one finite copy, not an intrinsic recorder loop.
- 2026-05-08T10:23Z - Implemented the follow-up fix after the RFC gate was ticked.
  - Picked guard point: after `cmdLog` resolves the session key and before opening output. That keeps fuzzy/id-prefix behavior unchanged and compares `$HOOT_SESSION` against the canonical key.
  - Picked override: `--output FILE`, not a broad `--force`. The failure mode is specifically stdout being the same PTY that is being recorded, so writing to an explicit regular file is the safer product shape. `unset HOOT_SESSION` still bypasses the guard for debugging, but docs point users to `--output`.
  - Added a source-protection check so `--output` cannot overwrite the same `pty.hootty.log` being read.
  - Test harness note: a shell-script helper executable claimed the PTY in the session-start path and made the child `Setctty` fail. The e2e tests now use the test binary itself in a `TestMain` helper mode, which matches production's direct-binary `hoot __session` path.

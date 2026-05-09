# Reproduce hoot log GB accumulation (claude)

## Goal

Identify the post-`d708de0` cause of GB-scale `pty.hootty.log` accumulation
and document a deterministic repro, the traced write path, the root-cause
verdict, and a fix-shape sketch. Research-only — no fix landed.

## Verdict

**Root cause is an unsigned-integer underflow in `recorder.go`'s
`bridgeIdleLocked` spacer-emit loop, not the previously-fixed self-feedback
path.** When `bridgeIdleLocked` is called with a target ms (`nowMs`) that
is *less than* `r.lastEmittedMs`, the expression `nowMs - r.lastEmittedMs`
wraps around in `uint64` to ~2^64, the loop's `> 65535` condition stays
permanently true, and the recorder writes 9-byte spacer frames in a tight
loop — limited only by disk throughput. At ~2.5 MB/s of pure spacer
frames, an unattended session fills the disk in a few hours.

That ordering — `pendingBucketMs < lastEmittedMs` — is reachable through
the public `Recorder` API any time a `RecordResize` lands at a
non-bucket-aligned millisecond R and the next `Write` arrives within ~23 ms
(the same 33 ms bucket window). bucket = `(now/33)*33` rounds *down* to
just below R, while `lastEmittedMs` was just bumped to R by the resize.
A subsequent `Write` into a different bucket then triggers
`flushPendingLocked → bridgeIdleLocked(bucket < lastEmittedMs)` → the
runaway loop.

This bug pre-dates `d708de0` (it landed with the binary-format rewrite in
`dcfb7e4`, 2026-05-08), but was masked by the more obvious self-recording
feedback loop the previous section fixed. With that path closed, the
underflow is the surviving GB-scale offender.

## Repro recipe (deterministic, package-level)

`repos/github.com/hayeah/hootty/recorder_underflow_test.go` (added by this
investigation). Two tests:

- `TestRecorderUnderflowAfterResize` — synthesizes the post-resize state
  (`lastEmittedMs=1000, pendingBucketMs=990, pendingBuf="oops"`) and calls
  `flushPendingLocked` under a 2 s timeout.
- `TestRecorderResizeWriteFlushRunaway` — exercises the bug end-to-end via
  the **public** API only: `NewRecorder` → rewind `started` so `nowMs() ≈
  1000` (non-aligned) → `RecordResize` → `Write` (lands at bucket 990) →
  rewind `started` again so a second `Write` triggers a flush.

Run from the worktree:

```sh
cd repos/github.com/hayeah/hootty
go test -run "TestRecorderResizeWriteFlushRunaway|TestRecorderUnderflowAfterResize" -v .
```

Observed (transcript saved at `tmp/164114_3N-recorder-runaway.log`):

- `TestRecorderUnderflowAfterResize`: `flushPendingLocked` did not return in
  2 s; log grew to **8,746,370 bytes**.
- `TestRecorderResizeWriteFlushRunaway`: same bug via the public API; log
  grew to **5,037,331 bytes** in the same 2 s budget.

Extrapolating: ~2.5–4.3 MB/s of pure spacer frames per stuck recorder.
Hours of background time → GB-scale `pty.hootty.log`.

## Traced write path (where the bytes come from)

The recorder fanout is **unchanged** from the previous investigation —
production writes only touch the recorder via:

- `LibghosttyPTY.readLoop` → `do(...)` → `r.rec.Write(chunk)` from
  `pty_libghostty.go:260`. Source: bytes the child wrote on the slave, read
  from the master.
- `LibghosttyPTY.Resize` → `do(...)` → `r.rec.RecordResize(cols, rows)` from
  `pty_libghostty.go:430`. Triggered by initial sizing and by every
  attach-client `MsgSize` (which is sent on connect AND on each `SIGWINCH`).

Both go through the dispatcher goroutine, so the recorder sees a totally
serialised stream. No new path feeds the master after `eb51a17` —
`hoot install-remote`, `hoot @host` auto-bootstrap, and `attach_restorer`
do not write to any session's PTY master. Nested attach with `unset
HOOT_SESSION` is still a self-feedback hazard but the boss-doc guard
flagged it as a *candidate* — the deterministic GB driver in current master
is the recorder's own spacer loop.

The runaway bytes are not "PTY content at all" — they are spacer frames
the recorder synthesises *itself* and writes to disk with no caller in the
loop. `Recorder.flushPendingLocked` holds `r.mu` for the duration, so the
dispatcher (and therefore subscribers + recorder fanout from `readLoop`)
**also blocks** while disk fills. The on-disk file is the only thing
growing; the apparent "logged scrollback repeats" the previous task
described would not be present in this variant.

### Why the bug is reachable from the natural flow

Setup (numbers picked for illustration; `R` is "any non-bucket-aligned
ms"):

1. `NewRecorder` — preamble + prelude resize at `delta=0`. `lastEmittedMs
   = 0`, `pendingHasBucket = false`.
2. Child output starts; `readLoop` calls `Write`. `pendingBucketMs` gets
   set to `(t/33)*33`.
3. Attach client connects, sends initial `MsgSize` (or sends one after a
   `SIGWINCH`). Server calls `LibghosttyPTY.Resize` →
   `Recorder.RecordResize`:
   - `flushPendingLocked` flushes buffered output, sets `lastEmittedMs =
     oldPendingBucket`.
   - `bridgeIdleLocked(now)` runs (now ≥ oldPendingBucket — fine).
   - `writeResizeFrameLocked(uint16(now - lastEmittedMs), ...)`. Note the
     `uint16` truncation if the gap exceeds 65535 ms — separately
     suspicious, but not the GB driver.
   - **`r.lastEmittedMs = now`** (call this R; `R % 33 != 0` is the
     overwhelmingly common case — real time is essentially never bucket-
     aligned).
4. Next `readLoop` chunk, t > R but in the same 33 ms bucket:
   - `bucket = (t/33)*33`. Because R is not bucket-aligned and t is in the
     same 33 ms window, `bucket = floor(R/33)*33 < R`.
   - `pendingHasBucket = false` after the resize, so the Write installs
     `pendingBucketMs = bucket < lastEmittedMs`.
5. Any later Write into a different bucket → `flushPendingLocked` →
   `bridgeIdleLocked(bucket)`:

```go
for nowMs - r.lastEmittedMs > 65535 {           // bucket - R underflows
    advance := uint32(^uint32(0))               // 4_294_967_295
    // write 9 bytes (spacer header + payload)  // ← infinite loop
    r.lastEmittedMs += uint64(advance)          // grows further past nowMs
}
```

Once `lastEmittedMs > nowMs`, every subsequent iteration's subtraction
wraps again. The loop has no exit; only the OS error path (disk full,
write returning error) will eventually break it.

### Window for the bug to fire

For first-Write-after-Resize at time t with R = `lastEmittedMs` after
resize:

- bucket-of-t < R iff `(t/33)*33 < R` iff `floor(t/33) < ceil(R/33)`,
  which for non-aligned R simplifies to `t < ((floor(R/33)+1) * 33)`.
- Window length: `33 - (R % 33)` ms — between 1 ms and 32 ms.

Interactive sessions emit child-output chunks far more often than every
33 ms (cursor blink, prompt rerender, status-line, just `ls` output),
so the first Write after a resize lands inside the window with very
high probability. SIGWINCH-driven resizes during normal terminal use
will trigger this routinely.

## Fix-shape sketch

Three independent guard locations, ordered by minimal-blast-radius:

1. **Sanitize `bridgeIdleLocked`'s entry condition.** Change:

   ```go
   for nowMs - r.lastEmittedMs > 65535 { ... }
   ```

   to a signed/explicit-comparison form that treats `nowMs <= lastEmittedMs`
   as "no spacer needed":

   ```go
   for nowMs > r.lastEmittedMs && nowMs - r.lastEmittedMs > 65535 { ... }
   ```

   This is the smallest fix and stops the runaway. Doesn't address the
   logical question of what `delta` should be when the next frame's
   timestamp is "before" the last one.

2. **Floor the next bucket to `lastEmittedMs` in `Write`.** When computing
   `bucket = (now/33)*33`, also raise it: `if bucket < r.lastEmittedMs:
   bucket = r.lastEmittedMs`. This keeps the recorder's monotonic-time
   invariant from being violated in the first place. Cleaner: callers
   never end up with a "backwards" pending bucket.

3. **Reset `lastEmittedMs` to bucket-floor inside `RecordResize`.** Instead
   of setting `r.lastEmittedMs = now`, set it to `(now/33)*33`. The next
   Write's bucket then equals or exceeds it. Smallest semantic surface but
   loses fidelity on the resize → next-write delta.

I'd pick (1)+(2) together: (1) is a defence-in-depth one-liner against any
future caller pushing `nowMs < lastEmittedMs`; (2) restores the invariant
the rest of the code already assumes.

A focused regression test: keep
`TestRecorderResizeWriteFlushRunaway` and assert the flush returns within
50 ms with a bounded log size (e.g. < 1 KB).

## Out of scope

- The previously-flagged `unset HOOT_SESSION; hoot attach $HOOT_SESSION`
  self-feedback loop is real but distinct, and not what's pushing GB onto
  disk in current master. The runaway-recorder underflow fires from any
  ordinary `hoot run --attach` once a SIGWINCH lands.
- The `uint16` truncation in `RecordResize`'s `delta := now -
  r.lastEmittedMs` (line 167–168) is a separate latent bug for sessions
  that idle longer than 65 s before their first resize; it does not cause
  GB growth on its own but should be fixed alongside.
- `hoot install-remote` / `hoot @host` auto-bootstrap from `eb51a17` —
  the new code never writes to any session's PTY master. Auto-bootstrap
  fires only when the local CLI is itself constructing a tunnel; the
  in-session guards in `cmdShell`, `cmdAttach`, `cmdRun` already refuse
  the wrap. Cleared.

## Open questions

None. The bug is reproducible from the public Recorder API alone; the
fix shape is straightforward.

## Design notes

- 2026-05-09T16:42Z — Verdict landed on the `bridgeIdleLocked` underflow
  rather than any of the listed candidate hot zones. Trace went:
  - Started by auditing every PTY-master writer per the boss's framing
    (`p.master.Write` from libghostty query auto-replies, `p.Write` from
    attach/clients, `p.rec.Write` from `readLoop`, `p.rec.RecordResize`
    from `Resize`). All bounded, all old code.
  - `hoot install-remote` and `hoot @host` auto-bootstrap (eb51a17) write
    to `os.Stdout/Stderr`, not to any recorded master. The
    `cmdShell`/`cmdAttach`/`cmdRun` `errIfNestedHootSession` guard already
    refuses these from inside a session.
  - `attach_restorer` writes to the local terminal, never back to the
    server's master.
  - That left the boss's "spacer/auto-split branches in the binary
    recorder" suggestion. Read recorder.go closely; spotted the unsigned
    subtraction in `bridgeIdleLocked` is unguarded.
  - Confirmed via two tests in `recorder_underflow_test.go`. Both fail by
    timeout in 2 s; the on-disk file balloons to multi-MB. Public-API path
    is reproducible, so this is not a synthetic-only state.
  - The bug is older than `d708de0` — landed with `dcfb7e4` (binary
    rewrite). It was previously masked by the louder self-feedback loop;
    fixing that loop (`d708de0`) didn't introduce a new bug, it just
    revealed an existing one.

- 2026-05-09T16:55Z — Considered three fix shapes; documented above. My
  preference is the one-liner guard in `bridgeIdleLocked` plus the
  bucket-floor in `Write`, but the human/codex agent should compare
  before picking. The third option (resize-time bucket floor) is the
  smallest patch but loses delta fidelity.

- 2026-05-09T17:14Z — RFC ticked. Codex's parallel investigation hit the
  same root cause (their repro: `unset HOOT_SESSION; hoot attach $HOOT_SESSION`
  → 7.08M spacer frames each `advance=4_294_967_295`). Boss greenlit the fix.
  - Picked options 1 + 2 from the spec body, dropped option 3.
    - **Option 1 (`bridgeIdleLocked` early-return)**: defense in depth.
      Bails when `nowMs <= lastEmittedMs` — costs zero spacer frames if a
      future caller violates the invariant. Cheap.
    - **Option 2 (`Write` floors `bucket` to `lastEmittedMs`)**: enforces
      the invariant *at the only place it can be violated through the
      public API*. The post-resize sub-bucket case becomes a `delta=0`
      frame at `lastEmittedMs` rather than a corrupt earlier-time entry.
      Preserves frame-time monotonicity in the on-disk format.
    - **Option 3 (`RecordResize` floors `lastEmittedMs` to bucket)**:
      *rejected*. Would set lastEmittedMs to `(now/33)*33` instead of
      `now`, losing up to 32 ms of fidelity on the resize event timestamp
      for no additional safety once option 2 is in place.
  - Footgun audit of `recorder.go` for similar unsigned subtractions:
    `flushPendingLocked`'s `delta := uint16(bucket - r.lastEmittedMs)` and
    `RecordResize`'s `delta := now - r.lastEmittedMs` both *could* underflow
    if their inputs regressed. Both become safe by construction once the
    Write invariant holds (bucket ≥ lastEmittedMs always; now from
    nowMsLocked is monotonic-clock-backed and only ever increases). No
    extra guards needed; documented the invariant on the field comments
    so the next reader doesn't need to re-derive it.
  - Regression test budgets: 200 ms flush time and 4 KiB log size. The
    public-API repro pre-fix burned 5+ MB in 2 s, so 4 KiB is comfortably
    out of any plausible runaway band. Tightening to 50 ms / 1 KiB is
    possible but adds CI flake risk for no better signal.
  - Fix landed in commit `df60c1a` on branch
    `reproduce-hoot-log-gb-accumulation-claude`. `go test ./...` clean.

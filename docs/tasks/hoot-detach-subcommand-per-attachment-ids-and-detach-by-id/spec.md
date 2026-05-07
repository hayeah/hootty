# spec — `hoot detach`, attachment IDs, and richer `hoot list`

## Goal

Today there is no way to remotely kick a single attachment off a `hoot` session. `hoot kill` (recently landed — see `cmd/hoot/kill.go` and `docs/tasks/hoot-kill-subcommand-signal-the-monitored-process/spec.md`) signals the supervised child process, not the attach plumbing — wrong tool for this job. We want to terminate one viewer of a session without disturbing the workload or the other viewers. `hoot detach` is the symmetric counterpart and reuses the same CLI / mux pattern as `hoot kill`.

This change introduces a per-attachment short ID, persists attachments in `state.json`, and adds a `hoot detach <id>` subcommand. `<id>` may be either a session id (detach all attachments on that session) or an attachment id (detach exactly one). The same change extends the `Hello` frame to carry origin metadata (host, terminal, declared cols×rows) so `hoot list` can show *who* is attached, *from where*, and *what window size each side wants*. Out of scope: any change to the supervised process lifecycle, asciicast playback, or the wire protocol's framing layer.

## Architecture

### Process model — what an attachment is today

An attachment is a single hijacked HTTP/1.1 connection serviced by `attachHandler.serveOne` (`attach_handler.go:116-272`). One inbound TCP/unix connection becomes:

- The `serveOne` goroutine itself, which does the Hello handshake, registers in `AttachSet`, kicks off live forwarding, and runs `clientReadLoop` to forward `Input` / `Size` / `Ping` from the client.
- A writer goroutine draining `outCh` and writing framed bytes to the conn (`attach_handler.go:142-151`).
- A live-forwarder goroutine that drains `liveCh` from the dispatcher into `outCh`, with snapshot/playback ordering (`attach_handler.go:215-244`).
- A subscription on the libghostty dispatcher (`pty_libghostty.go:611-650`, `SubscribeWithSnapshotParts`) — registered by sending a `subscribeReq` onto the dispatcher's command channel; the cancel func sends an `unsubscribeReq` and closes `liveCh`.
- A membership in `AttachSet` (`attach.go:26-47`) which holds the declared cols×rows used by min-wins resize.

So "an attachment" = (subscriber slot in dispatcher) + (`AttachSet` member) + (writer goroutine) + (read goroutine) + (the TCP/unix conn). All five are joined at the hip by `serveOne`'s `defer`s; once `serveOne` returns, everything is reaped.

**What "detach" means**: close the underlying conn. That fails the next `attachwire.ReadFrame` in `clientReadLoop`, which returns; `serveOne` then calls `cancelSub()`, drains `liveDone`, closes `outCh`, waits on `writerDone`, and unwinds the deferred `h.set.Remove(ac)`. Closing the conn is sufficient — no separate "deregister" is needed. The asymmetric race (write goroutine still mid-frame when we close the read side) is fine because we already tear down through `closeOut`/`writerDone`.

### ID model — session-scoped attachment IDs, `<session>/<attachment>` syntax

Session keys stay exactly as they are today: `shortid.Generate` against the existing set of session keys (`run.go:155-165`, the `generateKey` helper). 3–8 chars from `shortid.IDAlphabet`. No on-disk migration, no change to `hoot run` output, no change to existing scripts.

Attachment IDs are **scoped to their parent session**: a fresh attachment mints its ID via `shortid.Generate` against *only that session's* current `attachments[*].id` set. This is a much smaller candidate pool, never grows beyond the small handful of attachments a session ever has, so collision retries are essentially free. There is no cross-session uniqueness requirement on attachment IDs — `(sessionA, "abc")` and `(sessionB, "abc")` are perfectly fine because the addressing form always carries the session.

The CLI uses `/` as the session/attachment separator:

- `hoot detach <session-prefix>` — detach **all** attachments on the resolved session.
- `hoot detach <session-prefix>/<attachment-prefix>` — detach exactly that one attachment.

Resolution happens in two stages, mirroring the path structure:

1. Split the arg on the first `/`. Left half is the session prefix, right half (if present) is the attachment prefix.
2. Resolve session prefix via the existing `Store.Resolve` (`store.go:79-96`).
3. If an attachment prefix was given, load that session's `state.json` and call `shortid.Resolve(attachmentPrefix, sessionState.Attachments[*].ID)`.

Both halves use the existing `shortid.Resolve`. Two short prefixes joined by `/` is what the user types.

**Why this shape rather than a flat namespace?**

- Session-scoped attachment IDs mean each session mints against its own tiny set: `Generate` is fast, IDs stay short (3 chars almost always), and there's zero cross-session coordination.
- The `/` separator makes the addressing scheme self-describing in CLI output and in any future REST or programmatic surface (`/sessions/foo/attachments/bar` falls out naturally — see "Detach mechanics" below).
- The mental model is the same as filesystem paths: a session is a directory, an attachment is a file inside it. `hoot detach foo` is "detach this directory's contents"; `hoot detach foo/bar` is "detach this specific file."
- Whatever ambiguity exists with the prefix-resolve ("is `abc` a session or an attachment?") is gone by construction: `abc` is *always* a session prefix, `abc/x` is *always* a session/attachment pair.

**Per-session registry**: the supervisor (`__session`) still owns its own attachments. We add an `attachRegistry` (or fold into `AttachSet`) holding, per-id, the `attachConn`, `closeConn func()`, and origin metadata. The map key is the attachment short ID. The registry serializes minting through its mutex, so concurrent attaches into the same session never collide.

### state.json schema

Extend `SessionState` (`state.go:24-30`) with a new slice:

```go
// PTYSize is a {cols, rows} pair. Shared between the session's
// effective size and each attachment's client-declared size so the
// wire and read-side code can use one type.
type PTYSize struct {
    Cols uint16 `json:"cols"`
    Rows uint16 `json:"rows"`
}

type SessionState struct {
    Key         string             `json:"key"`                   // shortid.Generate, unique across sessions
    PID         int                `json:"pid,omitempty"`
    CreatedAt   time.Time          `json:"created_at"`
    Argv        []string           `json:"argv,omitempty"`
    CWD         string             `json:"cwd,omitempty"`
    Size        PTYSize            `json:"size"`                  // effective (min-wins) PTY size
    Attachments []AttachmentRecord `json:"attachments,omitempty"`
}

type AttachmentRecord struct {
    ID        string    `json:"id"`         // shortid.Generate, unique within this session's attachments[]
    StartedAt time.Time `json:"started_at"`
    Size      PTYSize   `json:"size"`       // client-declared
    Origin    *Origin   `json:"origin,omitempty"`
}

type Origin struct {
    Host               string `json:"host,omitempty"`
    Term               string `json:"term,omitempty"`             // $TERM
    TermProgram        string `json:"term_program,omitempty"`     // $TERM_PROGRAM
    TermProgramVersion string `json:"term_program_version,omitempty"`
    User               string `json:"user,omitempty"`             // $USER (cheap, skip if absent)
}
```

Note: `SessionState.Size` is *not* tagged `omitempty` — Go's `encoding/json` doesn't honor `omitempty` for non-pointer struct values, so the tag would be a lie. A zero `Size` renders as `"size":{"cols":0,"rows":0}`; that's fine for a session that briefly hasn't sized yet, and post-Hello it's never zero. (If we ever want it omitted we'd switch to `*PTYSize` — punted, not worth a pointer indirection for the uniform case.) `AttachmentRecord.Size` is always populated post-Hello, so the same applies.

Skip TZ for now — terminals don't carry it natively and adding `time.Local` adds noise. Keep `Origin` a pointer so the JSON is omitted entirely for old clients that didn't send any origin fields.

**Cols/Rows on `SessionState`**: we surface the *currently effective* size (what the supervised PTY is actually sized to right now — what `AttachSet.Effective()` returns). When the set goes empty we keep the last value (matches existing `AttachSet` semantics — see `attach.go:128`). This gives `hoot list` the "session size" datum the boss asked for.

**Concurrency**: the `Writer` already serializes updates through `Update(fn)` (`store.go:130`). The new attachment events (Add/Remove) call `Update` with a small mutator that appends or removes by ID. Updates run on the goroutine that finishes the Hello handshake (Add) and on the goroutine running `serveOne`'s deferred teardown (Remove). The `AttachSet`'s own mutex is not the right lock for state.json — we make a tiny `attachRegistry` that holds:

```go
type attachRegistry struct {
    mu      sync.Mutex
    writer  *session.Writer            // injected so the registry can persist
    members map[string]*registeredAttach
}

type registeredAttach struct {
    ac        *attachConn
    record    AttachmentRecord
    closeConn func()                   // close the underlying conn
}
```

**Effective-size churn**: every `AttachSet.Add/Update/Remove` may change `Effective()`. We hook a `sizeApplier` that, in addition to calling `pty.Resize`, calls `writer.Update` to refresh `SessionState.Cols/Rows`. This is one extra fsync per resize — fine; resize already does `TIOCSWINSZ` syscalls.

**Lifecycle on `__session` exit**: when the supervisor returns from `Run`, the `Writer` is closed and (in current behavior) `state.json` stays on disk until the next session reuses the key or the directory is GC'd by the consumer. So *yes*, stale `attachments` would persist across crashes if `__session` died ungracefully. Two mitigations:

- On clean shutdown, the deferred `h.set.Remove(ac)` chain runs for each in-flight attach as the supervisor exits, so attachments leave state cleanly. We additionally clear `attachments` to `nil` in a final `Update` from `Runner.Run` after `Service.Run` returns (or as a `defer` in `__session`'s entry).
- On crash, the dir flock is released by the kernel. The CLI can detect "this state has stale attachments" by combining `Store.IsAlive(key) == false` with `len(attachments) > 0`. `hoot list` renders these as `(stale)` next to the session row. We do *not* try to rewrite state.json from a non-supervisor process — that would race the flock contract. Detach against a non-alive session is rejected at the CLI layer with a friendly error.

### Hello

Reshape `attachwire.Hello` to mirror `AttachmentRecord` — same `PTYSize`, same `Origin` sub-struct — so the supervisor lifts them straight into the registry record without field-by-field shuffling. Drop the three `AsciiCinemaPlayback*` fields; that feature is being nuked in parallel.

```go
type Hello struct {
    Size   PTYSize `json:"size"`
    Origin Origin  `json:"origin"`
}
```

Where `PTYSize` and `Origin` are the *same* types declared on the read-side state schema (`session.PTYSize`, `session.Origin`); `internal/attachwire` imports them rather than redeclaring. This keeps wire ↔ state.json parity by construction.

`Origin` is sent inline (no pointer) — every field is `omitempty`, so an old/light client that sends `{"size":{...},"origin":{}}` (or even an empty/missing `origin` object) decodes to a zero `Origin`, which renders as no `origin` block in the persisted `AttachmentRecord` (because `AttachmentRecord.Origin` *is* `*Origin` — the supervisor stores `nil` if the unmarshalled `Origin` has all-empty fields).

CLI fills `Origin` from `os.Hostname()`, `$TERM`, `$TERM_PROGRAM`, `$TERM_PROGRAM_VERSION`, `$USER`. SSH-tunnelled remote attaches see the *local* host/term (which is exactly what we want — that's where the user is sitting).

This is a wire-protocol break for clients still sending `cols`/`rows` at the top level. We accept it: the attach protocol is internal to a single `hoot` binary version, the upgrade banner is still `hoot-attach/1` (no semver in the protocol today), and the asciicinema-field removal is a break of equal magnitude already happening in this section. Mismatched-version clients will fail Hello decoding cleanly — the server returns an error frame and closes, the client sees a protocol-error and exits non-zero.

### Detach mechanics

Per session, the registry holds a `closeConn` per attachment. The supervisor's `mux` gets a new route:

- `DELETE /attachments/{id}` → looks up the registered attach, calls `closeConn()`, returns 204. 404 if no such id. The closeConn closes the underlying hijacked `net.Conn`, which trips `clientReadLoop`'s next `ReadFrame` with an error; `serveOne` unwinds, the deferred `h.set.Remove(ac)` removes from AttachSet, and the registry's `Remove(id)` fires from the same defer chain (rewriting state.json). Detach is therefore "best-effort fast" — we don't wait for the unwind to finish before returning 204; the boss/CLI cares that the close *was issued*, not that all goroutines have exited.

- `DELETE /attachments` (no id) → close *all* attachments on this session. Use case: `hoot detach <session-id>` resolves to a session, not an attachment, and the CLI calls this no-id form. Returns 204 with body `{"closed": <n>}`.

The conn close emits a final framed message before close: a `MsgOutput` carrying `"\r\n\x1b[33m[detached by hoot detach]\x1b[0m\r\n"` so the user on the detached terminal sees a reason rather than a silent EOF. We piggyback on the existing best-effort error frame in `attachHandler.ServeHTTP:96-101` by threading a "reason" through.

**Local CLI path**, mirroring `killLocal` (`cmd/hoot/kill.go:87-110`):

1. Split the positional arg on first `/` → `(sessionPrefix, attachmentPrefix)` (attachmentPrefix may be empty).
2. Resolve `sessionPrefix` via `Store.Resolve` (existing).
3. If `attachmentPrefix` is non-empty, load that session's `state.json` and `shortid.Resolve(attachmentPrefix, sessionState.Attachments[*].ID)`.
4. Dial `<state-dir>/<sessionKey>/rpc.sock` with `dialUnixSock`.
5. Issue `DELETE http://unix/attachments` (no id) or `http://unix/attachments/<id>` (one id), map response status to the exit-code table.

**Remote CLI path**, mirroring `killRemote` (`cmd/hoot/kill.go:112-125`):

- `DELETE /sessions/{key}/attachments` → 204 (close all)
- `DELETE /sessions/{key}/attachments/{id}` → 204 (close one)

`serve.go` proxies these to the upstream rpc.sock (same `DialContext` pattern `handleEvents` uses at `serve_bridge.go:36-42`).

For the remote case the CLI cannot walk a local state-dir to resolve attachment ids, so when the user passes `<sess>/<att>` against `--remote`, the CLI first fetches `GET /sessions/{sessionPrefix}` (which already returns the full state including `attachments[]`), resolves the attachment prefix locally, then issues the DELETE.

### `hoot list` rendering

Today `hoot list` emits one JSON line per session — a serialized `StateFile`. The schema bump above is automatically reflected: the new `size` and `attachments[]` fields appear in the existing JSON output, no flag needed and no code change beyond the struct definitions. Effective session size shows up under `session.size`; per-attachment sizes and origins show up under `session.attachments[*]`.

That's the whole list change for this section. Pretty-printed table output is a separate, additive feature; punted out of scope. Backwards compat is automatic — a state.json without `attachments` decodes to an empty slice, which marshals back as `omitempty`-omitted, so old consumers see a JSON shape unchanged from before.

### CLI shape

```
hoot detach [--remote <url>] [--state-dir <d>] <session-prefix>[/<attachment-prefix>]
```

Behavior — mirroring `hoot kill`'s CLI shape (`cmd/hoot/kill.go`):

- Without `/`: resolve `<session-prefix>` against session keys; close every attachment on that session.
- With `/`: resolve `<session-prefix>` first, then resolve `<attachment-prefix>` against that session's `attachments[*].id`; close only that one attachment.
- Exit codes (matching `hoot kill` for a consistent CLI surface):
  - `0` — success (any matched ID closed; `detach <session>` with 0 attachments is also success).
  - `1` — I/O / network error (rpc.sock not reachable, unexpected 5xx).
  - `2` — usage error (bad flag, no positional, prefix too short, malformed `<sess>/<att>`).
  - `3` — no such session/attachment, or ambiguous prefix (`shortid.AmbiguousIDError` matches listed in the message — separately for the session and attachment halves).
  - `5` — session resolved but no longer alive (stale state.json); body explains.
- Stderr on success: `hoot: detached <n> attachment(s) from session "<session-key>"` for the session form, or `hoot: detached attachment "<sess>/<att>"` for the single form.

The detached client sees a yellow "[detached by hoot detach]" line then a clean EOF. Exit code on the client side is the existing graceful-detach 0 — nothing to change in `cmdAttach`.

### Auth/safety

The local rpc.sock is permission-gated by directory perms (state-dir is mode 0755 owned by the user; the socket inherits). Same surface as `hoot attach` already — anyone who can dial /attach can dial /attachments. Remote `hoot serve` is unauthenticated by design today; gating /attachments tighter than /attach would be inconsistent and pointless. **Confirmed: no new auth surface; existing controls suffice.**

## Steps

1. Schema: add `PTYSize`/`Attachments`/`AttachmentRecord`/`Origin` to `state.go`. Plumb effective-size persistence into `AttachSet`'s sizeApplier hook in `attach_handler.go`.
2. `attachwire.Hello`: reshape to `{Size PTYSize, Origin Origin}`, dropping the three `AsciiCinemaPlayback*` fields and their handler-side plumbing (`attach_handler.go:274-340`, `asciiCinemaPlaybackConfigFromHello`, `streamAsciiCinemaPlayback`, related tests). Import `PTYSize`/`Origin` from the session package so wire and state.json share types. Populate `Origin` in `cmdAttach` from `os.Hostname()` + `$TERM` / `$TERM_PROGRAM` / `$TERM_PROGRAM_VERSION` / `$USER`.
3. `attachRegistry` in the session library: minting via `shortid.Generate` against the session's own `attachments[*].id` set, Add/Remove, persistence via `Writer.Update`. Wire into `serveOne` after Hello validates.
4. New supervisor mux routes: `DELETE /attachments`, `DELETE /attachments/{id}`. Stash per-attach `closeConn` in the registry; close the conn on DELETE.
5. Final-frame "detached by hoot detach" injected on supervisor-initiated close.
6. `hoot serve` proxy routes: `DELETE /sessions/{key}/attachments[/{id}]`. Mirror `handleAttach`'s pattern of dialing rpc.sock.
7. `cmd/hoot/detach.go` — local + remote paths, ID resolution against the state-dir union.
8. `cmd/hoot/list.go` — no code change required; the schema bump flows through the existing JSON-encoder loop. Sanity-check via the smoke transcript.
9. Tests: registry add/remove + state.json roundtrip; DELETE /attachments local; ID resolution union. CLI smoke for `hoot run; hoot detach`.
10. README + `usage()` in `cmd/hoot/main.go`: add `hoot detach` line.

## Verification

- `go test ./...` passes.
- New unit tests:
  - `TestAttachRegistry_AddRemove_StateJSON`: starts a session library `Runner` with a stub Service, opens an attach via the in-process attach harness (`internal/attachetest`), checks state.json picks up the attachment record (id, cols/rows, origin), then closes the conn and checks the record is removed.
  - `TestDetachByAttachmentID`: opens two attaches, calls `DELETE /attachments/{id}` for one, asserts only that one's conn observes EOF and state.json reflects the removal.
  - `TestDetachBySessionID`: opens two attaches, calls `DELETE /attachments` (no id), asserts both close.
  - `TestDetachArgParse`: table-driven parse of `<sess>/<att>` forms (no slash → all-attachments; with slash → single; trailing slash and double slash are usage errors).
  - `TestResolveScopedAttachmentID`: builds a fake state-dir with two sessions, each carrying one attachment whose IDs happen to share a prefix; asserts that `foo/abc` resolves uniquely to that session's attachment and does not bleed across sessions.
- CLI smoke (in worklog `## Evidence`):
  ```
  $ hoot run -- bash
  abc      # session shortid as today
  $ hoot list
  {"session":{"key":"abc","size":{"cols":80,"rows":24},"attachments":[]},...}
  $ hoot attach abc &      # in another terminal
  $ hoot list | jq .session.attachments
  [{"id":"x9k","size":{"cols":80,"rows":24},
    "origin":{"host":"laptop","term":"xterm-256color","term_program":"ghostty"}}]
  $ hoot detach abc/x9k    # session/attachment form
  hoot: detached attachment "abc/x9k"
  $ hoot detach abc        # session form — closes whatever's left
  hoot: detached 0 attachment(s) from session "abc"
  ```

## Open questions

- (resolved-as-default-pick) Should `hoot detach <session>` with 0 attachments be a success or an error? Picking *success* — the postcondition "no attachments on session X" holds either way; failing would be surprising in scripts.
- TZ / username on Origin: spec includes `User` (cheap), skips TZ. OK?

## Design notes

- 2026-05-06T11:45Z — Settled on session-scoped attachment IDs with a `<session>/<attachment>` CLI syntax. Both kinds use `shortid.Generate` against their own (small) candidate set: session keys against existing session keys (existing `generateKey`), attachment IDs against just the supervisor's own `attachments[*].id`. Cross-session uniqueness on attachment IDs is *not* required because the addressing scheme is always qualified by the session.
  - Alternatives considered (during this design):
    - **`shortid.Generate` per-session, CLI-side flat namespace + `AmbiguousIDError`** (very first draft): one namespace at the CLI, but cross-session collisions at 3-char width are real and surface as user-visible `AmbiguousIDError`. Loser because it pushes a real coordination problem onto the human, and there's no good way to disambiguate `abc` between two sessions from the CLI surface.
    - **Global flock allocator** (`<state-dir>/.ids.lock`, scan + mint + persist under lock): actually unique, but new shared state, two locks to compose with the per-session writer flock, and a coordinator pattern hootty has otherwise avoided. Briefly explored.
    - **Long base34 IDs everywhere via `crypto/rand` → uint64 → 13-char base34** (briefly the chosen path): unique by construction, no flock, but every `hoot run` echoes a 13-char string and every `<state-dir>/` directory name becomes 13 chars. Mid-design we noticed the simpler answer was just to qualify attachment IDs by their session — kicking the cross-session problem out of existence rather than overengineering past it.
    - **Session-scoped IDs with `/` separator** (picked, also the original suggestion in the very first round): no global namespace at all. Each session mints attachment IDs in its own tiny pool (3-char shortids essentially always). The CLI separator `<sess>/<att>` makes the addressing self-describing and matches the `/sessions/{key}/attachments/{id}` REST shape we were going to need on `hoot serve` anyway. Filesystem-paths-as-mental-model maps cleanly onto the on-disk reality (a session is a dir; attachments are entries under it).
  - Why this beats the earlier roundabouts: the cross-session uniqueness "problem" was self-inflicted by trying to keep one flat CLI namespace. Once you accept `<sess>/<att>` as the addressing form, both halves can use the existing tiny-shortid pool independently, no coordination needed, no entropy expansion, no migration.

- 2026-05-06T10:14Z — Final-frame "detached by hoot detach" reuses the existing best-effort error-frame path at `attach_handler.go:96-101` rather than introducing a new `MsgDetach` wire type. Alternatives:
  - **New `MsgDetach` (0x08)** with a structured reason: cleanest semantically, but requires a wire bump and client-side handling. Old clients without 0x08 would barf on an unknown frame type (`clientReadLoop` rejects unknown types). Not worth the version churn for a cosmetic message.
  - **Server closes silently, no message**: simplest, but the user on the detached terminal sees an unexplained EOF. Unfriendly for what is otherwise an admin action.
  - **Reuse `MsgOutput` with an inline message** (picked): zero protocol change, the bytes render as a yellow notice on the local terminal, and the existing graceful-detach handling on the client takes over from there.

- 2026-05-06T10:18Z — `hoot list` keeps JSON as default. Boss asked about `--json` flag explicitly; chose to make `--table` the additive flag rather than flip defaults, because plenty of scripts already pipe `hoot list` into `jq`. Adding `--json` as a no-op (or adding `--format=json|table`) would mean the CLI's default output is configurable, which historically becomes a source of "why does my pipeline break on this user's machine" surprises. JSON-by-default-with-table-flag is the minimum change that adds the new affordance.

- 2026-05-06T11:05Z — Reshaped `Hello` to share `PTYSize` and `Origin` with the state.json schema (was: flat `{Cols, Rows, ClientHost, ClientTerm, ...}`). One type for both wire and read-side, supervisor lifts the Hello straight into the registry record. Also dropped `AsciiCinemaPlayback*` fields (being nuked in parallel). Accepted as a protocol break — the attach protocol is intra-version-locked anyway, no semver.
  - Alternatives considered:
    - **Keep flat fields prefixed `Client*`** (previous draft): one struct, one decode, no shared types. Loser because the persisted `AttachmentRecord.Origin` would still need a sub-struct for the JSON shape, so the wire would diverge from disk and the supervisor would write a 5-line field-by-field shuffle. Cosmetic difference but not free; either you read the shuffle every time you trace a field through the system, or you eventually inline an `Origin` anyway.
    - **Embed `Origin` inline (no nested object) in Hello and disk** (`{"size":{}, "host":"", "term":""}`): also one type, shorter JSON. Loser because grouping origin metadata under an `origin` key reads better in `hoot list --table` JSON output and leaves room for future origin fields without polluting the top-level namespace.
    - **`Origin` as `*Origin` on Hello**: tiny pointer indirection, but lets the client send a missing `origin` field unambiguously. Loser because we need every-field-`omitempty` regardless (origin metadata is best-effort), and the zero-value `Origin{}` round-trips cleanly. Pointer adds nil-check noise on the supervisor side for no real win.

- 2026-05-06T10:55Z — Synced workspace; `hoot kill` (`cmd/hoot/kill.go`, master) is the authoritative model for `hoot detach`. Adopted its CLI flag layout, exit-code table (0/1/2/3/5), unix-socket dialer pattern (`dialUnixSock`), and remote `/sessions/{key}/<verb>` routing convention. The two CLIs should feel like siblings — both target one session, both have local + remote paths through identical machinery. Difference: `hoot kill` is `POST /signal` with a body; `hoot detach` is `DELETE /attachments[/{id}]` with no body — REST verb matches the action. Earlier draft of this spec mistakenly said `hoot kill` didn't exist; corrected.

- 2026-05-06T10:22Z — Stale-attachment cleanup is left to the next supervisor that takes over the key (which will overwrite state.json on `OpenWriter`). We deliberately do *not* add a `hoot list --gc` style cleanup. Rationale: a non-supervisor process rewriting state.json would violate the "Writer holds the dir flock and owns all writes to state.json" invariant in `store.go:99-105`. The cost of leaving stale `attachments[]` in a state.json belonging to a dead session is purely cosmetic — `hoot list --table` tags them `(stale)` and `hoot detach <stale-id>` errors with a friendly "session not alive" message.

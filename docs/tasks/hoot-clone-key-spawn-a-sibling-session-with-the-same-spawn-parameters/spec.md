# hoot clone session spawn parameters

## Goal

Add a clone workflow for `hoot`: given an existing session, spawn a fresh sibling session in the same state directory using the original command and working directory. The clone inherits environment through the normal `run -> __session -> process` chain at clone time, with optional clone-time env overrides. This is not a process fork or PTY duplication. The supervisor repeats the original spawn operation using persistent session metadata. The first delivery is an RFC-quality design for review; implementation waits until the `rfc: review spec.md` gate is cleared.

Out of scope for v1: moving a clone to a different host or state directory, persisting environment snapshots in state, replaying live process memory, and browser UI buttons. The HTTP API can support browser UI later, but this task only specifies the route.

## Architecture

### State schema

Add cloneable metadata directly to the library-owned `session` section of `state.json`:

```json
{
  "session": {
    "key": "abc123",
    "pid": 12345,
    "created_at": "2026-05-05T16:00:00Z",
    "argv": ["bash", "-lc", "echo hi; exec bash"],
    "cwd": "/Users/me/project"
  },
  "state": {}
}
```

Code shape:

- `state.go`
  - Add `Argv []string json:"argv,omitempty"` and `CWD string json:"cwd,omitempty"` to `SessionState`.
  - No migration path is required for old state dirs.
- `session.go`
  - Add `Argv []string` and `CWD string` to `SessionConfig`.
  - `Runner.Run` writes `cfg.Argv` and `cfg.CWD` into `initial.Session`.
  - Do not require metadata for existing embedders of the session library.
- `cmd/hoot/session.go`
  - Parse internal flags for session metadata, likely `--cwd`.
  - Normalize the state dir to an absolute path before any `cmd.Dir` changes.
  - Pass the parsed metadata to `session.New`.
- `cmd/hoot/run.go` and `cmd/hoot/serve_spawn.go`
  - Factor current PTY/session forking into a helper that accepts `spawnSpec`.
  - Initial `run` and `POST /sessions` construct `spawnSpec` from the requested argv and `os.Getwd()`.
  - Clone reuses the same helper with metadata loaded from the source state file.

State-dir compatibility:

- This schema change can invalidate existing state dirs.
- Users should remove old state dirs when upgrading into this feature.
- Clone only targets sessions created by a build that writes `session.argv` and `session.cwd`.
- Clone may treat missing/empty argv or cwd as corrupt state and return a plain error; no special backward-compatibility error type or old-session UX is required.

### Environment Inheritance and Overrides

Do not persist an environment snapshot in `state.json`.

The clone path should preserve the same process inheritance shape as the original run:

- Original run: `hoot run` (or `hoot serve`) spawns `hoot __session`; `__session` spawns the managed process.
- Clone: the clone requester spawns a new `hoot __session`; that `__session` spawns the managed process.

So the cloned process inherits from the new `__session` environment at clone time, then goes through the existing `RunCmdService` child env behavior. Today that means `sanitizeChildEnv(os.Environ())` still drops `TMUX`/`TMUX_PANE` and forces `TERM=xterm-256color` immediately before the managed process starts.

Rationale:

- It avoids writing secrets or bulky env snapshots to persistent state.
- It keeps the mental model simple: clone repeats the spawn with the same argv/cwd, but env comes from the supervisor doing the spawning now.
- It avoids inventing and versioning an `env_policy` field.
- It matches normal process semantics. If a user wants different env for a clone, they should provide overrides at clone time rather than depending on stale captured state.

Clone-time override surface:

- CLI:
  - `hoot clone --env NAME=VALUE <key>`
  - `hoot clone --env NAME <key>` copies `NAME` from the cloning CLI process environment.
  - `hoot clone --env-file <path> <key>` loads dotenv-style `NAME=VALUE` lines from a file.
  - Flags may repeat; later `--env` entries override earlier `--env-file` values.
- HTTP:
  - `POST /sessions/{key}/clone {"key":"optional-new-key","env":{"NAME":"VALUE"}}`
  - `POST /sessions/{key}/clone {"env_file":"/path/on-server/.env"}` is intentionally not included in v1 because it would read arbitrary server-side files through the HTTP API.
- Attach-side `<prefix>c`:
  - no override prompt in v1; it inherits the attach client / remote serve environment path as described above.

Env-file parsing is intentionally small:

- Accept `NAME=VALUE` and blank/comment lines.
- No command substitution, shell expansion, `export`, or multiline values.
- Malformed lines return an error.

### Clone Primitive

Add a shared helper in `cmd/hoot`, for example:

```go
type spawnSpec struct {
    Argv []string
    CWD  string
    EnvOverrides map[string]string
}

func spawnSessionFromSpec(stateDir, key string, spec spawnSpec) (sockPath string, err error)
func cloneSession(store *session.Store, stateDir, sourceQuery, newKey string) (*session.StateFile, error)
```

Behavior:

- Resolve `sourceQuery` using `Store.Resolve` so full keys and unique prefixes work.
- Validate `source.Session.Argv` and `source.Session.CWD`.
- Generate a collision-free key if `newKey` is empty.
- Reject an alive destination key with `409 Conflict` / CLI error, matching `run --key`.
- Spawn `hoot __session` with:
  - argv from `source.Session.Argv`
  - `cmd.Dir = source.Session.CWD`
  - `cmd.Env = os.Environ()` plus clone-time env overrides, if any
  - the same `stateDir`
  - the new key
  - metadata flags so the clone's new `state.json` carries the same `session.argv` / `session.cwd`
- Wait for the new `rpc.sock` and load the new `state.json`.
- Clone of a clone works because the cloned session writes the same argv/cwd metadata.

### CLI Surface

Add `hoot clone`:

```sh
hoot clone [--remote <url>] [--state-dir <dir>] [--key <new-key>] [--env NAME[=VALUE]] [--env-file <path>] [--attach] [--detach] <id-or-prefix>
```

Semantics:

- Local default: clone in the same `--state-dir`.
- Remote default: with existing `--remote <url>`, call `POST /sessions/{key}/clone` on that remote; the source and destination are both on the remote host/state dir. Cross-host cloning is out of scope.
- If `--key` is omitted, generate a short id like `run`.
- `--env` and `--env-file` affect only the newly spawned clone. They are not persisted into state, so clone-of-clone does not implicitly replay overrides.
- Print the new key on stdout.
- `hoot clone <key>` defaults to detached, matching `hoot run`.
- `--attach` clones, prints the new key, then attaches to the new session.
- `--detach` is accepted as an explicit spelling of the default and conflicts with `--attach`.
- Do not add a separate "clone onto another host" flag in v1.

Usage/help updates:

- `cmd/hoot/main.go` dispatches `clone` and documents it.
- `README.md` adds `hoot clone` examples and the state schema note.

### HTTP Surface

Add:

```http
POST /sessions/{key}/clone
Content-Type: application/json

{"key":"optional-new-key","env":{"NAME":"VALUE"}}
```

Response on success: `201 Created`, same envelope shape as `POST /sessions`:

```json
{
  "session": {"key": "new123", "pid": 12346, "created_at": "...", "argv": ["bash"], "cwd": "..."},
  "state": {"state": "running", "cmd": "bash", "pid": 12350, "started_at": "..."},
  "alive": true,
  "socket_path": "/.../new123/rpc.sock"
}
```

Status codes:

- `201 Created`: cloned and session socket is ready.
- `400 Bad Request`: malformed JSON or invalid requested key.
- `404 Not Found`: source key/prefix does not resolve.
- `409 Conflict`: ambiguous source prefix or destination key alive.
- `500 Internal Server Error`: corrupt source state, generate/spawn/load failures.

Register the route in `cmd/hoot/serve.go` at both bare and prefixed paths, like existing routes.

### Prefix-Key Binding

Add `<prefix>c` to `hoot attach` as "clone current session and switch to the new session".

Use one control-plane mechanism for local and remote: an HTTP `POST` made by the attach client outside the attachwire data stream.

Local attach path:

- `cmdAttach` already resolves the current key and knows `<state-dir>/<key>/rpc.sock`.
- The prefix handler calls `POST http://unix/clone` over the current session's `rpc.sock`.
- `RunCmdService` grows `StateDir` and `Key` fields populated by `cmdSession`; `RunCmdService.Run` registers `POST /clone` on `super.Mux()` and delegates to the same clone primitive used by the CLI and serve bridge. Keep this in `cmd/hoot` rather than adding clone semantics to the generic `session.Session` interface.
- On success, the attach client receives the new key/state, rebuilds its local dialer for `<state-dir>/<new-key>/rpc.sock`, closes the current attach connection, and the connect loop immediately attaches to the new session.

Remote attach path:

- The prefix handler calls `POST /sessions/{current-key-or-prefix}/clone` using `httpClient(remote)`.
- For `ssh://` remotes, this uses the existing SSH tunnel and remote `hoot serve`; no new transport is needed.
- On success, the attach client rebuilds `dialAttachRaw(remote, newKey)`, closes the current attach connection, and the connect loop immediately attaches to the new session.

Client UX:

- While cloning, print a dim status line on stderr: `[hoot: cloning <old>...]`.
- On success, the normal attach connect banner should show the new key and host.
- On failure, print `[hoot: clone failed: ...]` and keep the current attachment alive.
- Extend prefix help to include `"c" clone`.

Why not an attachwire clone frame:

- `attachwire` is intentionally PTY/session-control focused. Clone is a `hoot` application operation, not a generic session library operation.
- The existing `/attach-raw` bridge can pass unknown frames, but the browser WebSocket bridge would need new translation and the session library would need app-specific spawn knowledge.
- HTTP is already the control plane for spawn/list/close and works over both Unix-socket local control and remote `hoot serve`.

## Steps

- Inspect current spawn, state, serve, and attach paths.
- Write this RFC spec and park at the review gate.
- After RFC approval, add `argv`/`cwd` schema and metadata plumbing through `SessionConfig`.
- Implement clone-time env override parsing with focused unit tests.
- Factor spawn helpers so `run`, `POST /sessions`, CLI clone, HTTP clone, and per-session `/clone` share the same primitive.
- Add local `hoot clone` command and remote `hoot clone --remote`.
- Add `POST /sessions/{key}/clone` to `hoot serve`.
- Add per-session `POST /clone` for local attach-side clone.
- Add `<prefix>c` attach FSM behavior and target switching.
- Update README/usage.
- Add unit and E2E verification.

## Verification

Schema and env override unit tests:

- `go test ./...` continues to pass.
- Add tests for env override parsing:
  - `--env NAME=VALUE` sets one override.
  - `--env NAME` copies from the caller environment and errors if missing.
  - `--env-file` loads dotenv-style `NAME=VALUE` lines and rejects malformed lines.
  - later `--env` values override earlier `--env-file` values.
- Add state JSON test proving new sessions write `session.argv` and `session.cwd`.

CLI/HTTP behavior tests:

- `TestCloneSessionRejectsCorruptState`: source state without argv/cwd metadata returns an error.
- `TestCloneSessionPreservesMetadata`: source with argv/cwd spawns with the same spec and writes it to the cloned state.
- `TestCloneSessionEnvOverride`: clone-time env override reaches the managed process but is not written to state.
- `TestHandleClone`: `POST /sessions/{key}/clone` returns `201` and the new key.
- `TestCmdCloneRemote`: `hoot clone --remote <server> --key dst src` sends `POST /sessions/src/clone` with `{key:"dst"}` and prints `dst`.

End-to-end smoke:

```sh
make build
STATE="$(mktemp -d)"
CWD="$(mktemp -d)"
(
  cd "$CWD"
  bin/hoot run --state-dir "$STATE" --key src -- \
    bash -lc 'printf "cwd=%s clone_var=%s\n" "$PWD" "${CLONE_VAR:-}"; sleep 30'
)
bin/hoot clone --state-dir "$STATE" --key dst --env CLONE_VAR=visible src
jq '.session.argv, .session.cwd' "$STATE/dst/state.json"
```

Expected:

- `dst` exists and is alive.
- `session.cwd == $CWD`.
- `session.argv` matches the original `bash -lc ...`.
- `session` contains no env snapshot.
- Attaching to `dst` shows `clone_var=visible`, proving clone-time env overrides reach the managed process.

Clone-of-clone:

```sh
bin/hoot clone --state-dir "$STATE" --key dst2 dst
jq '.session.argv, .session.cwd' "$STATE/dst2/state.json"
```

Expected: `dst2` uses the same argv/cwd metadata and remains cloneable. It does not inherit the one-shot `CLONE_VAR=visible` override unless passed again.

Attach-side `<prefix>c`:

- Add an automated PTY/attach harness test if practical:
  - start a source session with a long-running shell command
  - attach with a test terminal
  - send `<prefix>c`
  - assert a second session appears in the state dir
  - assert the attach output reaches the new session connect banner
  - send input that only the new session receives
- If the current harness cannot drive prefix commands reliably, capture a transcript with `script`/PTY tooling and file a `#friction` entry before marking done.

HTTP smoke:

```sh
bin/hoot serve --bind 127.0.0.1:0 --state-dir "$STATE"
curl -sS -X POST http://127.0.0.1:<port>/sessions/src/clone -d '{"key":"httpdst"}'
```

Expected: `201`, response key `httpdst`, and `httpdst/state.json` contains propagated argv/cwd metadata without an env snapshot.

## Open questions

None blocking the RFC draft. The spec deliberately chooses defaults for the open questions in the section:

- clone does not persist env; it inherits the clone-time `__session` environment with explicit override flags
- old state dirs are not supported; users nuke old state dirs on upgrade
- bare CLI clone is detached by default
- clone-of-clone propagates argv/cwd metadata
- prefix clone uses HTTP control-plane calls for both local and remote attaches
- cross-host clone is out of scope

## Design notes

- 2026-05-05T16:59:13Z — Picked `session.spawn` as the state schema name instead of `session.clone`.
  - `clone` describes one operation; the persisted data is the original spawn contract. Future features such as "rerun", "restart with same argv", or "show command metadata" can reuse the same field without sounding clone-specific.
  - Superseded by the 2026-05-06T03:32:23Z entry, which flattened this into `session.argv` / `session.cwd`.
  - Alternative considered: top-level `clone` beside `session` and `state`. Rejected because `StateFile` already separates library-owned metadata under `session` from consumer-owned runtime state under `state`; spawn parameters are library/runner metadata, not service state.

- 2026-05-05T16:59:13Z — Chose a safe allowlisted environment snapshot over full env capture.
  - Full env capture would make clone behavior maximally faithful but writes tokens and API keys to `state.json`, which is durable and likely to be inspected, copied, or synced.
  - A minimal env would be safer but breaks shells, locale behavior, XDG-aware tools, and PATH lookup often enough that clone would feel unreliable.
  - The chosen default preserves basic shell and common language-tool ergonomics while keeping secret persistence out of v1. It also stores the post-`sanitizeChildEnv` env so `TERM`/tmux behavior stays consistent with current `hoot run`.

- 2026-05-05T16:59:13Z — Picked detached-by-default for bare `hoot clone`.
  - `hoot run` currently starts a supervised session and prints the key without attaching. Matching that behavior keeps `clone` script-friendly and avoids surprising terminal takeover from a shell command.
  - `<prefix>c` still attaches after clone because it is already inside an attach session and mirrors tmux's new-window UX.
  - Alternative considered: default attach for all `hoot clone` calls with `--detach` for scripts. Rejected because it makes CLI clone differ from `run` and makes automation easier to get wrong.

- 2026-05-05T16:59:13Z — Picked HTTP control-plane clone for the prefix binding instead of an attachwire command frame.
  - The local attach path can speak HTTP over `rpc.sock`; the remote path can speak HTTP to `hoot serve` directly or over the existing SSH tunnel. That gives both paths the same conceptual mechanism.
  - An attachwire frame would couple the generic attach protocol to `hoot` process-spawn semantics and force extra browser bridge translation. HTTP is already where spawn, list, resolve, and close live.
  - This does add a small per-session `POST /clone` route for local attach. The route delegates to the same helper as `hoot clone`/serve clone, so behavior does not fork.

- 2026-05-05T16:59:13Z — Added `--clone-env` / `clone_env` as a v1 opt-in allowlist extension.
  - What changed: the first draft shipped only the default env policy. Re-reading the "too narrow breaks the cloned process" constraint made that feel unnecessarily brittle for project-specific non-secret env like `FEATURE_PROFILE`, `DEVBOX`, or language tool paths not covered by the defaults.
  - The flag takes names, not values, so the source of truth is still the process environment at spawn time.
  - The sensitive-name denylist still wins. That keeps the escape hatch useful for non-secret project shape without silently turning state.json into a credential store.

- 2026-05-06T03:25:02Z — Superseded env snapshotting entirely: clone now inherits env through the fresh `__session` spawn chain and supports clone-time overrides.
  - Human steering: the clearer model is `run -> __session -> process`; clone repeats that spawn now. The new `__session` already has an environment, and the managed process already goes through `RunCmdService` env handling. Persisting env in `StateFile` adds complexity without being necessary for the mental model.
  - Removed from the active spec:
    - `session.spawn.env`
    - `session.spawn.env_policy`
    - safe allowlist / denylist policy
    - `hoot run --clone-env` and `POST /sessions clone_env`
  - Added instead:
    - `hoot clone --env NAME=VALUE`
    - `hoot clone --env NAME` to copy a value from the caller environment
    - `hoot clone --env-file <path>` for local dotenv-style clone overrides
    - HTTP `POST /sessions/{key}/clone {"env":{"NAME":"VALUE"}}`
  - Tradeoff: clone-of-clone no longer replays one-shot env overrides. That is intentional because overrides are execution-time inputs, not original spawn metadata. Users pass them again when wanted.

- 2026-05-06T03:31:36Z — Removed tags from the active schema.
  - Human steering: do not spec `Tags []string` unless the existing code already has it.
  - Repo check: `rg "Tags|tags|--tag|tag"` found no session tag field or public tag flag in the current implementation; matches were unrelated docs, build tags, or protocol "type tags".
  - Removed from the active spec:
    - `session.spawn.tags`
    - `SpawnMetadata.Tags`
    - repeated internal `--tag`
    - tag preservation verification
  - Follow-on: if a future task adds real session tags, clone can extend `SpawnMetadata` then. This task should stay scoped to metadata that exists or is required now: argv and cwd.

- 2026-05-06T03:32:23Z — Flattened clone metadata from `session.spawn.{argv,cwd}` to `session.argv` / `session.cwd`.
  - Human steering: the nested `spawn` object is "same same but different" for only two fields. There is no current env or tag payload left in it, so the extra object does not carry its weight.
  - Active schema is now direct fields on `SessionState`: `Argv []string` and `CWD string`.
  - Alternatives considered:
    - Keep `session.spawn`: still semantically tidy, but after removing env and tags it mostly adds JSON depth and a new struct name.
    - Flatten to `session.argv` / `session.cwd` (picked): smaller state file, simpler validation, fewer Go types, and matches the section's concrete requirements exactly.

- 2026-05-06T03:33:29Z — Removed backward-compatibility requirements for old state dirs.
  - Human steering: no need for old state compatibility; users can nuke old state dirs.
  - Active behavior: clone assumes source sessions were created by a build that writes `session.argv` and `session.cwd`.
  - Missing/empty argv or cwd is treated as corrupt state, not a special "old session has no clone metadata" compatibility case.
  - Removed from the active spec:
    - old-state JSON compatibility tests
    - typed no-clone-metadata error
    - HTTP 409 mapping for old sessions
    - CLI message explaining that a session was created by an older `hoot`

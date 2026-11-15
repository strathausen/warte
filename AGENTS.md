# Agent guide

## Start here

warte is a Go terminal Signal client running as a **linked device**, not an account-registration client or Matrix bridge. The root module is `warte`; the bundled Signal dependency is a separate module under `fork/mautrix-signal`, selected by a root `go.mod` replacement.

Read these on demand rather than treating all documentation as implementation truth:

- `docs/ARCHITECTURE.md`: intended layer boundaries and engineering conventions.
- `docs/TUI.md`: overlay contracts, input dispatch, image pipeline, and feature integration.
- `internal/core/service.go` and `model.go`: frontend/backend contract and domain types.
- `internal/backend/doc.go` and `internal/store/doc.go`: backend and persistence details.
- `internal/config/sample.toml`: configuration options and defaults.
- `fork/README.md` and `fork/CHANGES.md`: local dependency changes.
- `SPEC.md`: product scope, not proof that a feature is implemented.

Some architecture prose is broader than current behavior: store requires cgo; CLI/demo link the real backend; the database is SQLCipher-encrypted; external editors/openers deliberately use protected plaintext temporary files. Follow the concrete constraints below.

## Build, run, and verify

Run commands from the repository root. Requirements are Go 1.26 or newer, a C compiler/cgo, and Rust/Cargo for the initial libsignal build. The first native build also needs Git/network access.

| Command | Purpose |
| --- | --- |
| `make` or `make build` | Build the native library if missing, then `./warte`. |
| `make libsignal` | Build `lib/libsignal_ffi.a` from the upstream commit pinned by the required mautrix-signal version. |
| `make test` | Run root-module `go test ./...` with production FTS5 and native linker settings. |
| `make vet` | Run root-module `go vet ./...` with the same settings. |
| `make demo` | Build and launch the in-memory demo; no Signal connection. See path-isolation precautions below. |
| `make run` | Build and start the **real** client, linking if necessary. |
| `make install PREFIX="$HOME/.local"` | Build and install to the selected prefix's `bin` directory. |
| `make build VERSION=<version>` | Stamp the CLI version; default is `dev`. |
| `make clean` | Remove only the root executable, not native build caches. |

Use `gofmt` on changed Go files. Although `lint` is listed as phony in the Makefile, it has no recipe; use `make vet`, not `make lint`, as the implemented check.

### Native build traps

- Make exports `CGO_ENABLED=1`, `GOFLAGS` containing `-tags=sqlite_fts5`, and `CGO_LDFLAGS` pointing at the local library plus macOS Security/CoreFoundation/SystemConfiguration frameworks. Bare `go build` or full `go test ./...` does not reproduce that environment.
- Every executable, including `--demo`, links libsignal because the CLI imports the real backend unconditionally. Demo avoids Signal runtime access, not native build dependencies.
- `lib/` and `third_party/` are ignored build products. The maintained Go fork is `fork/mautrix-signal/`; `third_party/mautrix-signal/` is an upstream checkout used only to build Rust. Do not make application fixes there.
- The archive target is cached by existence. Updating a dependency pin does not automatically invalidate an existing `lib/libsignal_ffi.a`; rebuild the matching native artifact when changing pins.
- For native ABI diagnosis, `internal/deps/linkcheck_test.go` supplies an optional `linkcheck`-tagged test; it still requires the Makefile linker environment.
- Root `./...` checks do **not** traverse the nested fork module. Fork changes require checks in that module too, with the appropriate native environment. Its own `go.mod` contains a toolchain directive.
- Nested upstream workflows and Docker/build scripts are not root warte CI/build entry points. Some refer to bridge directories removed from this copy.

For changes confined to frontend/domain packages, targeted `go test` invocations do not need the Rust archive. Store tests still need a C compiler; use `-tags sqlite_fts5` to exercise production search behavior.

## Architecture and change placement

| Area | Responsibility |
| --- | --- |
| `cmd/warte` | Flags/subcommands, configuration and logging, key/store setup, process lock, link/unlink/export, real versus fake service wiring. |
| `internal/core` | Domain types, service/linking interfaces, events; standard-library dependencies only. |
| `internal/backend` | Real `core.Service`: signalmeow/libsignal adapter, conversion, connection/history/sync, queued delivery, media and expiry. |
| `internal/store` | Shared encrypted database, app migrations/search, attachment metadata, encrypted blob files, key management. |
| `internal/fake` | Concurrent in-memory service, deterministic fixtures, simulated linking and live demo activity. |
| `internal/tui` | Bubble Tea v2 shell, layout, conversation/composer, event pump, overlays and image scheduling. |
| `internal/tui/ui` | Shared contracts, messages, registries and shell integration hooks. |
| `internal/tui/views` | Registered feature packages: emoji, finder, forms, info, media, stories; `all` wires them together. |
| `internal/tui/keymap`, `theme` | Modal actions/bindings and themed, sanitized terminal rendering. |
| `internal/format`, `termimg`, `notify`, `config` | Text coordinates/formatting, terminal image protocols, notifications, TOML and platform paths. |

Keep frontend packages independent of `backend`, `store`, and Signal-specific packages. They use `core.Service`. Production Signal integration belongs in `backend`; store compatibility tests and dependency tooling are exceptions to the otherwise backend-only Signal import boundary.

For a new service capability, update the core contract, real implementation, fake implementation, frontend wiring and relevant tests together. Keep blocking work in context-aware service methods or Bubble Tea commands, not rendering. Use cleanup/cancellation for goroutines and service lifetimes.

### State and event flow

- `Service` methods are concurrent-safe. Queries read local state; actions may use the network. Media access may download even though it looks like a read.
- `Events()` has **one consumer**. Frontend-originated mutations also emit events; events are hints for refreshing state, not an authoritative durable log.
- Real incoming callbacks are serialized in `backend/handle.go`. Conversion and persistence precede event publication; attachment downloads follow. App writes, backend indexing and event publication are not one atomic transaction, so preserve deduplication and retry behavior when changing ingestion.
- `Send` persists and announces a pending message, then delivers in a backend-owned background job. Returning a message ID does not mean network delivery succeeded; status changes arrive as updates. Delivery survives the initiating caller's context, bounded by backend lifetime and its delivery timeout.
- Backend events use an unbounded FIFO so producers do not block on the UI. Do not add a competing consumer; an abandoned consumer can also accumulate queued events. Close cancels workers and drops remaining queued events.
- Chat IDs distinguish `u:<aci>` and `g:<group-id>`. Message identity includes chat, author and millisecond timestamp. Do not substitute a timestamp alone for full identity. `Messages` pages are returned oldest-first; store pagination includes tie-breakers beyond timestamps.

## Extending the TUI

Read `docs/TUI.md` before adding a view. The important wiring is deliberately indirect:

1. Feature packages import `internal/tui/ui`, **not** root `tui`, and register commands/actions through `init` functions.
2. `internal/tui/views/all` imports features and installs sidebar, forwarding, pinned-strip, shutdown and other hooks.
3. `cmd/warte` blank-imports `views/all`. Importing only `tui` does not install the full feature set.

Registrations are process-global. Register before constructing an App; commands and sidebar setup are consulted during construction. Tests changing registrations or CLI seams must restore them and should not run those mutations in parallel. Shutdown hooks must tolerate repeated calls.

Overlay `Update` returns `nil` to close; use `ui` messages to ask the shell to open chats, insert text, react, show toasts or push overlays. Routed keys arrive as `ui.ActionMsg`. Text-entry views use `ui.KeyCapturer`; numeric keys otherwise become Normal-mode count prefixes. Read colors through `ctx.Styles`, which is updated in place when themes change.

### Rendering invariants

- The App re-arms its single event waiter after each event and forwards events to the sidebar and top overlay. App closure cancels stale waiters so relinking cannot leave an old App consuming a new App's events.
- `Update` renders immediately, **then drains image requests discovered during rendering into commands**. `View` normally returns that cached frame. Preserve this order.
- Conversation rendering is virtualized and block-cached. Changes affecting grouping, selection, revision, width, theme, images or expiring content must invalidate or participate in cache keys; otherwise a successful state change can remain visually stale.
- Frame lines must have exact ANSI-aware terminal cell widths. Use `lipgloss.Width` / `ansi.StringWidth`, not byte or rune length. Preserve the Bubble Tea grapheme-width setup in `App.Init`; it prevents renderer/layout disagreement on emoji.
- Signal body ranges use **UTF-16 code units**; search snippet match offsets use **bytes**; layout uses **terminal cells**. Use `internal/format` conversion/wrapping helpers rather than mixing coordinate systems. Test surrogate pairs, grapheme clusters and ZWJ emoji.
- Sanitize untrusted names, bodies and filenames before styling (`theme.SanitizeLine` / `theme.Sanitize`, or the format rendering pipeline). Storage does not sanitize raw domain values on read.
- Never write terminal escape sequences directly to stdout inside the TUI; use `tea.Raw`.
- Kitty images use cell placeholders plus out-of-band pixels. iTerm2/sixel fall back to half-blocks in scrolling conversations. Image fetch/decode/resize is command-driven with four workers. In tmux, automatic kitty mode depends on `allow-passthrough`.

## Persistence and security constraints

- SQLCipher (`github.com/mutecomm/go-sqlcipher/v4`) registers driver `sqlite3`. **Never import `github.com/mattn/go-sqlite3` into runtime code alongside it**: C symbols and driver names collide.
- `internal/deps/deps.go` is `tools`-tagged dependency retention, not an application package. It retains glamour and go-sqlite3; do not build the application with `-tags tools`. Preserve intentional pins through the documented dependency workflow rather than casually rewriting `go.mod` or dropping retained dependencies.
- Production requires `sqlite_fts5`. Untagged store tests fall back to FTS4. Opening an FTS4 database with FTS5 upgrades its index; that database then fails to open in a binary without FTS5.
- Bundled SQLite is 3.33: no `RETURNING`; JSON1 requires another build tag. Existing store SQL avoids both.
- warte and signalmeow share the same database. App tables use `warte_` prefixes and contiguous embedded migrations in `internal/store/migrations`, tracked in `warte_version`. Signalmeow upgrades and backend index setup are separate; retain shared-schema compatibility tests.
- Prefer store mutation helpers over raw SQL: message JSON, indexed columns, FTS, receipts, unread counters and chat previews must stay synchronized. `PutMessage` preserves existing unread state; `UpdateMessage` handles partial mutation. Derived chat fields are recomputed on read.
- Attachment blobs are encrypted and deduplicated. Metadata deletion and blob garbage collection are separate; respect shared references and the grace period for blob-before-metadata writes.
- The master secret lives in the OS keychain, or comes from an argon2id passphrase fallback; database and blob keys are separately derived. Do not silently replace missing keys for an existing store.
- Never add logs containing message bodies, phone numbers or keys outside the explicit `--log-sensitive` policy.
- Backend media methods return decrypted bytes in memory. Existing external-editor/open-attachment features are explicit plaintext-disk exceptions using mode-0600, PID-tagged temporary files and cleanup (`internal/tui/tempfiles.go`). Do not introduce incidental plaintext persistence or bypass that lifecycle.
- `export` is an encrypted application-data export, **not a complete Signal backup**: it omits Signal keys/sessions and attachment bytes and needs the same blob key to decrypt.

## Testing patterns

Tests live next to implementation and commonly use table-driven cases. Prefer existing helpers rather than constructing ad hoc timing and environment scaffolding:

- **TUI unit tests:** `internal/tui/helpers_test.go` supplies a deterministic harness with fixed clock/seed, half-block images, no-op notifications, temporary lock/temp paths and explicit event pumping. Its bounded command runner skips timers; it is not a full Bubble Tea runtime.
- **Full feature integration:** `internal/tui/views/all` tests load all registrations and exercise overlays through shell actions/keys. Cover registration and integration, not just a view constructor.
- **Real terminal model:** `internal/tui/teatest_test.go` and `width_test.go` exercise a real Bubble Tea program. Width/hostile-text tests check exact dimensions and terminal-injection resistance; preserve these checks when changing layout or sanitization.
- **Fake service timing:** a zero latency selects the default delay; **negative latency means immediate**. Live activity begins after `Start`. Close both App and service with test cleanup.
- **Backend:** helper fixtures use temporary encrypted stores and injected clocks/storage seams. `nonetwork_test.go` replaces HTTP layers and fails the package if an unexpected network request is attempted. Use conversion tests and injected fork transports rather than real Signal access.
- **Store:** shared-schema, search, encryption, hardening and blob tests cover behavior not represented by the in-memory fake. Do not treat fake ordering/read tracking as a complete persistence specification.
- **CLI:** reuse `isolate` and seam helpers in `cmd/warte/main_test.go`; these restore globals and isolate config/data/state/cache and keyring access.

## Safe manual testing and paths

`--config` / `WARTE_CONFIG` selects only the config file; it does **not** isolate data, logs or cache. Absolute XDG overrides work on macOS as well as Linux. Isolate all roots for manual experiments, following the CLI test helper:

```sh
root="$(mktemp -d)"
export WARTE_CONFIG="$root/config.toml"
export XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data"
export XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache"
export WARTE_KEYRING=mock
unset WARTE_PASSPHRASE
./warte --demo
```

Run the UI in an actual terminal. Demo still initializes logging and can persist settings or a screen-lock password; it is not a no-write mode. The mock keyring is forgotten on exit, so use it **only with disposable data**. `WARTE_KEYRING=file` instead selects passphrase-backed storage. Never use real linking/unlinking, clipboard, desktop notifications or external openers as incidental verification. One process per data directory is enforced by a lock file.

## Working on the bundled fork

The fork is intentionally small relative to upstream: bridge `cmd/` and `pkg/connector/` were removed. Follow `fork/README.md`: local changes are marked with `// warte:` and recorded in `fork/CHANGES.md` for upstreamability. Preserve this provenance when making fork changes. Dependency tests and generated protobuf sources live in the nested module; root checks alone do not validate edits there.

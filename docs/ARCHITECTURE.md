# warte architecture

See `SPEC.md` for product scope. This file is the engineering contract.

## Layers

```
cmd/warte            CLI entry: flags, subcommands (link, unlink, export), --demo
internal/tui         Bubble Tea v2 app (views, overlays, commands, mouse)
internal/tui/keymap  modal vim-style key engine (pure, no Bubble Tea types in its core)
internal/tui/theme   Lip Gloss v2 themes
internal/termimg     terminal image protocols: kitty (unicode placeholders), iTerm2, sixel, half-blocks
internal/format      Signal body ranges <-> styled spans, composer markup, emoji shortcodes, wrapping
internal/notify      desktop notifications (OSC 9/99/777, native, bell), terminal title
internal/config      TOML config, XDG paths, keymap overrides
internal/core        domain model + Service interface + events   (no deps besides stdlib)
internal/fake        in-memory Service with demo data (used by --demo and TUI tests)
internal/store       app persistence: chats, messages, FTS5 search, encrypted blob store
internal/backend     real Service: signalmeow + libsignal (cgo) + internal/store
```

Dependency rules:

- `core` imports only the standard library.
- Frontend packages (`tui/...`, `format`, `termimg`, `notify`, `config`) never import `backend`, `store`, signalmeow or libsignal. They talk to `core.Service` only.
- `backend` is the only package that imports `go.mau.fi/mautrix-signal/...`, and it requires cgo plus `lib/libsignal_ffi.a` (`make libsignal`). Everything else builds and tests with plain `go test`.
- Don't edit `go.mod` by hand. Its dependencies are pinned in `internal/deps/deps.go` (build tag `tools`).

## Conventions

- Go 1.26, `gofmt`, `go vet` clean. Table-driven tests next to the code.
- Contexts on every blocking call; no goroutine leaks (tests use `t.Cleanup`).
- Never log message bodies, phone numbers, or keys unless `--log-sensitive`.
- Never write decrypted message content or attachments to disk outside the encrypted stores.
- UI strings in plain English; keep them short.

## Data at rest

- `~/.local/share/warte/` (macOS: `~/Library/Application Support/warte/`)
  - `warte.db`: SQLite. Holds signalmeow's tables (keys, sessions, groups) plus warte's app tables (chats, messages, reactions, FTS5). The file is 0600.
  - `blobs/`: attachments, avatars and stickers, AES-256-GCM encrypted with a random data key.
- The data key lives in the OS keychain (`go-keyring`, service `warte`). If no keychain is available, a passphrase-derived key (argon2id) is used.

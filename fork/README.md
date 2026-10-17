# Forked dependencies

`mautrix-signal/` is a copy of [mautrix/signal](https://github.com/mautrix/signal)
(AGPL-3.0) at commit d737b89, minus `cmd/` and `pkg/connector/`. warte uses it
through a `replace` directive in `go.mod`. Every change is marked with a
`// warte:` comment and listed in `CHANGES.md`, so it can be sent upstream.

libsignal itself is not forked. `make libsignal` builds the exact libsignal
commit pinned by this copy's submodule.

package main

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/tui"
)

// Seams between the CLI and the real Signal backend (internal/backend:
// signalmeow + libsignal via cgo). backend_real.go implements them; tests
// replace them so they never reach Signal's servers.
//
// The binary always links the real backend, so it needs lib/libsignal_ffi.a
// at build time ("make libsignal" builds it).
var (
	// openRealEnv opens the local account state up front: it locks the data
	// directory and loads the keys, asking for a passphrase on the terminal
	// if there is no keychain. Commands call it before any screen or signal
	// handler takes over the terminal.
	openRealEnv = func(cfg config.Config) error {
		_, err := getEnv(cfg)
		return err
	}
	// newRealService returns a new, not yet started backend over the
	// process's local account state (opened once; see openEnv). Start may
	// return core.ErrNotLoggedIn, which means linking comes first.
	newRealService = func(ctx context.Context, cfg config.Config) (core.Service, error) {
		return realService(ctx, cfg)
	}
	// newRealLinker returns the device-linking flow.
	newRealLinker = func(ctx context.Context, cfg config.Config) (core.Linker, error) {
		return realLinker(ctx, cfg)
	}
	// realIsLinked reports whether a Signal account is linked.
	realIsLinked = func(ctx context.Context, cfg config.Config) (bool, error) {
		return realLinked(ctx, cfg)
	}
	// realUnlink removes this device from the account (and tells the server).
	// It returns core.ErrNotLoggedIn if no account is linked.
	realUnlink = func(ctx context.Context, cfg config.Config) error {
		return realUnlinkDevice(ctx, cfg)
	}
	// realWipe deletes the local data (database, attachments, key) and
	// returns the data directory.
	realWipe = func(cfg config.Config) (string, error) {
		return realWipeData(cfg)
	}
	// realExport writes an encrypted export of the account to path.
	realExport = func(ctx context.Context, cfg config.Config, path string) error {
		return realExportBackup(ctx, cfg, path)
	}
	// realLockVerifier returns the screen lock's passphrase check: the data
	// passphrase when the local data is passphrase-protected, nil (the TUI's
	// own lock password) when the keys are in the OS keychain.
	realLockVerifier = func() tui.Verifier { return dataPassphraseVerifier() }
	// closeEnv releases the local account state (backend log, database,
	// lock). run defers it.
	closeEnv = func() { closeRealEnv() }

	// linkProgramOptions are extra options for the link screen's program
	// (tests run it without a terminal).
	linkProgramOptions []tea.ProgramOption
)

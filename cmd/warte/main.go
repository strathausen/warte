// Command warte is a keyboard-first terminal Signal client.
//
// Usage:
//
//	warte [flags]                 run the client
//	warte link [flags]            link this device to your phone (QR code)
//	warte unlink [--yes] [flags]  unlink this device (and delete local data)
//	warte export [flags] <path>   export a backup
//
// Without --demo warte talks to Signal through internal/backend (signalmeow
// and libsignal; the build needs lib/libsignal_ffi.a, see "make libsignal").
// backend.go holds the seams, backend_real.go the wiring: one locked data
// directory, keys from the OS keychain (or a passphrase), one database.
// With --demo the client runs against the built-in demo account
// (internal/fake), and "warte link --demo" shows the linking screen with a
// simulated phone.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/log/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui"
	"warte/internal/tui/theme"
	"warte/internal/tui/views/forms"

	// Feature views (finder, emoji, media, stories, forms, info) and their
	// wiring into the shell.
	_ "warte/internal/tui/views/all"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the flags shared by every subcommand.
type options struct {
	demo       bool
	demoNotify bool
	cfgPath    string
	themeName  string
	mouse      bool
	noMouse    bool
	version    bool
	yes        bool
	logSens    bool
}

func newFlagSet(name string, stderr io.Writer, o *options) *flag.FlagSet {
	return newSubFlagSet(name, "", stderr, o)
}

// newSubFlagSet adds the flags of subcommand sub ("" for the client).
func newSubFlagSet(name, sub string, stderr io.Writer, o *options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.demo, "demo", false, "use the built-in demo account (no Signal connection)")
	fs.BoolVar(&o.demoNotify, "notify", false, "with --demo: send desktop notifications for simulated messages")
	fs.StringVar(&o.cfgPath, "config", "", "path to config.toml (default: $WARTE_CONFIG or the XDG config dir)")
	fs.StringVar(&o.themeName, "theme", "", "theme name (overrides config)")
	fs.BoolVar(&o.mouse, "mouse", false, "enable mouse support")
	fs.BoolVar(&o.noMouse, "no-mouse", false, "disable mouse support")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	fs.BoolVar(&o.logSens, "log-sensitive", false, "allow phone numbers and server responses in the log (debugging)")
	if sub == "unlink" {
		fs.BoolVar(&o.yes, "yes", false, "do not ask: unlink and delete the local data")
	}
	return fs
}

const usage = `usage:
  warte [flags]                 run the client
  warte link [flags]            link this device to your phone
  warte unlink [--yes] [flags]  unlink this device, optionally delete local data
  warte export [flags] <path>   export an encrypted backup to path

environment:
  WARTE_CONFIG       config file path
  WARTE_PASSPHRASE   data passphrase when no OS keychain is available (scripts)

flags:`

func run(args []string, stdout, stderr io.Writer) int {
	sub := ""
	if len(args) > 0 {
		switch args[0] {
		case "link", "unlink", "export", "run":
			sub, args = args[0], args[1:]
		case "help":
			fmt.Fprintln(stderr, usage)
			newFlagSet("warte", stderr, &options{}).PrintDefaults()
			return 0
		}
	}
	var o options
	fs := newSubFlagSet("warte", sub, stderr, &o)
	fs.Usage = func() {
		fmt.Fprintln(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if o.version {
		fmt.Fprintln(stdout, "warte", version)
		return 0
	}

	cfg, warns, err := config.Load(o.cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "warte:", err)
		return 1
	}
	if o.themeName != "" {
		cfg.Theme = o.themeName
	}
	if o.mouse {
		cfg.Mouse = true
	}
	if o.noMouse {
		cfg.Mouse = false
	}
	if o.logSens {
		cfg.LogSensitive = true
	}

	closeLog := setupLog(cfg)
	defer closeLog()
	con = newConsole(stderr)
	// Close the backend's database, log and lock after the subcommand.
	defer closeEnv()

	switch sub {
	case "link":
		return runLink(o, cfg, stdout, stderr)
	case "unlink":
		return runUnlink(o, cfg, stdout, stderr)
	case "export":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: warte export [flags] <path>")
			return 2
		}
		return runExport(o, cfg, fs.Arg(0), stdout, stderr)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "warte: unknown command %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	return runClient(o, cfg, warns, stderr)
}

// runClient runs the TUI. Without a linked account the link screen comes
// first; when the user asks to relink from the logged-out screen, the link
// flow runs and the client starts again.
func runClient(o options, cfg config.Config, warns []config.Warning, stderr io.Writer) int {
	var w []string
	for _, x := range warns {
		w = append(w, x.String())
	}
	if o.demo {
		if !o.demoNotify {
			// Simulated contacts shouldn't pop real desktop notifications.
			cfg.Notifications = config.NotifyOff
			cfg.NotifyMethod = config.MethodOff
		}
		f := fake.New(fake.Options{Seed: 1, Live: true, Latency: 300 * time.Millisecond})
		defer f.Close()
		for {
			relink, code := runApp(f, o, cfg, w, false, stderr)
			if !relink {
				return code
			}
			if code := linkFlow(o, cfg, io.Discard, stderr); code != 0 {
				return code
			}
			w = nil
		}
	}

	// Keys (and a passphrase prompt, if needed) come before any screen.
	if err := openRealEnv(cfg); err != nil {
		fmt.Fprintln(stderr, "warte:", err)
		return 1
	}
	ctx := context.Background()
	justLinked := false
	for {
		svc, err := newRealService(ctx, cfg)
		if err != nil {
			fmt.Fprintln(stderr, "warte:", err)
			return 1
		}
		err = svc.Start(ctx)
		if errors.Is(err, core.ErrNotLoggedIn) {
			_ = svc.Close()
			if justLinked {
				fmt.Fprintln(stderr, "warte: linking finished but no linked account was found; see the log in", logHint())
				return 1
			}
			if code := linkFlow(o, cfg, io.Discard, stderr); code != 0 {
				return code
			}
			justLinked = true
			continue
		}
		if err != nil {
			_ = svc.Close()
			fmt.Fprintln(stderr, "warte:", err)
			return 1
		}
		justLinked = false
		relink, code := runApp(svc, o, cfg, w, true, stderr)
		if err := svc.Close(); err != nil {
			log.Error("closing backend", "err", err)
		}
		if !relink {
			return code
		}
		w = nil
		if code := linkFlow(o, cfg, io.Discard, stderr); code != 0 {
			return code
		}
		justLinked = true
	}
}

// runApp runs the main TUI once over svc. relink reports that the user asked
// to link again from the logged-out screen.
func runApp(svc core.Service, o options, cfg config.Config, w []string, started bool, stderr io.Writer) (relink bool, code int) {
	opts := tui.Options{Warnings: w, Version: version, ConfigPath: o.cfgPath, SkipStart: started}
	if started {
		// The real account (not --demo): with passphrase-protected data the
		// data passphrase unlocks the screen lock.
		opts.LockVerifier = realLockVerifier()
	}
	app := tui.New(svc, cfg, opts)
	err := runProgram(tea.NewProgram(app))
	app.Close()
	if err != nil {
		log.Error("program exited", "err", err)
		fmt.Fprintln(stderr, "warte:", err)
		return false, 1
	}
	return app.RelinkRequested(), 0
}

// runProgram runs p. The terminal going away (SIGHUP) or a SIGTERM would
// kill the process before the caller removes decrypted temp files, stops
// players and closes the database; both become a regular shutdown instead.
func runProgram(p *tea.Program) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM)
	var killed atomic.Bool
	go func() {
		if sig, ok := <-sigs; ok {
			log.Info("shutting down", "signal", sig.String())
			killed.Store(true)
			p.Kill()
		}
	}()
	_, err := p.Run()
	signal.Stop(sigs)
	close(sigs)
	if killed.Load() && errors.Is(err, tea.ErrProgramKilled) {
		return nil // a requested shutdown, not a failure
	}
	return err
}

// signalContext is a context cancelled by a timeout, Ctrl-C, SIGTERM or
// SIGHUP (for the non-interactive subcommands).
func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, func() { cancel(); stop() }
}

// logHint names the log file for error messages.
func logHint() string {
	if p, err := config.LogPath(); err == nil {
		return p
	}
	return "the log file"
}

// linker returns the demo or real linker.
func linker(o options, cfg config.Config) (core.Linker, error) {
	if o.demo {
		return &fake.Linker{Seed: 1}, nil
	}
	return newRealLinker(context.Background(), cfg)
}

// runLink is "warte link": the link flow, unless an account is linked.
func runLink(o options, cfg config.Config, stdout, stderr io.Writer) int {
	if !o.demo {
		if err := openRealEnv(cfg); err != nil {
			fmt.Fprintln(stderr, "warte link:", err)
			return 1
		}
		ctx, cancel := signalContext(time.Minute)
		linked, err := realIsLinked(ctx, cfg)
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "warte link:", err)
			return 1
		}
		if linked {
			fmt.Fprintln(stderr, "warte link: this device is already linked to a Signal account.")
			fmt.Fprintln(stderr, `Run "warte unlink" first to link it again.`)
			return 1
		}
	}
	return linkFlow(o, cfg, stdout, stderr)
}

// linkFlow shows the full-screen QR linking flow.
func linkFlow(o options, cfg config.Config, stdout, stderr io.Writer) int {
	l, err := linker(o, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "warte link:", err)
		return 1
	}
	th, _ := theme.Resolve(cfg.Theme, true)
	if cfg.NoColor {
		th, _ = theme.Get(theme.MonoName)
	}
	name := cfg.DeviceName
	if name == "" {
		name = config.AppName
	}
	m := forms.LinkModel(l, name).WithStyles(theme.NewStyles(th)).WithReduceMotion(cfg.ReduceMotion)
	if err := runProgram(tea.NewProgram(m, linkProgramOptions...)); err != nil {
		fmt.Fprintln(stderr, "warte link:", err)
		return 1
	}
	self, err := m.Result()
	switch {
	case errors.Is(err, forms.ErrLinkCanceled), errors.Is(err, context.Canceled):
		// Quitting can race the linker's own "canceled" error.
		fmt.Fprintln(stderr, "warte link: canceled")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "warte link:", err)
		return 1
	}
	// Never print the raw phone number: a name or username, else the
	// number masked (forms.LinkedName, as the link screen shows it).
	who := theme.SanitizeLine(forms.LinkedName(self))
	if who == "" {
		who = "your account"
	}
	fmt.Fprintln(stdout, "Linked to "+who+". Run warte to start.")
	return 0
}

// runUnlink is "warte unlink": confirm, unlink, then offer to delete the
// local data. --yes answers yes to both questions.
func runUnlink(o options, cfg config.Config, stdout, stderr io.Writer) int {
	if o.demo {
		fmt.Fprintln(stdout, "demo account: nothing to unlink")
		return 0
	}
	if !o.yes && !con.confirm("Unlink this device from your Signal account?") {
		fmt.Fprintln(stderr, "warte unlink: canceled")
		return 1
	}
	if err := openRealEnv(cfg); err != nil {
		fmt.Fprintln(stderr, "warte unlink:", err)
		return 1
	}
	ctx, cancel := signalContext(time.Minute)
	err := realUnlink(ctx, cfg)
	cancel()
	code := 0
	switch {
	case err == nil:
		fmt.Fprintln(stdout, "This device is unlinked.")
	case errors.Is(err, core.ErrNotLoggedIn):
		fmt.Fprintln(stdout, "This device is not linked to a Signal account.")
	case errors.Is(err, errUnlinkedLocally):
		fmt.Fprintln(stderr, "warte unlink:", err)
		fmt.Fprintln(stderr, "If this device is still listed on your phone (Settings › Linked devices), remove it there.")
		code = 1
	default:
		fmt.Fprintln(stderr, "warte unlink:", err)
		return 1
	}
	if o.yes || con.confirm("Also delete warte's local data (messages database, attachments and the key in the OS keychain)? This cannot be undone.") {
		dir, err := realWipe(cfg)
		if err != nil {
			fmt.Fprintln(stderr, "warte unlink: delete local data:", err)
			return 1
		}
		fmt.Fprintln(stdout, "Deleted the local data in "+dir+".")
	} else {
		fmt.Fprintln(stdout, `Local data kept. Run "warte link" to link again.`)
	}
	return code
}

// exportFormatNote describes the export file for the user.
const exportFormatNote = `Format: warte export v1 ("WARTEXP1"), AES-256-GCM encrypted, gzip-compressed JSON
with chats, messages, contacts, stories, calls and settings. Attachments are not
included. Only this warte installation (the same key) can read it; it is not a
Signal app backup and cannot be restored on a phone.`

func runExport(o options, cfg config.Config, path string, stdout, stderr io.Writer) int {
	if o.demo {
		fmt.Fprintln(stderr, "warte export: not available for the demo account")
		return 1
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if err := openRealEnv(cfg); err != nil {
		fmt.Fprintln(stderr, "warte export:", err)
		return 1
	}
	ctx, cancel := signalContext(30 * time.Minute)
	defer cancel()
	if err := realExport(ctx, cfg, path); err != nil {
		fmt.Fprintln(stderr, "warte export:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Backup written to "+path+" (readable only by you).")
	fmt.Fprintln(stdout, exportFormatNote)
	return 0
}

// setupLog sends the log to config.LogPath (never to the terminal).
func setupLog(cfg config.Config) func() {
	path, err := config.LogPath()
	if err != nil {
		log.SetOutput(io.Discard)
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.SetOutput(io.Discard)
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.SetOutput(io.Discard)
		return func() {}
	}
	_ = f.Chmod(0o600)
	logger := log.NewWithOptions(logWriter(f, cfg.LogSensitive), log.Options{ReportTimestamp: true, Prefix: "warte"})
	if lvl, err := log.ParseLevel(cfg.LogLevel); err == nil {
		logger.SetLevel(lvl)
	}
	log.SetDefault(logger)
	log.Info("starting", "version", version)
	return func() { _ = f.Close() }
}

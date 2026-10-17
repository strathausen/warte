// Package tui is warte's Bubble Tea v2 frontend: a three-pane, modal,
// keyboard-first Signal client. App is the root tea.Model. It talks to the
// backend only through core.Service and renders exclusively with styles from
// internal/tui/theme.
//
// Feature views (pickers, viewers, settings, ...) live in packages under
// internal/tui/views and plug in through the contract in internal/tui/ui; see
// docs/TUI.md.
package tui

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/notify"
	"warte/internal/termimg"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Focus names the pane that receives navigation keys.
type Focus int

const (
	FocusChatList Focus = iota
	FocusConversation
	FocusComposer
	FocusSidebar
)

func (f Focus) String() string {
	switch f {
	case FocusConversation:
		return "conversation"
	case FocusComposer:
		return "composer"
	case FocusSidebar:
		return "sidebar"
	}
	return "chatlist"
}

// Options tune an App beyond config.Config. The zero value is fine.
type Options struct {
	// Now replaces time.Now (tests).
	Now func() time.Time
	// Getenv replaces os.Getenv for terminal detection (tests).
	Getenv func(string) string
	// Images overrides the image protocol (nil: detect from the environment
	// and config.Images).
	Images *termimg.Protocol
	// Cell is the terminal cell size in pixels, if known.
	Cell termimg.CellSize
	// NerdFonts forces Nerd Font icons on or off (nil: config/auto).
	NerdFonts *bool
	// Warnings are shown as toasts after startup (config warnings).
	Warnings []string
	// SkipStart skips calling Service.Start in Init (the caller started it).
	SkipStart bool
	// Notifier overrides the notification backend (nil: from config).
	Notifier notify.Notifier
	// DarkBackground is the terminal background guess for theme "auto"
	// (nil: assume dark until the terminal reports its background).
	DarkBackground *bool
	// Version is shown in the help overlay.
	Version string
	// ConfigPath is the config.toml that ":w" (and the settings form's
	// save) writes to ("": config.ConfigPath()).
	ConfigPath string
	// LockVerifier checks the screen-lock passphrase (":lock", <leader>l,
	// screen_lock idle timeout). nil: the App's own lock password, an
	// argon2id hash in LockPath that the user sets the first time they lock.
	LockVerifier Verifier
	// LockPath is the lock password file used without a LockVerifier
	// ("": "lock" next to ConfigPath).
	LockPath string
	// TempDir is where $EDITOR drafts and attachments opened externally
	// are written ("": os.TempDir()). Init removes files there that a
	// crashed warte left behind.
	TempDir string
}

// App is the root Bubble Tea model.
type App struct {
	svc  core.Service
	cfg  config.Config
	opts Options
	now  func() time.Time
	ctx  context.Context
	stop context.CancelFunc

	styles *theme.Styles
	icons  theme.IconSet
	keys   *keymap.Engine
	regs   keymap.Registers
	tf     format.TimeFormatter

	width, height int
	focus         Focus
	narrowPane    Focus // which pane is shown in the single-pane layout
	sidebarOpen   bool
	termFocused   bool
	mouse         bool
	layout        layout

	self      core.Contact
	conn      core.ConnState
	sync      *core.SyncProgress
	loggedOut error
	calls     map[core.ChatID]core.IncomingCall

	list    *chatList
	conv    *conversation
	comp    *composer
	cmdline *cmdLine
	search  *searchState
	lock    *screenLock

	overlays       []ui.Overlay
	modeBeforeOver keymap.Mode
	// overlayKeys holds, per open overlay, the keys that opened it; typing
	// them again closes it (K, gr, ?, ...). nextOverlayKeys is recorded by
	// the action and picked up by the next pushOverlay.
	overlayKeys     []string
	nextOverlayKeys string
	toasts          []toast
	toastSeq        int

	images   *imageManager
	zones    zoneMap
	commands map[string]*Command
	cmdNames []string

	coalescer *notify.Coalescer
	notifier  notify.Notifier

	unreadTotal int
	animating   bool
	tick        int
	ctrlCAt     time.Time
	quitting    bool
	frame       string
	frameDirty  bool
	warnedOpen  bool
	pumpOn      bool
	noPump      bool // tests feed events themselves

	tempMu    sync.Mutex
	tempFiles map[string]bool

	composerTop int // screen row of the composer (for the completion popup)
	sidebarOff  int
	sidebarMax  int
	lastEmoji   string

	// jump list (ctrl+o / ctrl+i): messages selected before big jumps.
	jumps   []core.MessageID
	jumpIdx int
	jumping bool // moving through the list: don't record

	uictx ui.Context

	// feature view integration (integration.go)
	sidebarPane     ui.Pane
	sidebarChat     core.ChatID
	pinnedIdx       int
	cfgDirty        map[string]bool
	previewing      bool
	previewBase     theme.Theme
	relink          bool
	storiesUnviewed int
}

var _ tea.Model = (*App)(nil)

// contextT shortens closure signatures.
type contextT = context.Context

// New builds the App. It does not start the backend; Init does (unless
// Options.SkipStart is set).
func New(svc core.Service, cfg config.Config, opts Options) *App {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	ctx, stop := context.WithCancel(context.Background())
	a := &App{
		svc:         svc,
		cfg:         cfg,
		opts:        opts,
		now:         now,
		ctx:         ctx,
		stop:        stop,
		termFocused: true,
		mouse:       cfg.Mouse,
		calls:       map[core.ChatID]core.IncomingCall{},
		frameDirty:  true,
		tempFiles:   map[string]bool{},
	}
	a.tf = format.TimeFormatter{Hour12: cfg.TimeFormat == config.Time12h}

	// Theme.
	dark := true
	if opts.DarkBackground != nil {
		dark = *opts.DarkBackground
	}
	th, _ := theme.Resolve(cfg.Theme, dark)
	if cfg.NoColor {
		th, _ = theme.Get(theme.MonoName)
	}
	a.styles = theme.NewStyles(th)

	// Icons.
	nerd := cfg.NerdFonts.Enabled(detectNerdFont(getenv))
	if opts.NerdFonts != nil {
		nerd = *opts.NerdFonts
	}
	a.icons = theme.Icons(nerd)

	// Keymap.
	a.keys = keymap.New(keymap.WithLeader(cfg.Leader))
	if len(cfg.Keys) > 0 {
		for _, w := range a.keys.ApplyOverrides(keymap.Overrides(cfg.Keys)) {
			opts.Warnings = append(opts.Warnings, "keys: "+w.Error())
		}
	}

	// Notifications.
	a.coalescer = notify.NewCoalescer(notifyLevel(cfg.Notifications))
	a.coalescer.Now = now
	if cfg.Notifications == config.NotifyOff || cfg.NotifyMethod == config.MethodOff {
		a.coalescer.SetEnabled(false)
	}
	a.notifier = opts.Notifier
	if a.notifier == nil {
		m, err := notify.ParseMethod(cfg.NotifyMethod)
		if err != nil {
			m = notify.MethodAuto
		}
		a.notifier = notify.New(m, notify.Options{Getenv: getenv})
	}

	// Images.
	a.images = newImageManager(a, cfg, opts, getenv)

	a.self = svc.Self()
	a.conn = svc.ConnState()
	a.list = newChatList()
	a.conv = newConversation()
	a.comp = newComposer(a)
	a.cmdline = newCmdLine(a)
	a.search = newSearch(a)
	a.lock = newScreenLock()
	a.registerBuiltinCommands()
	for _, c := range ui.Commands() {
		a.registerUICommand(c)
	}
	a.uictx = ui.Context{
		Svc:      svc,
		Styles:   a.styles,
		Icons:    a.icons,
		Theme:    th,
		Config:   cfg,
		Images:   a.images,
		Keys:     a.keys,
		Now:      now,
		Self:     a.self,
		Chat:     func() core.ChatID { return a.conv.chatID() },
		Selected: func() *core.Message { return a.conv.selected() },
	}
	a.opts = opts
	if f := ui.SidebarFactory(); f != nil {
		a.sidebarPane = f(a.uictx)
	}
	a.restyleInputs()
	a.refreshChats()
	if c := a.list.current(); c != nil {
		a.openChat(c.ID, nil, false)
	}
	return a
}

// UIContext returns the context handed to feature views.
func (a *App) UIContext() ui.Context { return a.uictx }

// Styles returns the live style set.
func (a *App) Styles() *theme.Styles { return a.styles }

// Mode returns the current editor mode.
func (a *App) Mode() keymap.Mode { return a.keys.Mode() }

// FocusedPane returns the focused pane.
func (a *App) FocusedPane() Focus { return a.focus }

// detectNerdFont guesses whether the terminal font has Nerd Font glyphs.
// There is no reliable probe; terminals that bundle a Nerd Font symbol
// fallback (kitty, Ghostty, WezTerm) are assumed to have them.
func detectNerdFont(getenv func(string) string) bool {
	if v := getenv("NERD_FONT"); v != "" {
		return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	switch {
	case getenv("KITTY_WINDOW_ID") != "", getenv("GHOSTTY_RESOURCES_DIR") != "",
		getenv("TERM_PROGRAM") == "WezTerm", getenv("TERM_PROGRAM") == "ghostty":
		return true
	}
	return false
}

func notifyLevel(s string) core.NotificationLevel {
	switch s {
	case config.NotifyName:
		return core.NotifyNameOnly
	case config.NotifyNone:
		return core.NotifyNone
	}
	return core.NotifyNameAndMessage
}

// ---- messages ---------------------------------------------------------------

type (
	eventMsg     struct{ ev core.Event }
	eventsClosed struct{}
	startedMsg   struct{ err error }
	flushKeysMsg struct{}
	animTickMsg  struct{}
	toastExpire  struct{ id int }
	errMsg       struct {
		op  string
		err error
	}
	infoMsg struct {
		level ui.ToastLevel
		text  string
	}
	quitNowMsg struct{}
)

// Init starts the backend and the event pump.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{graphemeWidths, a.waitEvent(), tea.RequestBackgroundColor, a.loadStoriesCount(), a.syncSidebar()}
	a.pumpOn = true
	if !a.opts.SkipStart {
		svc, ctx := a.svc, a.ctx
		cmds = append(cmds, func() tea.Msg { return startedMsg{err: svc.Start(ctx)} })
	}
	for _, w := range a.opts.Warnings {
		cmds = append(cmds, ui.Toast(ui.ToastWarning, w))
	}
	cmds = append(cmds, a.idleTimer(), a.lockWarning())
	dir := a.tempDir()
	cmds = append(cmds, func() tea.Msg { sweepStaleTemp(dir); return nil })
	if a.images.kittyInit != "" {
		cmds = append(cmds, tea.Raw(a.images.kittyInit))
	}
	return tea.Batch(cmds...)
}

// graphemeWidths switches Bubble Tea's renderer to grapheme-cluster widths.
// Every view is laid out with lipgloss, which measures graphemes (a ZWJ
// family, a flag, "❤️" are two cells each, as modern terminals and tmux draw
// them), but the renderer measures with wcwidth unless the terminal answers
// the mode 2027 query, and most do not. The two then disagree on any row
// with a ZWJ sequence, skin tone or VS16 emoji: the renderer places the
// cells too far right and the rest of the row, including the pane border, is
// cut off. Reporting mode 2027 as set makes the renderer use the same widths
// as the layout (it also enables 2027 where supported, which is harmless).
func graphemeWidths() tea.Msg {
	return tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet}
}

// waitEvent blocks on the service's event channel and delivers one event.
func (a *App) waitEvent() tea.Cmd {
	if a.noPump {
		return nil
	}
	ch, done := a.svc.Events(), a.ctx.Done()
	return func() tea.Msg {
		// Stop waiting once the App is closed: the channel has a single
		// consumer, and a stale waiter would swallow the next App's event
		// (relink restarts the client over the same service).
		select {
		case ev, ok := <-ch:
			if !ok {
				return eventsClosed{}
			}
			return eventMsg{ev: ev}
		case <-done:
			return nil
		}
	}
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := a.update(msg)
	a.frameDirty = true
	if a.quitting {
		return a, cmd
	}
	a.syncSidebarFocus()
	cmd = tea.Batch(cmd, a.syncSidebar())
	// Render now so image requests discovered while rendering can be
	// returned as commands, and View stays cheap.
	a.frame = a.render()
	a.frameDirty = false
	cmds := []tea.Cmd{cmd}
	cmds = append(cmds, a.images.drain()...)
	if c := a.ensureAnim(); c != nil {
		cmds = append(cmds, c)
	}
	return a, tea.Batch(cmds...)
}

func (a *App) update(msg tea.Msg) tea.Cmd {
	if cmd, ok := a.handleLockMsg(msg); ok {
		return cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.conv.invalidate()
		return nil

	case tea.BackgroundColorMsg:
		if strings.EqualFold(a.cfg.Theme, theme.AutoName) || a.cfg.Theme == "" {
			if !theme.NoColorRequested() && !a.cfg.NoColor {
				a.setTheme(theme.Auto(msg.IsDark()))
			}
		}
		return nil

	case tea.FocusMsg:
		a.termFocused = true
		a.coalescer.SetFocused(!a.isLocked())
		return a.markReadIfVisible()

	case tea.BlurMsg:
		a.termFocused = false
		a.coalescer.SetFocused(false)
		return nil

	case tea.KeyPressMsg:
		a.noteActivity()
		if a.locked() {
			return a.lockKey(msg)
		}
		return a.handleKey(msg)

	case tea.PasteMsg:
		a.noteActivity()
		if a.locked() {
			return a.lockPaste(msg.Content)
		}
		if strings.TrimSpace(msg.Content) == "" && a.keys.Mode() == keymap.Insert {
			// Some terminals send an empty paste when the clipboard only
			// holds an image; look for one.
			return pasteImageIfAny
		}
		return a.handlePaste(msg.Content)

	case tea.MouseMsg:
		a.noteActivity()
		if a.locked() {
			return nil
		}
		return a.handleMouse(msg)

	case flushKeysMsg:
		return a.handleResult(a.keys.Flush(), nil)

	case startedMsg:
		if msg.err != nil {
			return a.toast(ui.ToastError, "start: "+msg.err.Error())
		}
		a.conn = a.svc.ConnState()
		a.refreshChats()
		return nil

	case eventMsg:
		cmd := a.handleEvent(msg.ev)
		ev := ui.EventMsg{Event: msg.ev}
		return tea.Batch(cmd, a.waitEvent(), a.forwardPane(ev), a.forwardOverlay(ev))

	case eventsClosed:
		a.pumpOn = false
		return nil

	case animTickMsg:
		a.animating = false
		a.tick++
		a.list.animate(a)
		a.expireTyping()
		return nil

	case toastExpire:
		a.removeToast(msg.id)
		return nil

	case ui.ToastMsg:
		return a.toast(msg.Level, msg.Text)

	case infoMsg:
		return a.toast(msg.level, msg.text)

	case errMsg:
		if msg.err == nil {
			return nil
		}
		text := msg.err.Error()
		if msg.op != "" {
			text = msg.op + ": " + text
		}
		return a.toast(ui.ToastError, text)

	case notify.ErrMsg:
		return nil // delivery failures are not worth a toast

	case notify.FlushMsg:
		var cmds []tea.Cmd
		for _, n := range a.coalescer.Flush() {
			cmds = append(cmds, notify.Cmd(a.notifier, a.lockedNotification(n)))
		}
		cmds = append(cmds, a.coalescer.Schedule())
		return tea.Batch(cmds...)

	case ui.CloseOverlayMsg:
		return a.popOverlay()

	case ui.PushOverlayMsg:
		return a.pushOverlay(msg.Overlay)

	case ui.OpenChatMsg:
		return a.openChat(msg.Chat, msg.Focus, true)

	case ui.ReplyToMsg:
		return a.startReply(msg.ID)

	case ui.ComposeInsertMsg:
		return a.composeInsert(msg.Text)

	case ui.RunCommandMsg:
		return a.runCommandLine(msg.Line)

	case ui.ReactMsg:
		return a.react(msg.ID, msg.Emoji, msg.Toggle)

	case ui.ConfigChangedMsg:
		return tea.Batch(a.applyConfig(msg.Config, msg.Save), a.forwardOverlay(msg))

	case ui.PreviewThemeMsg:
		if msg.Name != "" && len(a.overlays) == 0 {
			return nil // a late preview from a picker that already closed
		}
		a.previewTheme(msg.Name)
		return nil

	case ui.RelinkMsg:
		a.relink = true
		return a.quit()

	case storiesCountMsg:
		if msg.err == nil {
			a.storiesUnviewed = msg.n
		}
		return nil

	case imageLoadedMsg:
		if a.images.isStale(msg, a.locked()) {
			a.images.loadedStale(msg)
			return nil
		}
		a.images.loaded(msg)
		a.conv.invalidateMedia()
		var cmds []tea.Cmd
		if msg.raw != "" {
			cmds = append(cmds, tea.Raw(msg.raw))
		}
		ready := ui.ImageReadyMsg{Key: msg.key}
		cmds = append(cmds, a.forwardPane(ready), a.forwardOverlay(ready))
		return tea.Batch(cmds...)

	case olderLoadedMsg:
		return a.conv.applyOlder(a, msg)

	case editorDoneMsg:
		return a.comp.editorDone(a, msg)

	case typingCheckMsg:
		return a.comp.typingCheck(a, msg)

	case clipboardImageMsg:
		if a.locked() {
			return nil
		}
		return a.stagePastedImage(msg.data)

	case clipboardPasteMsg:
		if a.locked() {
			return nil
		}
		return a.handlePaste(msg.text)

	case searchResultMsg:
		return a.search.applyResults(a, msg)

	case jumpLoadedMsg:
		return a.conv.applyJump(a, msg)

	case quitNowMsg:
		return a.quit()
	}

	// Anything else (results of overlay and pane commands) goes to the
	// sidebar pane and the top overlay; each ignores what isn't its own.
	return tea.Batch(a.forwardPane(msg), a.forwardOverlay(msg))
}

// View implements tea.Model.
func (a *App) View() tea.View {
	if a.frameDirty || a.frame == "" {
		a.frame = a.render()
		a.frameDirty = false
	}
	v := tea.NewView(a.frame)
	v.AltScreen = true
	v.ReportFocus = true
	if a.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if !a.styles.Theme.Mono {
		v.BackgroundColor = a.styles.Theme.Palette.Bg
		v.ForegroundColor = a.styles.Theme.Palette.Fg
	}
	v.WindowTitle = notify.Title(a.unreadTotal, config.AppName)
	if a.locked() {
		v.WindowTitle = config.AppName // no unread count while locked
	}
	return v
}

// forwardOverlay sends msg to the top overlay, if any.
func (a *App) forwardOverlay(msg tea.Msg) tea.Cmd {
	if len(a.overlays) == 0 {
		return nil
	}
	top := len(a.overlays) - 1
	o, cmd := a.overlays[top].Update(msg)
	if o == nil {
		return tea.Batch(cmd, a.popOverlay())
	}
	a.overlays[top] = o
	return cmd
}

// toast shows a transient message.
func (a *App) toast(level ui.ToastLevel, text string) tea.Cmd {
	text = clean(text)
	if text == "" || a.locked() {
		// Nothing is shown over the lock screen (toasts may name people).
		return nil
	}
	a.toastSeq++
	id := a.toastSeq
	a.toasts = append(a.toasts, toast{id: id, level: level, text: text})
	if len(a.toasts) > 4 {
		a.toasts = a.toasts[len(a.toasts)-4:]
	}
	d := 3 * time.Second
	switch level {
	case ui.ToastError:
		d = 6 * time.Second
	case ui.ToastWarning:
		d = 5 * time.Second
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return toastExpire{id: id} })
}

func (a *App) removeToast(id int) {
	for i, t := range a.toasts {
		if t.id == id {
			a.toasts = append(a.toasts[:i], a.toasts[i+1:]...)
			return
		}
	}
}

// ensureAnim schedules the animation tick while something animates.
func (a *App) ensureAnim() tea.Cmd {
	if a.animating || !a.needsAnim() {
		return nil
	}
	a.animating = true
	d := 160 * time.Millisecond
	if a.list.springActive() && !a.cfg.ReduceMotion {
		d = time.Second / 60
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return animTickMsg{} })
}

// needsAnim reports whether something on screen moves: the chat list
// spring or a typing indicator (which also has to expire).
func (a *App) needsAnim() bool {
	return a.list.springActive() || len(a.list.typing) > 0
}

// setTheme switches the theme live. Styles are updated in place so every
// holder of the pointer (views, overlays) follows.
func (a *App) setTheme(t theme.Theme) {
	*a.styles = *theme.NewStyles(t)
	a.uictx.Theme = t
	a.conv.invalidate()
	a.images.resetPlaceholders()
	a.restyleInputs()
}

// restyleInputs applies the theme to the text inputs.
func (a *App) restyleInputs() {
	a.comp.restyle(a)
	a.cmdline.restyle(a)
	a.search.restyle(a)
	styleInput(&a.lock.input, a.styles)
}

// quit shuts down cleanly: saves the draft, stops typing, deletes temp
// files and clears kitty images.
func (a *App) quit() tea.Cmd {
	a.quitting = true
	dismiss := a.dismissAll()
	ui.Shutdown()
	a.comp.saveDraft(a)
	a.comp.stopTyping(a)
	a.cleanupTemp()
	a.stop()
	cmds := []tea.Cmd{dismiss}
	if s := a.images.shutdown(); s != "" {
		cmds = append(cmds, tea.Raw(s))
	}
	cmds = append(cmds, tea.Quit)
	return tea.Sequence(cmds...)
}

// Close releases resources held by the App (temp files, contexts). The
// caller still owns the Service.
func (a *App) Close() {
	ui.Shutdown()
	a.cleanupTemp()
	a.stop()
}

// trackTemp registers a plaintext temp file for removal on quit/Close.
func (a *App) trackTemp(path string) {
	a.tempMu.Lock()
	a.tempFiles[path] = true
	a.tempMu.Unlock()
}

// removeTemp deletes a tracked temp file now.
func (a *App) removeTemp(path string) {
	_ = os.Remove(path)
	a.tempMu.Lock()
	delete(a.tempFiles, path)
	a.tempMu.Unlock()
}

func (a *App) cleanupTemp() {
	a.tempMu.Lock()
	defer a.tempMu.Unlock()
	for p := range a.tempFiles {
		_ = os.Remove(p)
		delete(a.tempFiles, p)
	}
}

// opCtx returns a context for one backend call.
func (a *App) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, 30*time.Second)
}

// queryCtx returns a short context for local queries.
func (a *App) queryCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, 3*time.Second)
}

// do runs a backend action asynchronously and reports errors as toasts.
func (a *App) do(op string, f func(ctx context.Context) error, okText string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := a.opCtx()
		defer cancel()
		if err := f(ctx); err != nil {
			return errMsg{op: op, err: err}
		}
		if okText != "" {
			return infoMsg{level: ui.ToastSuccess, text: okText}
		}
		return nil
	}
}

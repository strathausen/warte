package forms

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// SettingsChangedMsg is emitted by the settings view whenever a value
// changes (live, while the form is open). Config holds the local settings
// (theme, layout, mouse, images, notifications, reduce motion; other fields
// are passed through unchanged); Settings the account settings, which the
// view has already sent to Service.UpdateSettings. The App should apply
// Config (and may persist it).
type SettingsChangedMsg struct {
	Config   config.Config
	Settings core.Settings
}

// EmitCommands makes the settings view apply theme, layout, mouse and
// reduce-motion changes through the App's ex commands (ui.RunCommandMsg
// "theme …" / "set …") instead of ui.ConfigChangedMsg. The App handles
// ui.ConfigChangedMsg (all local settings, live), so it is off by default.
var EmitCommands = false

// SettingsView is the settings form: appearance (theme, layout, images,
// mouse, reduce motion), notifications (what this device shows: one local
// setting, config.notifications — Signal's notification content setting is
// per device too, so there is no synced level to edit here),
// privacy (read receipts, typing indicators, link previews, sealed sender
// indicators, phone number sharing and discoverability) and the default
// disappearing-message timer. Changes apply immediately.
type SettingsView struct {
	ctx    ui.Context
	form   *formOverlay
	cfg    config.Config
	s      core.Settings
	lastC  config.Config
	lastS  core.Settings
	loaded bool
	err    string

	themeSeq int
	// appliedTheme is the theme last sent to the App (the theme select is
	// debounced, so the config sent for other changes keeps this one).
	appliedTheme string

	// form-bound values
	themeName, layout, images, notif string
	mouse, motion                    bool
	timer                            time.Duration
}

// NewSettings returns the settings overlay. The local values start from
// ctx.Config; the account settings are loaded from the Service.
func NewSettings(ctx ui.Context) *SettingsView {
	return &SettingsView{ctx: ctx, cfg: ctx.Config}
}

type settingsLoadedMsg struct {
	owner *SettingsView
	s     core.Settings
	err   error
}

// themeDebounce is how long the theme cursor must rest before the theme is
// applied through the App's :theme command.
const themeDebounce = 250 * time.Millisecond

type themeApplyMsg struct {
	owner *SettingsView
	seq   int
	name  string
}

type settingsSavedMsg struct {
	owner *SettingsView
	err   error
}

func (v *SettingsView) Init() tea.Cmd {
	svc := v.ctx.Svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		s, err := svc.Settings(ctx)
		return settingsLoadedMsg{owner: v, s: s, err: err}
	}
}

func (v *SettingsView) Title() string      { return "Settings" }
func (v *SettingsView) KeyContext() string { return ContextForm }
func (v *SettingsView) CaptureKeys() bool  { return true }
func (v *SettingsView) Footer() string {
	return v.ctx.Styles.RenderKeyHints("tab", "next", "j/k", "choose", "ctrl+s", "save to config", "esc", "close")
}

// configMsg is the ui.ConfigChangedMsg for the edited local settings, with
// the theme the App currently shows.
func (v *SettingsView) configMsg(save bool) tea.Cmd {
	c, _ := v.current()
	c.Theme = v.appliedTheme
	return func() tea.Msg { return ui.ConfigChangedMsg{Config: c, Save: save} }
}

func (v *SettingsView) Size(maxW, maxH int) (int, int) {
	return min(60, maxW), min(24, maxH)
}

func (v *SettingsView) start() tea.Cmd {
	c := v.cfg
	v.themeName = c.Theme
	if v.themeName == "" {
		v.themeName = theme.AutoName
	}
	v.appliedTheme = v.themeName
	v.layout, v.images, v.notif = c.Layout, c.Images, c.Notifications
	v.mouse, v.motion = c.Mouse, c.ReduceMotion
	v.timer = v.s.DefaultExpireTimer
	v.lastC, v.lastS = v.current()
	st := v.ctx.Styles
	onOff := func(title, desc string, val *bool) *huh.Confirm {
		c := huh.NewConfirm().Title(title).Affirmative("On").Negative("Off").Value(val)
		if desc != "" {
			c.Description(desc)
		}
		return c
	}
	build := func() *huh.Form {
		var themes []huh.Option[string]
		for _, n := range theme.Names() {
			themes = append(themes, huh.NewOption(n, n))
		}
		var timers []huh.Option[time.Duration]
		for _, d := range timerOptions(v.timer) {
			timers = append(timers, huh.NewOption(timerLabel(d), d))
		}
		appearance := huh.NewGroup(
			huh.NewSelect[string]().Title("Theme").Description("Previewed live; / filters").
				Options(themes...).Value(&v.themeName).Height(6),
			huh.NewSelect[string]().Title("Message layout").Inline(true).Options(
				huh.NewOption("Bubbles", config.LayoutBubbles),
				huh.NewOption("IRC lines", config.LayoutIRC)).Value(&v.layout),
			huh.NewSelect[string]().Title("Inline images").Inline(true).Options(
				huh.NewOption("Auto", config.ImagesAuto),
				huh.NewOption("Kitty", config.ImagesKitty),
				huh.NewOption("iTerm2", config.ImagesITerm2),
				huh.NewOption("Sixel", config.ImagesSixel),
				huh.NewOption("Half blocks", config.ImagesBlocks),
				huh.NewOption("Off", config.ImagesOff)).Value(&v.images),
			onOff("Mouse", "Click, scroll and drag; disables terminal text selection", &v.mouse),
			onOff("Reduce motion", "No animations", &v.motion),
		).Title("Appearance")
		localNotify := huh.NewSelect[string]().Title("Notification content").
			Description("What notifications from this device show").Options(
			huh.NewOption("Name and message", config.NotifyNameAndMessage),
			huh.NewOption("Name only", config.NotifyName),
			huh.NewOption("No name or message", config.NotifyNone),
			huh.NewOption("Off", config.NotifyOff)).Value(&v.notif)
		if !v.accountOK() {
			// The account settings could not be loaded: editing them would
			// overwrite the real values with zeros, so only local settings
			// are offered.
			return newHuhForm(st, appearance, huh.NewGroup(localNotify).Title("Notifications"))
		}
		return newHuhForm(st,
			appearance,
			huh.NewGroup(localNotify).Title("Notifications"),
			huh.NewGroup(
				onOff("Read receipts", "Let people see when you've read their messages", &v.s.ReadReceipts),
				onOff("Typing indicators", "", &v.s.TypingIndicators),
				onOff("Link previews", "Generate previews for links you send", &v.s.LinkPreviews),
				onOff("Sealed sender indicators", "Show when messages were sent with sealed sender", &v.s.SealedSenderNotice),
				huh.NewConfirm().Title("Who can see my number").Affirmative("Everybody").Negative("Nobody").Value(&v.s.PhoneNumberSharing),
				huh.NewConfirm().Title("Who can find me by number").Affirmative("Everybody").Negative("Nobody").Value(&v.s.DiscoverableByPhone),
			).Title("Privacy"),
			huh.NewGroup(
				huh.NewSelect[time.Duration]().Title("Default timer for new chats").
					Description("Disappearing messages").Options(timers...).Value(&v.timer).Height(8),
			).Title("Chats"),
		)
	}
	v.form = newFormOverlay(v.ctx, "Settings", 60, build, nil)
	v.form.onChange = v.changed
	return v.form.Init()
}

// current returns the config and settings as edited.
func (v *SettingsView) current() (config.Config, core.Settings) {
	c := v.cfg
	c.Theme, c.Layout, c.Images, c.Notifications = v.themeName, v.layout, v.images, v.notif
	c.Mouse, c.ReduceMotion = v.mouse, v.motion
	s := v.s
	s.DefaultExpireTimer = v.timer
	return c, s
}

// changed emits SettingsChangedMsg (and saves account settings) when a
// value changed since the last call.
func (v *SettingsView) changed() tea.Cmd {
	c, s := v.current()
	cfgChanged := c.Theme != v.lastC.Theme || c.Layout != v.lastC.Layout || c.Images != v.lastC.Images ||
		c.Notifications != v.lastC.Notifications || c.Mouse != v.lastC.Mouse || c.ReduceMotion != v.lastC.ReduceMotion
	sChanged := s != v.lastS && v.accountOK()
	if !cfgChanged && !sChanged {
		return nil
	}
	var cmds []tea.Cmd
	if c.Theme != v.lastC.Theme && !EmitCommands {
		v.themeSeq++
		seq, name := v.themeSeq, c.Theme
		cmds = append(cmds, tea.Tick(themeDebounce, func(time.Time) tea.Msg {
			return themeApplyMsg{owner: v, seq: seq, name: name}
		}))
	}
	if cfgChanged && !EmitCommands {
		cmds = append(cmds, v.configMsg(false))
	}
	if EmitCommands {
		run := func(line string) {
			cmds = append(cmds, func() tea.Msg { return ui.RunCommandMsg{Line: line} })
		}
		if c.Theme != v.lastC.Theme {
			// Debounced: scrolling through the theme list previews the
			// theme the cursor rests on, without a toast per step.
			v.themeSeq++
			seq, name := v.themeSeq, c.Theme
			cmds = append(cmds, tea.Tick(themeDebounce, func(time.Time) tea.Msg {
				return themeApplyMsg{owner: v, seq: seq, name: name}
			}))
		}
		if c.Layout != v.lastC.Layout {
			run("set layout=" + c.Layout)
		}
		if c.Mouse != v.lastC.Mouse {
			run(map[bool]string{true: "set mouse", false: "set nomouse"}[c.Mouse])
		}
		if c.ReduceMotion != v.lastC.ReduceMotion {
			run(map[bool]string{true: "set reducemotion", false: "set noreducemotion"}[c.ReduceMotion])
		}
	}
	if sChanged {
		svc := v.ctx.Svc
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			return settingsSavedMsg{owner: v, err: svc.UpdateSettings(ctx, s)}
		})
	}
	v.lastC, v.lastS = c, s
	msg := SettingsChangedMsg{Config: c, Settings: s}
	cmds = append(cmds, func() tea.Msg { return msg })
	return tea.Batch(cmds...)
}

// accountOK reports whether the account settings were loaded (and may be
// edited and saved).
func (v *SettingsView) accountOK() bool { return v.loaded && v.err == "" }

// Values returns the local config and account settings as currently edited.
func (v *SettingsView) Values() (config.Config, core.Settings) { return v.current() }

func (v *SettingsView) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsLoadedMsg:
		if msg.owner != v {
			return v, nil
		}
		v.loaded = true
		if msg.err != nil {
			v.err = msg.err.Error()
		}
		v.s = msg.s
		return v, v.start()
	case settingsSavedMsg:
		if msg.owner == v && msg.err != nil {
			return v, ui.Toast(ui.ToastError, "Settings: "+clean(msg.err.Error()))
		}
		return v, nil
	case themeApplyMsg:
		if msg.owner == v && msg.seq == v.themeSeq {
			if !EmitCommands {
				v.appliedTheme = msg.name
				return v, v.configMsg(false)
			}
			line := "theme " + msg.name
			return v, func() tea.Msg { return ui.RunCommandMsg{Line: line} }
		}
		return v, nil
	case ui.ConfigChangedMsg:
		return v, nil // our own message, echoed back by the App
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+s" && v.form != nil {
			// Persist the local settings (account settings are saved
			// as they change).
			v.appliedTheme = v.themeName
			return v, v.configMsg(true)
		}
	case SettingsChangedMsg:
		return v, nil // our own broadcast when the App does not consume it
	}
	if v.form == nil {
		if km, ok := msg.(tea.KeyPressMsg); ok {
			if k := keymap.FromTea(km); k == "esc" || k == "ctrl+c" || k == "q" {
				return nil, nil
			}
		}
		return v, nil
	}
	o, cmd := v.form.Update(msg)
	if o == nil {
		return nil, cmd
	}
	return v, cmd
}

func (v *SettingsView) View(w, h int) string {
	st := v.ctx.Styles
	if v.form == nil {
		return muted(st, "Loading…")
	}
	out := v.form.View(w, h)
	if v.err != "" && h > 1 {
		lines := splitN(out, h-1)
		lines = append(lines, statusLine(st, "account settings: "+v.err, true, w))
		return clampLines(lines, w, h)
	}
	return out
}

func splitN(s string, n int) []string {
	lines := splitLines(s)
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

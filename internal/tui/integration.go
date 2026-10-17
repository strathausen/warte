package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// This file holds the glue between the App and the feature view packages
// (internal/tui/views/...): the pluggable sidebar pane, the pinned strip,
// live configuration changes and saving, theme previews, the story ring
// and overlay dismissal.

// ---- sidebar pane --------------------------------------------------------------------

// sidebarVisible reports whether the sidebar is drawn in the current layout.
func (a *App) sidebarVisible() bool {
	return a.sidebarOpen && a.conv.open
}

// syncSidebar tells the sidebar pane which chat to show (only while it is
// open, so a closed sidebar costs nothing).
func (a *App) syncSidebar() tea.Cmd {
	if a.sidebarPane == nil || !a.sidebarVisible() {
		return nil
	}
	id := a.conv.chat.ID
	if id == a.sidebarChat {
		return nil
	}
	a.sidebarChat = id
	return a.sidebarPane.SetChat(id)
}

// syncSidebarFocus focuses or blurs the pane with the App's focus.
func (a *App) syncSidebarFocus() {
	if a.sidebarPane == nil {
		return
	}
	if a.focus == FocusSidebar {
		a.sidebarPane.Focus()
	} else {
		a.sidebarPane.Blur()
	}
}

// forwardPane hands a message to the sidebar pane.
func (a *App) forwardPane(msg tea.Msg) tea.Cmd {
	if a.sidebarPane == nil {
		return nil
	}
	return a.sidebarPane.Update(msg)
}

// sidebarAction routes navigation actions to the focused sidebar pane.
// It reports whether the action was consumed.
func (a *App) sidebarAction(r keymap.Result) (tea.Cmd, bool) {
	if a.sidebarPane == nil || a.focus != FocusSidebar || !a.sidebarVisible() {
		return nil, false
	}
	switch r.Action {
	case keymap.ActCursorDown, keymap.ActCursorUp, keymap.ActCursorLeft, keymap.ActCursorRight,
		keymap.ActCursorTop, keymap.ActCursorBottom, keymap.ActHalfPageDown, keymap.ActHalfPageUp,
		keymap.ActPageDown, keymap.ActPageUp, keymap.ActItemSelect, keymap.ActChatOpen:
		return a.sidebarPane.Update(ui.ActionMsg{Action: r.Action, Count: max(r.Count, 1), Keys: r.Keys}), true
	}
	return nil, false
}

// renderSidebarPane draws the registered sidebar pane in r.
func (a *App) renderSidebarPane(r rect) []string {
	focused := a.focus == FocusSidebar
	iw, ih := max(r.w-2, 0), max(r.h-2, 0)
	var lines []string
	if iw > 0 && ih > 0 {
		lines = splitLines(a.sidebarPane.View(iw, ih))
	}
	return box(a.styles, lines, r.w, r.h, focused, a.paneTitle("Info", focused), "")
}

// ---- pinned messages -----------------------------------------------------------------

// pinnedLoaded returns the pinned messages among the loaded ones, newest
// first.
func (a *App) pinnedLoaded() []core.Message {
	var out []core.Message
	for i := len(a.conv.msgs) - 1; i >= 0; i-- {
		if m := a.conv.msgs[i].m; m.Pinned && !m.RemoteDeleted {
			out = append(out, m)
		}
	}
	return out
}

// renderPinnedStrip returns the pinned strip row (nil when there is none).
func (a *App) renderPinnedStrip(w int) []string {
	f := ui.PinnedStripRenderer()
	if f == nil || !a.conv.open {
		return nil
	}
	pinned := a.pinnedLoaded()
	if len(pinned) == 0 {
		return nil
	}
	a.pinnedIdx = clamp(a.pinnedIdx, 0, len(pinned)-1)
	s := f(a.uictx, a.conv.chat, pinned, a.pinnedIdx, w)
	if s == "" {
		return nil
	}
	return []string{fit(s, w)}
}

// ---- live configuration --------------------------------------------------------------

// applyConfig applies a changed local configuration and remembers which
// keys differ from the file so ":w" can save them.
func (a *App) applyConfig(c config.Config, save bool) tea.Cmd {
	old := a.cfg
	var cmds []tea.Cmd
	if c.Theme != old.Theme && c.Theme != "" {
		if t, ok := a.resolveTheme(c.Theme); ok {
			a.setTheme(t)
			a.cfg.Theme = c.Theme
			a.markDirty("theme")
		}
	}
	if c.Layout != old.Layout && (c.Layout == config.LayoutBubbles || c.Layout == config.LayoutIRC) {
		a.cfg.Layout = c.Layout
		a.conv.invalidate()
		a.markDirty("layout")
	}
	if c.Mouse != a.mouse {
		a.mouse = c.Mouse
		a.cfg.Mouse = c.Mouse
		a.markDirty("mouse")
	}
	if c.ReduceMotion != old.ReduceMotion {
		a.cfg.ReduceMotion = c.ReduceMotion
		a.markDirty("reduce_motion")
	}
	if c.Notifications != old.Notifications && c.Notifications != "" {
		a.cfg.Notifications = c.Notifications
		a.coalescer.SetLevel(notifyLevel(c.Notifications))
		a.coalescer.SetEnabled(c.Notifications != config.NotifyOff && a.cfg.NotifyMethod != config.MethodOff)
		a.markDirty("notifications")
	}
	if c.TimeFormat != old.TimeFormat && c.TimeFormat != "" {
		a.cfg.TimeFormat = c.TimeFormat
		a.tf.Hour12 = c.TimeFormat == config.Time12h
		a.conv.invalidate()
		a.markDirty("time_format")
	}
	if c.Images != old.Images && c.Images != "" {
		a.cfg.Images = c.Images
		a.markDirty("images")
		cmds = append(cmds, a.toast(ui.ToastInfo, "Image protocol changes apply after a restart"))
	}
	a.uictx.Config = a.cfg
	if save {
		cmds = append(cmds, a.saveConfig())
	}
	return tea.Batch(cmds...)
}

func (a *App) resolveTheme(name string) (theme.Theme, bool) {
	if strings.EqualFold(name, theme.AutoName) {
		return theme.Auto(a.styles.Theme.Dark), true
	}
	return theme.Get(strings.ToLower(name))
}

func (a *App) markDirty(key string) {
	if a.cfgDirty == nil {
		a.cfgDirty = map[string]bool{}
	}
	a.cfgDirty[key] = true
}

// previewTheme shows a theme without changing the configuration; "" goes
// back to the configured theme.
func (a *App) previewTheme(name string) {
	if name == "" {
		a.endPreview()
		return
	}
	t, ok := a.resolveTheme(name)
	if !ok {
		return
	}
	if !a.previewing {
		a.previewing = true
		a.previewBase = a.styles.Theme
	}
	if t.Name != a.styles.Theme.Name {
		a.setTheme(t)
	}
}

// endPreview restores the theme that was active before the preview.
func (a *App) endPreview() {
	if !a.previewing {
		return
	}
	a.previewing = false
	if a.previewBase.Name != a.styles.Theme.Name {
		a.setTheme(a.previewBase)
	}
}

// configValue returns the TOML literal of a saveable key.
func (a *App) configValue(key string) (string, bool) {
	c := a.cfg
	switch key {
	case "theme":
		return strconv.Quote(c.Theme), true
	case "layout":
		return strconv.Quote(c.Layout), true
	case "images":
		return strconv.Quote(c.Images), true
	case "notifications":
		return strconv.Quote(c.Notifications), true
	case "time_format":
		return strconv.Quote(c.TimeFormat), true
	case "mouse":
		return strconv.FormatBool(c.Mouse), true
	case "reduce_motion":
		return strconv.FormatBool(c.ReduceMotion), true
	}
	return "", false
}

// saveConfig writes the keys changed in this session to config.toml.
func (a *App) saveConfig() tea.Cmd {
	if len(a.cfgDirty) == 0 {
		return a.toast(ui.ToastInfo, "No settings changed")
	}
	path := a.opts.ConfigPath
	if path == "" {
		p, err := config.ConfigPath()
		if err != nil {
			return a.toast(ui.ToastError, "save config: "+err.Error())
		}
		path = p
	}
	vals := map[string]string{}
	for k := range a.cfgDirty {
		if v, ok := a.configValue(k); ok {
			vals[k] = v
		}
	}
	if err := writeConfigKeys(path, vals); err != nil {
		return a.toast(ui.ToastError, "save config: "+err.Error())
	}
	a.cfgDirty = nil
	return a.toast(ui.ToastSuccess, "Saved "+filepath.Base(path))
}

var tableHeader = regexp.MustCompile(`^\s*\[`)

// writeConfigKeys sets top-level keys in a TOML file, keeping everything
// else (comments, tables, order) untouched: existing "key = …" lines
// before the first table are replaced, missing keys are inserted before
// the first table. The file is created (0600) when missing.
func writeConfigKeys(path string, vals map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	if len(data) == 0 {
		lines = nil
	}
	firstTable := len(lines)
	for i, l := range lines {
		if tableHeader.MatchString(l) {
			firstTable = i
			break
		}
	}
	done := map[string]bool{}
	for i := 0; i < firstTable; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		k = strings.Trim(strings.TrimSpace(k), `"`)
		if v, ok := vals[k]; ok {
			lines[i] = k + " = " + v
			done[k] = true
		}
	}
	var add []string
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !done[k] {
			add = append(add, k+" = "+vals[k])
		}
	}
	if len(add) > 0 {
		if firstTable < len(lines) {
			add = append(add, "")
		}
		lines = append(lines[:firstTable], append(add, lines[firstTable:]...)...)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Validate before replacing the file.
	if _, _, err := config.Parse([]byte(out)); err != nil {
		return fmt.Errorf("refusing to write invalid config: %w", err)
	}
	// Keep a symlinked config (dotfile managers) a symlink: replace the
	// file it points to.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(bytes.TrimLeft([]byte(out), "\n"))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// ---- stories ring --------------------------------------------------------------------

type storiesCountMsg struct {
	n   int
	err error
}

// loadStoriesCount counts the authors with unviewed stories for the ring
// in the chat list title.
func (a *App) loadStoriesCount() tea.Cmd {
	svc, self := a.svc, a.self.ACI
	return func() tea.Msg {
		ctx, cancel := a.queryCtx()
		defer cancel()
		list, err := svc.Stories(ctx)
		if err != nil {
			return storiesCountMsg{err: err}
		}
		authors := map[core.ACI]bool{}
		for _, s := range list {
			if !s.Viewed && s.ID.Author != self {
				authors[s.ID.Author] = true
			}
		}
		return storiesCountMsg{n: len(authors)}
	}
}

// storyRing renders the story indicator for the chat list title.
func (a *App) storyRing() string {
	if _, ok := ui.ActionHandlerFor(keymap.ActStories); !ok {
		return ""
	}
	st := a.styles
	if a.storiesUnviewed == 0 {
		return st.Subtitle.Render(a.icons.Story)
	}
	ring := lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true)
	if st.Theme.Mono {
		ring = lipgloss.NewStyle().Bold(true)
	}
	return ring.Render(a.icons.Story + " " + itoa(a.storiesUnviewed))
}

// ---- overlays ------------------------------------------------------------------------

// dismissOverlay pops the top overlay, letting it clean up first.
func (a *App) dismissOverlay() tea.Cmd {
	if len(a.overlays) == 0 {
		return nil
	}
	var cmd tea.Cmd
	if d, ok := a.overlays[len(a.overlays)-1].(ui.Dismisser); ok {
		cmd = d.Dismiss()
	}
	return tea.Batch(cmd, a.popOverlay())
}

// dismissAll closes every overlay (logout, quit).
func (a *App) dismissAll() tea.Cmd {
	var cmds []tea.Cmd
	for len(a.overlays) > 0 {
		cmds = append(cmds, a.dismissOverlay())
	}
	return tea.Batch(cmds...)
}

// RelinkRequested reports whether the user asked to link the device again
// from the logged-out screen (the App quit for that).
func (a *App) RelinkRequested() bool { return a.relink }

// SidebarOpen reports whether the sidebar is shown.
func (a *App) SidebarOpen() bool { return a.sidebarOpen }

// Overlays returns the number of open overlays.
func (a *App) Overlays() int { return len(a.overlays) }

// TopOverlay returns the top overlay, or nil.
func (a *App) TopOverlay() ui.Overlay {
	if len(a.overlays) == 0 {
		return nil
	}
	return a.overlays[len(a.overlays)-1]
}

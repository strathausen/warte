package tui

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

type zoneKind int

const (
	zoneChat zoneKind = iota + 1
	zoneMessage
	zoneAttachment
	zoneReaction
	zoneLink
	zoneComposer
	zoneOverlay
	zoneStories
	zonePinned
	zoneFolder
)

// zone is a clickable screen region recorded while rendering.
type zone struct {
	x, y, w, h int
	kind       zoneKind
	chat       core.ChatID
	index      int // chat or message index
	att        int // attachment index (-1 = sticker)
	emoji      string
	url        string
}

func (z zone) contains(x, y int) bool {
	return x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h
}

// zoneMap holds the zones of the last frame. Later zones win (they are
// drawn on top or are finer-grained).
type zoneMap struct {
	zones   []zone
	overlay *overlayRect
}

func (m *zoneMap) reset() {
	m.zones = m.zones[:0]
	m.overlay = nil
}

func (m *zoneMap) add(z zone) {
	if z.w <= 0 || z.h <= 0 {
		return
	}
	m.zones = append(m.zones, z)
}

// at returns the topmost zone at (x, y).
func (m *zoneMap) at(x, y int) (zone, bool) {
	for i := len(m.zones) - 1; i >= 0; i-- {
		if m.zones[i].contains(x, y) {
			return m.zones[i], true
		}
	}
	return zone{}, false
}

// msgZoneAt finds the message zone (index) under (x, y).
func (m *zoneMap) msgZoneAt(x, y int) (zone, bool) {
	for i := len(m.zones) - 1; i >= 0; i-- {
		if m.zones[i].kind == zoneMessage && m.zones[i].contains(x, y) {
			return m.zones[i], true
		}
	}
	return zone{}, false
}

// handleMouse dispatches mouse events using the zones of the last frame.
func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if !a.mouse {
		return nil
	}
	m := msg.Mouse()
	l := a.layout
	a.nextOverlayKeys = ""
	inRect := func(r rect) bool { return r.w > 0 && m.X >= r.x && m.X < r.x+r.w && m.Y >= r.y && m.Y < r.y+r.h }

	// Overlays get events inside their content area; clicks outside close.
	if len(a.overlays) > 0 {
		if _, ok := msg.(tea.MouseClickMsg); ok {
			if o := a.zones.overlay; o != nil && !(m.X >= o.x && m.X < o.x+o.w && m.Y >= o.y && m.Y < o.y+o.h) {
				return a.dismissOverlay()
			}
		}
		for i := len(a.zones.zones) - 1; i >= 0; i-- {
			z := a.zones.zones[i]
			if z.kind == zoneOverlay && z.contains(m.X, m.Y) {
				rel := tea.Mouse{X: m.X - z.x, Y: m.Y - z.y, Button: m.Button, Mod: m.Mod}
				switch msg.(type) {
				case tea.MouseClickMsg:
					return a.forwardOverlay(tea.MouseClickMsg(rel))
				case tea.MouseWheelMsg:
					return a.forwardOverlay(tea.MouseWheelMsg(rel))
				case tea.MouseReleaseMsg:
					return a.forwardOverlay(tea.MouseReleaseMsg(rel))
				}
			}
		}
		return nil
	}

	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		delta := 3
		if msg.Button == tea.MouseWheelUp {
			delta = -3
		} else if msg.Button != tea.MouseWheelDown {
			return nil
		}
		switch {
		case inRect(l.list):
			a.listMove(delta)
		case inRect(l.conv):
			cmd := a.scrollLines(-delta)
			a.selIntoView()
			return cmd
		case inRect(l.side):
			if a.sidebarPane != nil {
				// The registered pane scrolls by moving its cursor.
				act := keymap.ActCursorDown
				if delta < 0 {
					act = keymap.ActCursorUp
				}
				return a.sidebarPane.Update(ui.ActionMsg{Action: act, Count: 3, Keys: "wheel"})
			}
			a.sidebarScroll(delta)
		}
		return nil

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil
		}
		// A click ends the command line, search line and visual selection
		// (like vim), so keys don't keep going to a line the user left.
		switch a.keys.Mode() {
		case keymap.Command:
			a.closeCmdLine()
		case keymap.Search:
			_ = a.cancelSearch()
		case keymap.Visual, keymap.VisualLine:
			a.exitVisual()
		}
		z, ok := a.zones.at(m.X, m.Y)
		if !ok {
			switch {
			case inRect(l.list):
				a.setFocus(FocusChatList)
			case inRect(l.conv):
				a.setFocus(FocusConversation)
			case inRect(l.side):
				a.setFocus(FocusSidebar)
			}
			return nil
		}
		if a.keys.Mode() == keymap.Insert && z.kind != zoneComposer {
			_ = a.leaveInsert()
		}
		switch z.kind {
		case zoneChat:
			a.setFocus(FocusChatList)
			if z.index == a.list.sel && a.conv.open && a.conv.chat.ID == z.chat {
				a.setFocus(FocusConversation)
				a.enterComposerIfWritable()
				return nil
			}
			a.listSelect(z.index)
			cmd := a.openChat(z.chat, nil, false)
			if l.narrow {
				a.setFocus(FocusConversation)
				a.enterComposerIfWritable()
			}
			return cmd
		case zoneMessage:
			a.setFocus(FocusConversation)
			a.conv.sel = clamp(z.index, 0, max(a.conv.last(), 0))
			return nil
		case zoneAttachment:
			mz, ok := a.zones.msgZoneAt(m.X, m.Y)
			if !ok || mz.index >= len(a.conv.msgs) {
				return nil
			}
			a.setFocus(FocusConversation)
			a.conv.sel = mz.index
			mm := a.conv.msgs[mz.index].m
			if mm.ViewOnce || z.att == 0 {
				return a.messageOpen()
			}
			if z.att > 0 && z.att < len(mm.Attachments) {
				att := mm.Attachments[z.att]
				if att.IsImage() {
					return a.pushOverlay(newImageOverlay(a, mm, att, false))
				}
				return a.openAttachment(mm, att)
			}
			return nil
		case zoneReaction:
			mz, ok := a.zones.msgZoneAt(m.X, m.Y)
			if !ok || mz.index >= len(a.conv.msgs) {
				return nil
			}
			a.conv.sel = mz.index
			return a.react(a.conv.msgs[mz.index].m.ID, z.emoji, true)
		case zoneLink:
			if mz, ok := a.zones.msgZoneAt(m.X, m.Y); ok {
				a.conv.sel = clamp(mz.index, 0, max(a.conv.last(), 0))
			}
			return a.openURL(z.url)
		case zoneComposer:
			if a.conv.open {
				a.focusComposer()
			}
			return nil
		case zoneStories:
			return a.dispatchRegistered(keymap.ActStories)
		case zonePinned:
			return a.dispatchRegistered(keymap.ActChatPinned)
		case zoneFolder:
			a.setFocus(FocusChatList)
			a.switchTab(z.index - a.list.tab)
			return nil
		}
	}
	return nil
}

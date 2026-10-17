package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// focusContext maps the focused pane to its keymap context.
func (a *App) focusContext() keymap.Context {
	switch a.focus {
	case FocusConversation, FocusComposer:
		return keymap.Conversation
	case FocusSidebar:
		return keymap.Sidebar
	}
	return keymap.ChatList
}

// setFocus moves keyboard focus (and the visible pane in the narrow layout).
func (a *App) setFocus(f Focus) {
	if f == FocusSidebar && !a.sidebarOpen {
		a.sidebarOpen = true
	}
	if (f == FocusConversation || f == FocusComposer) && !a.conv.open {
		f = FocusChatList
	}
	a.focus = f
	switch f {
	case FocusComposer:
		a.narrowPane = FocusConversation
	default:
		a.narrowPane = f
	}
	if f != FocusComposer && a.keys.Mode() == keymap.Insert {
		a.keys.SetMode(keymap.Normal)
	}
}

// focusComposer enters Insert mode in the composer.
func (a *App) focusComposer() {
	if !a.conv.open {
		return
	}
	a.setFocus(FocusComposer)
	a.keys.SetMode(keymap.Insert)
	a.comp.ta.Focus()
}

func (a *App) leaveInsert() tea.Cmd {
	a.keys.SetMode(keymap.Normal)
	a.comp.comp = completion{}
	a.comp.ta.Blur()
	if a.focus == FocusComposer {
		a.focus = FocusConversation
	}
	if a.comp.editing != nil {
		a.comp.reset()
		a.comp.loadDraftText(a)
	}
	a.comp.saveDraft(a)
	return a.comp.stopTyping(a)
}

// loadDraftText restores the saved draft text after cancelling an edit.
func (c *composer) loadDraftText(a *App) {
	if c.draftSaved != "" {
		c.ta.SetValue(c.draftSaved)
	}
}

// handleKey is the entry point for key presses.
func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if a.loggedOut != nil && len(a.overlays) == 0 {
		switch msg.String() {
		case "q", "ctrl+c", "enter", "esc":
			return a.quit()
		}
		return nil
	}
	if len(a.overlays) > 0 {
		return a.overlayKey(msg)
	}
	key := keymap.FromTea(msg)
	mode := a.keys.Mode()

	// The completion popup captures navigation keys in Insert mode.
	if mode == keymap.Insert && a.comp.comp.kind != compNone {
		c := &a.comp.comp
		switch msg.String() {
		case "tab", "enter", "ctrl+y":
			return a.comp.acceptCompletion(a)
		case "down", "ctrl+n":
			c.sel = (c.sel + 1) % len(c.items)
			return nil
		case "up", "ctrl+p", "shift+tab":
			c.sel = (c.sel - 1 + len(c.items)) % len(c.items)
			return nil
		case "esc":
			a.comp.comp = completion{}
			return nil
		}
	}

	ctx := a.focusContext()
	switch mode {
	case keymap.Insert, keymap.Command, keymap.Search:
		ctx = keymap.Global
	}
	return a.handleResult(a.keys.Feed(key, ctx), &msg)
}

// handleResult acts on a keymap result chain. msg is the key that produced
// it (nil after a timeout flush).
func (a *App) handleResult(res keymap.Result, msg *tea.KeyPressMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range res.All() {
		switch {
		case r.Expires:
			cmds = append(cmds, tea.Tick(a.keys.Timeout(), func(time.Time) tea.Msg { return flushKeysMsg{} }))
		case r.Pending, r.Canceled:
			// showcmd displays the pending keys.
		case (r.Unmatched || r.Matched()) && a.isOverlayToggle(r):
			cmds = append(cmds, a.dismissOverlay())
		case r.Unmatched:
			cmds = append(cmds, a.unmatched(r, msg))
		case r.Matched():
			if len(a.overlays) > 0 {
				cmds = append(cmds, a.overlayAction(r, msg))
			} else {
				cmds = append(cmds, a.action(r))
			}
		}
	}
	return tea.Batch(cmds...)
}

// unmatched handles keys that are not bound: text input in the text modes.
func (a *App) unmatched(r keymap.Result, msg *tea.KeyPressMsg) tea.Cmd {
	if len(a.overlays) > 0 {
		if msg != nil {
			return a.forwardOverlay(*msg)
		}
		return nil
	}
	mode := a.keys.Mode()
	if mode != keymap.Insert && mode != keymap.Command && mode != keymap.Search {
		return nil
	}
	// A single key: forward the real key press so modifiers work.
	if msg != nil && len(r.Seq) == 1 {
		switch mode {
		case keymap.Insert:
			return a.composerKey(*msg)
		case keymap.Command:
			return a.cmdLineKey(*msg)
		case keymap.Search:
			return a.searchKey(*msg)
		}
	}
	// A flushed partial sequence: insert its printable keys.
	text := ""
	for _, k := range r.Seq {
		switch {
		case k == "space":
			text += " "
		case len([]rune(k)) == 1:
			text += k
		}
	}
	if text == "" {
		return nil
	}
	switch mode {
	case keymap.Insert:
		return a.comp.insert(a, text)
	case keymap.Command:
		a.cmdline.input.SetValue(a.cmdline.input.Value() + text)
		a.cmdline.input.CursorEnd()
	case keymap.Search:
		a.search.input.SetValue(a.search.input.Value() + text)
		a.search.input.CursorEnd()
		return a.search.live(a)
	}
	return nil
}

// action performs a fired binding when no overlay is open.
func (a *App) action(r keymap.Result) tea.Cmd {
	n := max(r.Count, 1)
	a.nextOverlayKeys = ""
	if toggleOpeners[r.Action] && !r.Repeat {
		a.nextOverlayKeys = r.Keys
	}
	if cmd, ok := a.sidebarAction(r); ok {
		return cmd
	}
	switch r.Action {
	// ---- navigation
	case keymap.ActCursorDown:
		return a.moveCursor(n)
	case keymap.ActCursorUp:
		return a.moveCursor(-n)
	case keymap.ActMessageDownOrCompose:
		// ↓ on the newest message goes back to writing (the reverse of ↑
		// on the composer's first line).
		if a.focus == FocusConversation && !r.HasCount && a.conv.sel >= a.conv.last() {
			a.enterComposerIfWritable()
			return nil
		}
		return a.moveCursor(n)
	case keymap.ActCursorLeft:
		if a.focus == FocusConversation && !a.keys.Mode().IsVisual() {
			a.setFocus(FocusChatList)
		}
		return nil
	case keymap.ActCursorRight:
		if a.focus == FocusChatList {
			return a.openSelected(false)
		}
		return nil
	case keymap.ActCursorTop:
		if a.inList() {
			if r.HasCount {
				a.listSelect(n - 1)
			} else {
				a.listSelect(0)
			}
			return nil
		}
		if r.HasCount {
			a.conv.sel = clamp(n-1, 0, max(a.conv.last(), 0))
			a.ensureSelVisible()
			return nil
		}
		return a.convTop()
	case keymap.ActCursorBottom:
		if a.inList() {
			if r.HasCount {
				a.listSelect(n - 1)
			} else {
				a.listSelect(len(a.list.chats) - 1)
			}
			return nil
		}
		if r.HasCount {
			a.conv.sel = clamp(n-1, 0, max(a.conv.last(), 0))
			a.ensureSelVisible()
			return nil
		}
		a.recordJump()
		a.convBottom()
		return a.markReadIfVisible()
	case keymap.ActHalfPageDown, keymap.ActPageDown, keymap.ActHalfPageUp, keymap.ActPageUp:
		frac := 0.5
		if r.Action == keymap.ActPageDown || r.Action == keymap.ActPageUp {
			frac = 1
		}
		dir := 1
		if r.Action == keymap.ActHalfPageUp || r.Action == keymap.ActPageUp {
			dir = -1
		}
		if a.inList() {
			a.listMove(dir * max(int(float64(a.list.height)*frac), 1))
			return nil
		}
		return a.convPage(frac, dir)
	case keymap.ActCenter:
		if !a.inList() {
			a.centerSel()
		}
		return nil
	case keymap.ActJumpBack:
		return a.jumpHistory(-n)
	case keymap.ActJumpForward:
		return a.jumpHistory(n)
	case keymap.ActSearchNext:
		return a.jumpToHit(1, false)
	case keymap.ActSearchPrev:
		return a.jumpToHit(-1, false)
	case keymap.ActChatNextUnread:
		return a.listNextUnread(1)
	case keymap.ActChatPrevUnread:
		return a.listNextUnread(-1)
	case keymap.ActFolderNext:
		a.switchTab(n)
		return nil
	case keymap.ActFolderPrev:
		a.switchTab(-n)
		return nil

	// ---- panes
	case keymap.ActFocusLeft:
		return a.focusStep(-1, false)
	case keymap.ActFocusRight:
		return a.focusStep(1, false)
	case keymap.ActFocusNext:
		return a.focusStep(1, true)
	case keymap.ActFocusPrev:
		return a.focusStep(-1, true)
	case keymap.ActSidebarToggle:
		a.sidebarOpen = !a.sidebarOpen
		if !a.sidebarOpen && a.focus == FocusSidebar {
			a.setFocus(FocusConversation)
		}
		if a.sidebarOpen && (a.layout.narrow || a.sidebarPane != nil) {
			a.setFocus(FocusSidebar)
		}
		return nil

	// ---- chats
	case keymap.ActChatOpen:
		return a.openSelected(true)
	case keymap.ActChatArchive:
		return a.toggleArchive()
	case keymap.ActChatMute:
		return a.toggleMute(0, false)
	case keymap.ActChatPin:
		return a.togglePin()
	case keymap.ActChatMarkUnread:
		return a.toggleMarkUnread()
	case keymap.ActChatFilter:
		return a.startSearch(scopeList)
	case keymap.ActChatDelete:
		return a.confirmDeleteChat()
	case keymap.ActChatShowArchived:
		a.list.archived = !a.list.archived
		a.list.sel, a.list.offset = 0, 0
		a.refreshChats()
		return nil
	case keymap.ActChatNew:
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		return a.openCmdLine("new ")

	// ---- messages
	case keymap.ActMessageOpen:
		return a.messageOpen()
	case keymap.ActMessageReply:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.startReply(m.ID)
	case keymap.ActMessageReact:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		if r.Repeat {
			if lr, ok := a.lastReaction(); ok {
				return a.react(m.ID, lr, true)
			}
		}
		return a.openReactionBar(m)
	case keymap.ActMessageEdit:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.startEdit(m)
	case keymap.ActMessageDelete, keymap.ActMessageDeleteEveryone:
		msgs := a.messagesForCount(n)
		if len(msgs) == 0 {
			return nil
		}
		return a.confirmDelete(msgs, r.Action == keymap.ActMessageDeleteEveryone)
	case keymap.ActMessageCopy:
		return a.copyMessages(a.messagesForCount(n), r.Register)
	case keymap.ActMessageCopyLink:
		return a.copyLink(r.Register)
	case keymap.ActMessageForward:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.forward([]core.Message{*m})
	case keymap.ActMessageOpenAttachment:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.openFirstAttachment(*m)
	case keymap.ActMessageOpenURL:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.openURLOf(*m)
	case keymap.ActMessagePin:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.setMessagePinned(m, !m.Pinned)
	case keymap.ActMessageGotoQuote:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		if m.Quote == nil {
			return a.toast(ui.ToastInfo, "Not a reply")
		}
		if !m.Quote.OriginalFound {
			return a.toast(ui.ToastWarning, "Original message not found")
		}
		a.recordJump()
		return a.jumpTo(m.Quote.ID)
	case keymap.ActMessageInfo:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		return a.pushOverlay(newInfoOverlay(a, *m))
	case keymap.ActMessageRevealSpoiler:
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		a.conv.revealed[m.ID] = !a.conv.revealed[m.ID]
		return nil

	// ---- modes
	case keymap.ActModeInsert, keymap.ActModeAppend, keymap.ActModeInsertStart:
		if a.focus == FocusChatList {
			if c := a.list.current(); c != nil && (!a.conv.open || a.conv.chat.ID != c.ID) {
				cmd := a.openChat(c.ID, nil, true)
				a.focusComposer()
				return cmd
			}
		}
		if !a.conv.open {
			return a.toast(ui.ToastInfo, "Open a chat first")
		}
		a.focusComposer()
		switch r.Action {
		case keymap.ActModeInsertStart:
			a.comp.ta.MoveToBegin()
		case keymap.ActModeAppend:
			a.comp.ta.MoveToEnd()
		}
		return nil
	case keymap.ActModeVisual:
		if a.keys.Mode() == keymap.Normal && a.focus != FocusChatList {
			a.enterVisual(false)
		} else if a.keys.Mode().IsVisual() {
			a.keys.SetMode(keymap.Visual)
		}
		return nil
	case keymap.ActModeVisualLine:
		if a.focus != FocusChatList {
			a.enterVisual(true)
		}
		return nil
	case keymap.ActModeNormal:
		switch a.keys.Mode() {
		case keymap.Insert:
			return a.leaveInsert()
		case keymap.Visual, keymap.VisualLine:
			a.exitVisual()
		}
		return nil
	case keymap.ActModeCommand:
		return a.openCmdLine("")
	case keymap.ActVisualYank:
		msgs := a.visualMessages()
		a.exitVisual()
		return a.copyMessages(msgs, r.Register)
	case keymap.ActVisualDelete:
		msgs := a.visualMessages()
		a.exitVisual()
		return a.confirmDelete(msgs, false)
	case keymap.ActVisualForward:
		msgs := a.visualMessages()
		a.exitVisual()
		return a.forward(msgs)

	// ---- composer
	case keymap.ActComposerSend:
		if !a.cfg.SendOnEnter {
			return a.comp.insert(a, "\n")
		}
		return a.send()
	case keymap.ActComposerNewline:
		if !a.cfg.SendOnEnter {
			return a.send()
		}
		return a.comp.insert(a, "\n")
	case keymap.ActComposerEditor:
		return a.openEditor()
	case keymap.ActComposerAttach:
		return a.openCmdLine("attach ")
	case keymap.ActComposerPaste:
		return clipboardPaste
	case keymap.ActComposerCancelReply:
		if a.comp.replyTo != nil || a.comp.editing != nil {
			a.comp.replyTo = nil
			if a.comp.editing != nil {
				a.comp.editing = nil
				a.comp.ta.SetValue(a.comp.draftSaved)
			}
		}
		return nil
	case keymap.ActComposerComplete, keymap.ActComposerCompletePrev:
		a.comp.updateCompletion(a)
		if a.comp.comp.kind == compNone && r.Action == keymap.ActComposerComplete {
			return a.comp.insert(a, "\t")
		}
		return nil
	case keymap.ActComposerCancel:
		return a.leaveInsert()
	case keymap.ActComposerLeft:
		// At the very start of the text, ← leaves the composer for the
		// chat list; anywhere else it moves the cursor.
		if a.comp.comp.kind == compNone && a.comp.ta.Line() == 0 && a.comp.ta.Column() == 0 {
			cmd := a.leaveInsert()
			if i := a.list.indexOf(a.conv.chat.ID); i >= 0 {
				a.list.sel = i
			}
			a.setFocus(FocusChatList)
			return cmd
		}
		return a.composerKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	case keymap.ActComposerUp:
		// On the first line, ↑ leaves the composer and selects the newest
		// message (Normal mode); elsewhere it moves the cursor.
		if a.comp.comp.kind == compNone && a.comp.ta.Line() == 0 && a.conv.last() >= 0 {
			cmd := a.leaveInsert()
			a.setFocus(FocusConversation)
			a.convBottom()
			return tea.Batch(cmd, a.markReadIfVisible())
		}
		return a.composerKey(tea.KeyPressMsg{Code: tea.KeyUp})

	// ---- command line
	case keymap.ActCmdExecute:
		return a.cmdExecute()
	case keymap.ActCmdCancel:
		a.closeCmdLine()
		return nil
	case keymap.ActCmdComplete:
		a.cmdComplete(1)
		return nil
	case keymap.ActCmdCompletePrev:
		a.cmdComplete(-1)
		return nil
	case keymap.ActCmdHistoryPrev:
		a.cmdHistory(-1)
		return nil
	case keymap.ActCmdHistoryNext:
		a.cmdHistory(1)
		return nil

	// ---- search
	case keymap.ActSearchForward:
		if a.focus == FocusChatList {
			return a.startSearch(scopeList)
		}
		return a.startSearch(scopeConv)
	case keymap.ActSearchSubmit:
		return a.submitSearch()
	case keymap.ActSearchCancel:
		return a.cancelSearch()
	case keymap.ActSearchHistoryPrev:
		a.searchHistory(-1)
		return a.search.live(a)
	case keymap.ActSearchHistoryNext:
		a.searchHistory(1)
		return a.search.live(a)

	// ---- general
	case keymap.ActHelp:
		return a.pushOverlay(newHelpOverlay(a))
	case keymap.ActRedraw:
		a.conv.invalidate()
		return tea.ClearScreen
	case keymap.ActBack:
		return a.back()
	case keymap.ActCancel:
		return a.cancel()
	case keymap.ActQuit:
		return a.quit()
	case keymap.ActQuitHint:
		now := a.now()
		if !a.ctrlCAt.IsZero() && now.Sub(a.ctrlCAt) < 1500*time.Millisecond {
			return a.quit()
		}
		a.ctrlCAt = now
		return a.toast(ui.ToastInfo, "Type :q or ZZ to quit (or ctrl+c again)")
	case keymap.ActRepeat:
		return nil
	case keymap.ActFinderChats:
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		return a.pushOverlay(newChatPicker(a, "Find chat", func(id core.ChatID) tea.Cmd { return a.openChat(id, nil, true) }))
	case keymap.ActThemePicker:
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		return a.openCmdLine("theme ")
	case keymap.ActChatInfo, keymap.ActMediaGallery:
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		a.sidebarOpen = true
		a.setFocus(FocusSidebar)
		return nil
	case keymap.ActItemSelect:
		return nil
	case keymap.ActLock:
		if cmd := a.dispatchRegistered(r.Action); cmd != nil {
			return cmd
		}
		return a.lockScreen()
	}
	if cmd := a.dispatchRegistered(r.Action); cmd != nil {
		return cmd
	}
	return a.toast(ui.ToastInfo, r.Action.Help()+": not available yet")
}

// dispatchRegistered runs a handler registered by a view package.
func (a *App) dispatchRegistered(act keymap.Action) tea.Cmd {
	if h, ok := ui.ActionHandlerFor(act); ok {
		if cmd := h(a.uictx); cmd != nil {
			return cmd
		}
		return func() tea.Msg { return nil }
	}
	return nil
}

func (a *App) inList() bool { return a.focus == FocusChatList }

func (a *App) moveCursor(n int) tea.Cmd {
	switch a.focus {
	case FocusChatList:
		a.listMove(n)
		return nil
	case FocusSidebar:
		a.sidebarScroll(n)
		return nil
	}
	return a.convMove(n)
}

// openSelected opens the chat selected in the list.
func (a *App) openSelected(focus bool) tea.Cmd {
	c := a.list.current()
	if c == nil {
		return nil
	}
	return a.openChat(c.ID, nil, true)
}

// focusStep moves focus between the visible panes. cycle wraps around.
func (a *App) focusStep(dir int, cycle bool) tea.Cmd {
	panes := []Focus{FocusChatList}
	if a.conv.open {
		panes = append(panes, FocusConversation)
	}
	if a.sidebarOpen && a.conv.open {
		panes = append(panes, FocusSidebar)
	}
	cur := a.focus
	if cur == FocusComposer {
		cur = FocusConversation
	}
	idx := 0
	for i, p := range panes {
		if p == cur {
			idx = i
		}
	}
	idx += dir
	if cycle {
		idx = (idx + len(panes)) % len(panes)
	} else {
		idx = clamp(idx, 0, len(panes)-1)
	}
	a.setFocus(panes[idx])
	return a.markReadIfVisible()
}

// back implements "q" in Normal mode.
func (a *App) back() tea.Cmd {
	switch {
	case a.search.highlight != "":
		a.search.clearHits()
		a.conv.invalidate()
	case a.list.filter != "" && a.focus == FocusChatList:
		a.list.filter = ""
		a.refreshChats()
	case a.list.archived && a.focus == FocusChatList:
		a.list.archived = false
		a.refreshChats()
	case a.focus == FocusSidebar:
		a.setFocus(FocusConversation)
	case a.focus != FocusChatList:
		a.setFocus(FocusChatList)
	}
	return nil
}

// cancel implements Esc in Normal mode.
func (a *App) cancel() tea.Cmd {
	switch {
	case a.search.highlight != "":
		a.search.clearHits()
		a.conv.invalidate()
	case a.list.filter != "":
		a.list.filter = ""
		a.refreshChats()
	case a.comp.replyTo != nil:
		a.comp.replyTo = nil
	case a.layout.narrow && a.focus != FocusChatList:
		a.setFocus(FocusChatList)
	}
	return nil
}

// messagesForCount returns the selected message and the count-1 following.
func (a *App) messagesForCount(n int) []core.Message {
	c := a.conv
	if c.selected() == nil {
		return nil
	}
	var out []core.Message
	for i := c.sel; i < len(c.msgs) && len(out) < n; i++ {
		out = append(out, c.msgs[i].m)
	}
	return out
}

// lastReaction is the emoji "." re-applies after a reaction.
func (a *App) lastReaction() (string, bool) { return a.lastEmoji, a.lastEmoji != "" }

// setMessagePinned pins or unpins a message.
func (a *App) setMessagePinned(m *core.Message, pin bool) tea.Cmd {
	id := m.ID
	ok := "Pinned message"
	if !pin {
		ok = "Unpinned message"
	}
	return a.do("pin", func(ctx contextT) error { return a.svc.PinMessage(ctx, id, pin, 0) }, ok)
}

// pinSelectedMessage backs :pin / :unpin while the conversation has focus:
// they act on the selected message there. ok is false when the command
// should act on the chat instead (chat list focus, nothing selected).
func (a *App) pinSelectedMessage(pin bool) (tea.Cmd, bool) {
	if a.focus == FocusChatList || !a.conv.open || len(a.conv.msgs) == 0 {
		return nil, false
	}
	i := clamp(a.conv.sel, 0, a.conv.last())
	m := &a.conv.msgs[i].m
	if m.Pinned == pin {
		if pin {
			return a.toast(ui.ToastInfo, "Message is already pinned"), true
		}
		return a.toast(ui.ToastInfo, "Message isn't pinned"), true
	}
	return a.setMessagePinned(m, pin), true
}

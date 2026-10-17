package keymap

import "sort"

// Action names a command the TUI performs in response to keys. The TUI
// switches on these values; config files refer to them by their string
// value ("chat.mute").
type Action string

// Group is a heading in the help overlay.
type Group string

// Help groups, in display order (see Groups).
const (
	GroupNavigation Group = "Navigation"
	GroupPanes      Group = "Panes"
	GroupChats      Group = "Chats"
	GroupMessages   Group = "Messages"
	GroupSelection  Group = "Selection"
	GroupComposer   Group = "Composer"
	GroupSearch     Group = "Search"
	GroupCommand    Group = "Command line"
	GroupViewer     Group = "Viewer"
	GroupGeneral    Group = "General"
)

// Groups lists the help groups in display order.
var Groups = []Group{
	GroupNavigation, GroupPanes, GroupChats, GroupMessages, GroupSelection,
	GroupComposer, GroupSearch, GroupCommand, GroupViewer, GroupGeneral,
}

// Navigation actions. A count moves that many items; with gg/G a count
// jumps to that item.
const (
	ActCursorDown     Action = "cursor.down"
	ActCursorUp       Action = "cursor.up"
	ActCursorLeft     Action = "cursor.left"
	ActCursorRight    Action = "cursor.right"
	ActCursorTop      Action = "cursor.top"
	ActCursorBottom   Action = "cursor.bottom"
	ActHalfPageDown   Action = "page.half_down"
	ActHalfPageUp     Action = "page.half_up"
	ActPageDown       Action = "page.down"
	ActPageUp         Action = "page.up"
	ActCenter         Action = "view.center"
	ActJumpBack       Action = "jump.back"
	ActJumpForward    Action = "jump.forward"
	ActSearchNext     Action = "search.next"
	ActSearchPrev     Action = "search.prev"
	ActChatNextUnread Action = "chat.next_unread"
	ActChatPrevUnread Action = "chat.prev_unread"
	ActFolderNext     Action = "folder.next"
	ActFolderPrev     Action = "folder.prev"
)

// Pane actions.
const (
	ActFocusLeft     Action = "focus.left"
	ActFocusRight    Action = "focus.right"
	ActFocusNext     Action = "focus.next"
	ActFocusPrev     Action = "focus.prev"
	ActSidebarToggle Action = "sidebar.toggle"
	ActChatInfo      Action = "chat.info"
	ActMediaGallery  Action = "chat.media"
)

// Chat actions (apply to the selected chat in the list, or the open chat).
const (
	ActChatOpen         Action = "chat.open"
	ActChatArchive      Action = "chat.archive"
	ActChatMute         Action = "chat.mute"
	ActChatPin          Action = "chat.pin"
	ActChatMarkUnread   Action = "chat.mark_unread"
	ActChatFilter       Action = "chat.filter"
	ActChatDelete       Action = "chat.delete"
	ActChatShowArchived Action = "chat.show_archived"
	ActChatNew          Action = "chat.new"
	ActGroupNew         Action = "group.new"
	ActChatPinned       Action = "chat.pinned"
)

// Message actions (apply to the selected message).
const (
	ActMessageOpen           Action = "message.open"
	ActMessageReply          Action = "message.reply"
	ActMessageReact          Action = "message.react"
	ActMessageEdit           Action = "message.edit"
	ActMessageDelete         Action = "message.delete"
	ActMessageDeleteEveryone Action = "message.delete_everyone"
	ActMessageCopy           Action = "message.copy"
	ActMessageCopyLink       Action = "message.copy_link"
	ActMessageForward        Action = "message.forward"
	ActMessageOpenAttachment Action = "message.open_attachment"
	ActMessageOpenURL        Action = "message.open_url"
	ActMessagePin            Action = "message.pin"
	ActMessageGotoQuote      Action = "message.goto_quote"
	ActMessageInfo           Action = "message.info"
	ActMessageRevealSpoiler  Action = "message.reveal_spoiler"
	ActMessageReactions      Action = "message.reactions"
)

// Mode switching and visual selection actions.
const (
	ActModeNormal      Action = "mode.normal"
	ActModeInsert      Action = "mode.insert"
	ActModeAppend      Action = "mode.append"
	ActModeInsertStart Action = "mode.insert_start"
	ActModeVisual      Action = "mode.visual"
	ActModeVisualLine  Action = "mode.visual_line"
	ActModeCommand     Action = "mode.command"
	ActVisualYank      Action = "visual.yank"
	ActVisualDelete    Action = "visual.delete"
	ActVisualForward   Action = "visual.forward"
)

// Composer (insert mode) actions.
const (
	ActComposerSend         Action = "composer.send"
	ActComposerNewline      Action = "composer.newline"
	ActComposerEditor       Action = "composer.editor"
	ActComposerAttach       Action = "composer.attach"
	ActComposerPaste        Action = "composer.paste"
	ActComposerCancelReply  Action = "composer.cancel_reply"
	ActComposerComplete     Action = "composer.complete"
	ActComposerCompletePrev Action = "composer.complete_prev"
	ActComposerCancel       Action = "composer.cancel"
	ActComposerEmoji        Action = "composer.emoji"
	ActComposerSticker      Action = "composer.sticker"
	ActComposerLeft         Action = "composer.left"
	ActComposerUp           Action = "composer.up"
	ActMessageDownOrCompose Action = "message.down"
)

// Search actions.
const (
	ActSearchForward     Action = "search.forward"
	ActSearchGlobal      Action = "search.global"
	ActSearchSubmit      Action = "search.submit"
	ActSearchCancel      Action = "search.cancel"
	ActSearchHistoryPrev Action = "search.history_prev"
	ActSearchHistoryNext Action = "search.history_next"
)

// Command line actions.
const (
	ActCmdExecute      Action = "cmdline.execute"
	ActCmdCancel       Action = "cmdline.cancel"
	ActCmdComplete     Action = "cmdline.complete"
	ActCmdCompletePrev Action = "cmdline.complete_prev"
	ActCmdHistoryPrev  Action = "cmdline.history_prev"
	ActCmdHistoryNext  Action = "cmdline.history_next"
)

// Viewer and overlay actions.
const (
	ActItemSelect Action = "item.select"
	ActZoomIn     Action = "view.zoom_in"
	ActZoomOut    Action = "view.zoom_out"
	ActZoomReset  Action = "view.zoom_reset"
	ActStoryNext  Action = "story.next"
	ActStoryPrev  Action = "story.prev"
	ActConfirmYes Action = "confirm.yes"
	ActConfirmNo  Action = "confirm.no"
)

// General actions.
const (
	ActHelp        Action = "app.help"
	ActFinderChats Action = "finder.chats"
	ActStories     Action = "app.stories"
	ActCalls       Action = "app.calls"
	ActSettings    Action = "app.settings"
	ActThemePicker Action = "app.theme"
	ActRedraw      Action = "app.redraw"
	ActBack        Action = "app.back"
	ActCancel      Action = "app.cancel"
	ActQuit        Action = "app.quit"
	ActQuitHint    Action = "app.quit_hint"
	ActLock        Action = "app.lock"
	ActRepeat      Action = "app.repeat"
)

// ActionInfo describes a registered action.
type ActionInfo struct {
	// Name is the action identifier.
	Name Action
	// Help is a short description for the help overlay ("Reply").
	Help string
	// Group is the help overlay heading.
	Group Group
	// Repeatable marks actions that "." repeats (changes, not motions).
	Repeatable bool
}

// registry is the list of all known actions.
var registry = []ActionInfo{
	{ActCursorDown, "Move down", GroupNavigation, false},
	{ActCursorUp, "Move up", GroupNavigation, false},
	{ActCursorLeft, "Move left", GroupNavigation, false},
	{ActCursorRight, "Move right", GroupNavigation, false},
	{ActCursorTop, "Go to top (or item N)", GroupNavigation, false},
	{ActCursorBottom, "Go to bottom (or item N)", GroupNavigation, false},
	{ActHalfPageDown, "Half page down", GroupNavigation, false},
	{ActHalfPageUp, "Half page up", GroupNavigation, false},
	{ActPageDown, "Page down", GroupNavigation, false},
	{ActPageUp, "Page up", GroupNavigation, false},
	{ActCenter, "Center selection", GroupNavigation, false},
	{ActJumpBack, "Jump back", GroupNavigation, false},
	{ActJumpForward, "Jump forward", GroupNavigation, false},
	{ActSearchNext, "Next search hit", GroupNavigation, false},
	{ActSearchPrev, "Previous search hit", GroupNavigation, false},
	{ActChatNextUnread, "Next unread chat", GroupNavigation, false},
	{ActChatPrevUnread, "Previous unread chat", GroupNavigation, false},
	{ActFolderNext, "Next chat folder", GroupNavigation, false},
	{ActFolderPrev, "Previous chat folder", GroupNavigation, false},

	{ActFocusLeft, "Focus left pane", GroupPanes, false},
	{ActFocusRight, "Focus right pane", GroupPanes, false},
	{ActFocusNext, "Focus next pane", GroupPanes, false},
	{ActFocusPrev, "Focus previous pane", GroupPanes, false},
	{ActSidebarToggle, "Toggle sidebar", GroupPanes, false},
	{ActChatInfo, "Chat settings", GroupPanes, false},
	{ActMediaGallery, "Media gallery", GroupPanes, false},

	{ActChatOpen, "Open chat", GroupChats, false},
	{ActChatArchive, "Archive chat", GroupChats, true},
	{ActChatMute, "Mute chat", GroupChats, true},
	{ActChatPin, "Pin chat", GroupChats, true},
	{ActChatMarkUnread, "Toggle unread", GroupChats, true},
	{ActChatFilter, "Filter chats", GroupChats, false},
	{ActChatDelete, "Delete chat", GroupChats, true},
	{ActChatShowArchived, "Show archived", GroupChats, false},
	{ActChatNew, "New chat", GroupChats, false},
	{ActGroupNew, "New group", GroupChats, false},
	{ActChatPinned, "Pinned messages", GroupChats, false},

	{ActMessageOpen, "Open / expand", GroupMessages, false},
	{ActMessageReply, "Reply", GroupMessages, false},
	{ActMessageReact, "React", GroupMessages, false},
	{ActMessageEdit, "Edit message", GroupMessages, false},
	{ActMessageDelete, "Delete for me", GroupMessages, true},
	{ActMessageDeleteEveryone, "Delete for everyone", GroupMessages, true},
	{ActMessageCopy, "Copy text", GroupMessages, false},
	{ActMessageCopyLink, "Copy link", GroupMessages, false},
	{ActMessageForward, "Forward", GroupMessages, false},
	{ActMessageOpenAttachment, "Open attachment", GroupMessages, false},
	{ActMessageOpenURL, "Open URL", GroupMessages, false},
	{ActMessagePin, "Pin / unpin message", GroupMessages, true},
	{ActMessageGotoQuote, "Jump to quoted", GroupMessages, false},
	{ActMessageInfo, "Message details", GroupMessages, false},
	{ActMessageRevealSpoiler, "Reveal spoiler", GroupMessages, false},
	{ActMessageReactions, "Reaction details", GroupMessages, false},

	{ActModeNormal, "Normal mode", GroupSelection, false},
	{ActModeInsert, "Write message", GroupComposer, false},
	{ActModeAppend, "Write (cursor at end)", GroupComposer, false},
	{ActModeInsertStart, "Write (cursor at start)", GroupComposer, false},
	{ActModeVisual, "Select messages", GroupSelection, false},
	{ActModeVisualLine, "Select lines", GroupSelection, false},
	{ActModeCommand, "Command line", GroupGeneral, false},
	{ActVisualYank, "Copy selection", GroupSelection, false},
	{ActVisualDelete, "Delete selection", GroupSelection, true},
	{ActVisualForward, "Forward selection", GroupSelection, false},

	{ActComposerSend, "Send", GroupComposer, false},
	{ActComposerNewline, "New line", GroupComposer, false},
	{ActComposerEditor, "Compose in $EDITOR", GroupComposer, false},
	{ActComposerAttach, "Attach file", GroupComposer, false},
	{ActComposerPaste, "Paste (text or image)", GroupComposer, false},
	{ActComposerCancelReply, "Cancel reply", GroupComposer, false},
	{ActComposerComplete, "Complete", GroupComposer, false},
	{ActComposerCompletePrev, "Previous completion", GroupComposer, false},
	{ActComposerCancel, "Cancel / clear", GroupComposer, false},
	{ActComposerEmoji, "Emoji picker", GroupComposer, false},
	{ActComposerSticker, "Sticker picker", GroupComposer, false},
	{ActComposerLeft, "Cursor left (at start: chat list)", GroupComposer, false},
	{ActComposerUp, "Cursor up (on first line: select messages)", GroupComposer, false},
	{ActMessageDownOrCompose, "Next message (past the newest: write)", GroupComposer, false},

	{ActSearchForward, "Search in chat", GroupSearch, false},
	{ActSearchGlobal, "Search everywhere", GroupSearch, false},
	{ActSearchSubmit, "Search", GroupSearch, false},
	{ActSearchCancel, "Cancel search", GroupSearch, false},
	{ActSearchHistoryPrev, "Older search", GroupSearch, false},
	{ActSearchHistoryNext, "Newer search", GroupSearch, false},

	{ActCmdExecute, "Run command", GroupCommand, false},
	{ActCmdCancel, "Cancel", GroupCommand, false},
	{ActCmdComplete, "Complete", GroupCommand, false},
	{ActCmdCompletePrev, "Previous completion", GroupCommand, false},
	{ActCmdHistoryPrev, "Older command", GroupCommand, false},
	{ActCmdHistoryNext, "Newer command", GroupCommand, false},

	{ActItemSelect, "Select", GroupViewer, false},
	{ActZoomIn, "Zoom in", GroupViewer, false},
	{ActZoomOut, "Zoom out", GroupViewer, false},
	{ActZoomReset, "Reset zoom", GroupViewer, false},
	{ActStoryNext, "Next story", GroupViewer, false},
	{ActStoryPrev, "Previous story", GroupViewer, false},
	{ActConfirmYes, "Confirm", GroupViewer, false},
	{ActConfirmNo, "Cancel", GroupViewer, false},

	{ActHelp, "Help", GroupGeneral, false},
	{ActFinderChats, "Find chat", GroupGeneral, false},
	{ActStories, "Stories", GroupGeneral, false},
	{ActCalls, "Calls", GroupGeneral, false},
	{ActSettings, "Settings", GroupGeneral, false},
	{ActThemePicker, "Theme picker", GroupGeneral, false},
	{ActRedraw, "Redraw screen", GroupGeneral, false},
	{ActBack, "Back / close", GroupGeneral, false},
	{ActCancel, "Clear / cancel", GroupGeneral, false},
	{ActQuit, "Quit", GroupGeneral, false},
	{ActQuitHint, "Quit hint (use :q)", GroupGeneral, false},
	{ActLock, "Lock", GroupGeneral, false},
	{ActRepeat, "Repeat last change", GroupGeneral, false},
}

var (
	actionIndex = map[Action]int{}
	groupIndex  = map[Group]int{}
)

func init() {
	for i, a := range registry {
		if _, dup := actionIndex[a.Name]; dup {
			panic("keymap: duplicate action " + string(a.Name))
		}
		actionIndex[a.Name] = i
	}
	for i, g := range Groups {
		groupIndex[g] = i
	}
}

// LookupAction returns the registered info for an action name.
func LookupAction(name Action) (ActionInfo, bool) {
	i, ok := actionIndex[name]
	if !ok {
		return ActionInfo{}, false
	}
	return registry[i], true
}

// Actions returns every registered action, sorted by group then name.
func Actions() []ActionInfo {
	out := append([]ActionInfo(nil), registry...)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := groupIndex[out[i].Group], groupIndex[out[j].Group]
		if gi != gj {
			return gi < gj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Help returns the registered help text of the action, or its name.
func (a Action) Help() string {
	if info, ok := LookupAction(a); ok {
		return info.Help
	}
	return string(a)
}

// Valid reports whether a is a registered action.
func (a Action) Valid() bool {
	_, ok := actionIndex[a]
	return ok
}

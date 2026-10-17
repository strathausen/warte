package keymap

// DefaultLeader is the default leader key (the space bar).
const DefaultLeader = "space"

// DefaultSpec is one default binding in source form: key notation as
// accepted by ParseKeys, applied to every listed mode.
type DefaultSpec struct {
	// Modes the binding applies to.
	Modes []Mode
	// Context scopes the binding (Global for mode-wide bindings).
	Context Context
	// Keys in vim-like notation ("gg", "<C-d>", "<leader>s").
	Keys string
	// Action to perform.
	Action Action
	// Help overrides the action's registered help text when non-empty.
	Help string
}

var (
	normalOnly = []Mode{Normal}
	navModes   = []Mode{Normal, Visual, VisualLine}
	visualOnly = []Mode{Visual}
	vlineOnly  = []Mode{VisualLine}
	bothVisual = []Mode{Visual, VisualLine}
	insertOnly = []Mode{Insert}
	cmdOnly    = []Mode{Command}
	searchOnly = []Mode{Search}
	drawModes  = []Mode{Normal, Insert, Visual, VisualLine}
)

// DefaultSpecs is the default keymap (SPEC.md §5). Order matters: the first
// key listed for an action is the one shown in hints.
var DefaultSpecs = []DefaultSpec{
	// Navigation (normal and visual).
	{navModes, Global, "j", ActCursorDown, ""},
	{navModes, Global, "<Down>", ActCursorDown, ""},
	{navModes, Global, "k", ActCursorUp, ""},
	{navModes, Global, "<Up>", ActCursorUp, ""},
	{navModes, Global, "h", ActCursorLeft, ""},
	{navModes, Global, "<Left>", ActCursorLeft, ""},
	{navModes, Global, "l", ActCursorRight, ""},
	{navModes, Global, "<Right>", ActCursorRight, ""},
	{navModes, Global, "gg", ActCursorTop, ""},
	{navModes, Global, "<Home>", ActCursorTop, ""},
	{navModes, Global, "G", ActCursorBottom, ""},
	{navModes, Global, "<End>", ActCursorBottom, ""},
	{navModes, Global, "<C-d>", ActHalfPageDown, ""},
	{navModes, Global, "<C-u>", ActHalfPageUp, ""},
	{navModes, Global, "<C-f>", ActPageDown, ""},
	{navModes, Global, "<PageDown>", ActPageDown, ""},
	{navModes, Global, "<C-b>", ActPageUp, ""},
	{navModes, Global, "<PageUp>", ActPageUp, ""},
	{navModes, Global, "zz", ActCenter, ""},
	{normalOnly, Global, "<C-o>", ActJumpBack, ""},
	{normalOnly, Global, "<C-i>", ActJumpForward, ""},
	{normalOnly, Global, "n", ActSearchNext, ""},
	{normalOnly, Global, "N", ActSearchPrev, ""},
	{normalOnly, Global, "]u", ActChatNextUnread, ""},
	{normalOnly, Global, "[u", ActChatPrevUnread, ""},
	{normalOnly, Global, "gt", ActFolderNext, ""},
	{normalOnly, Global, "gT", ActFolderPrev, ""},

	// Panes.
	{normalOnly, Global, "H", ActFocusLeft, ""},
	{normalOnly, Global, "<C-w>h", ActFocusLeft, ""},
	{normalOnly, Global, "<C-w><Left>", ActFocusLeft, ""},
	{normalOnly, Global, "L", ActFocusRight, ""},
	{normalOnly, Global, "<C-w>l", ActFocusRight, ""},
	{normalOnly, Global, "<C-w><Right>", ActFocusRight, ""},
	{normalOnly, Global, "<Tab>", ActFocusNext, ""},
	{normalOnly, Global, "<C-w>w", ActFocusNext, ""},
	{normalOnly, Global, "<S-Tab>", ActFocusPrev, ""},
	{normalOnly, Global, "<C-w>W", ActFocusPrev, ""},
	{normalOnly, Global, "<leader>i", ActSidebarToggle, ""},
	{normalOnly, Global, "gi", ActChatInfo, ""},
	{normalOnly, Global, "gm", ActMediaGallery, ""},

	// Chats (selected chat in the list, or the open chat).
	{normalOnly, Global, "za", ActChatArchive, ""},
	{normalOnly, Global, "zm", ActChatMute, ""},
	{normalOnly, Global, "<leader>m", ActChatMute, ""},
	{normalOnly, Global, "zp", ActChatPin, ""},
	{normalOnly, Global, "zu", ActChatMarkUnread, ""},
	{normalOnly, Global, "<leader>n", ActChatNew, ""},
	{normalOnly, Global, "<leader>g", ActGroupNew, ""},
	{normalOnly, Global, "gp", ActChatPinned, ""},
	{normalOnly, ChatList, "<CR>", ActChatOpen, ""},
	{normalOnly, ChatList, "/", ActChatFilter, ""},
	{normalOnly, ChatList, "dd", ActChatDelete, ""},
	{normalOnly, ChatList, "A", ActChatShowArchived, ""},
	{normalOnly, ChatList, "i", ActModeInsert, "Open chat and write"},
	{normalOnly, ChatList, "a", ActModeInsert, "Open chat and write"},
	{normalOnly, ChatList, "o", ActModeInsert, "Open chat and write"},

	// Messages.
	{normalOnly, Conversation, "i", ActModeInsert, ""},
	{normalOnly, Conversation, "a", ActModeAppend, ""},
	{normalOnly, Conversation, "I", ActModeInsertStart, ""},
	{normalOnly, Conversation, "<CR>", ActMessageOpen, ""},
	{normalOnly, Conversation, "<Down>", ActMessageDownOrCompose, ""},
	{normalOnly, Conversation, "r", ActMessageReply, ""},
	{normalOnly, Conversation, "R", ActMessageReact, ""},
	{normalOnly, Conversation, "+", ActMessageReact, ""},
	{normalOnly, Conversation, "e", ActMessageEdit, ""},
	{normalOnly, Conversation, "dd", ActMessageDelete, ""},
	{normalOnly, Conversation, "dD", ActMessageDeleteEveryone, ""},
	{normalOnly, Conversation, "yy", ActMessageCopy, ""},
	{normalOnly, Conversation, "yl", ActMessageCopyLink, ""},
	{normalOnly, Conversation, "f", ActMessageForward, ""},
	{normalOnly, Conversation, "o", ActMessageOpenAttachment, ""},
	{normalOnly, Conversation, "gx", ActMessageOpenURL, ""},
	{normalOnly, Conversation, "p", ActMessagePin, ""},
	{normalOnly, Conversation, "gq", ActMessageGotoQuote, ""},
	{normalOnly, Conversation, "K", ActMessageInfo, ""},
	{normalOnly, Conversation, "gr", ActMessageReactions, ""},
	{normalOnly, Conversation, "s", ActMessageRevealSpoiler, ""},
	{normalOnly, Conversation, "/", ActSearchForward, ""},
	{normalOnly, Conversation, "v", ActModeVisual, ""},
	{normalOnly, Conversation, "V", ActModeVisualLine, ""},

	// Sidebar.
	{normalOnly, Sidebar, "<CR>", ActItemSelect, ""},

	// Visual selection.
	{bothVisual, Global, "<Esc>", ActModeNormal, "Cancel selection"},
	{bothVisual, Global, "<C-c>", ActModeNormal, "Cancel selection"},
	{bothVisual, Global, "y", ActVisualYank, ""},
	{bothVisual, Global, "d", ActVisualDelete, ""},
	{bothVisual, Global, "f", ActVisualForward, ""},
	{visualOnly, Global, "v", ActModeNormal, "Cancel selection"},
	{visualOnly, Global, "V", ActModeVisualLine, ""},
	{vlineOnly, Global, "V", ActModeNormal, "Cancel selection"},
	{vlineOnly, Global, "v", ActModeVisual, ""},

	// Composer.
	{insertOnly, Global, "<Esc>", ActModeNormal, ""},
	{insertOnly, Global, "<CR>", ActComposerSend, ""},
	{insertOnly, Global, "<A-CR>", ActComposerNewline, ""},
	{insertOnly, Global, "<S-CR>", ActComposerNewline, ""},
	{insertOnly, Global, "<C-e>", ActComposerEditor, ""},
	{insertOnly, Global, "<C-a>", ActComposerAttach, ""},
	{insertOnly, Global, "<C-v>", ActComposerPaste, ""},
	{insertOnly, Global, "<C-r>", ActComposerCancelReply, ""},
	{insertOnly, Global, "<Tab>", ActComposerComplete, ""},
	{insertOnly, Global, "<S-Tab>", ActComposerCompletePrev, ""},
	{insertOnly, Global, "<C-c>", ActComposerCancel, ""},
	{insertOnly, Global, "<C-g>", ActComposerEmoji, ""},
	{insertOnly, Global, "<C-s>", ActComposerSticker, ""},
	{insertOnly, Global, "<Left>", ActComposerLeft, ""},
	{insertOnly, Global, "<Up>", ActComposerUp, ""},

	// Command line.
	{cmdOnly, Global, "<CR>", ActCmdExecute, ""},
	{cmdOnly, Global, "<Esc>", ActCmdCancel, ""},
	{cmdOnly, Global, "<C-c>", ActCmdCancel, ""},
	{cmdOnly, Global, "<Tab>", ActCmdComplete, ""},
	{cmdOnly, Global, "<S-Tab>", ActCmdCompletePrev, ""},
	{cmdOnly, Global, "<Up>", ActCmdHistoryPrev, ""},
	{cmdOnly, Global, "<C-p>", ActCmdHistoryPrev, ""},
	{cmdOnly, Global, "<Down>", ActCmdHistoryNext, ""},
	{cmdOnly, Global, "<C-n>", ActCmdHistoryNext, ""},

	// Search line.
	{searchOnly, Global, "<CR>", ActSearchSubmit, ""},
	{searchOnly, Global, "<Esc>", ActSearchCancel, ""},
	{searchOnly, Global, "<C-c>", ActSearchCancel, ""},
	{searchOnly, Global, "<Up>", ActSearchHistoryPrev, ""},
	{searchOnly, Global, "<C-p>", ActSearchHistoryPrev, ""},
	{searchOnly, Global, "<Down>", ActSearchHistoryNext, ""},
	{searchOnly, Global, "<C-n>", ActSearchHistoryNext, ""},

	// Search (normal).
	{normalOnly, Global, "/", ActSearchForward, ""},
	{normalOnly, Global, "<leader>/", ActSearchGlobal, ""},

	// Overlays.
	{normalOnly, AnyOverlay, "q", ActBack, "Close"},
	{normalOnly, AnyOverlay, "<Esc>", ActBack, "Close"},
	{normalOnly, AnyOverlay, "<CR>", ActItemSelect, ""},
	{normalOnly, Overlay(OverlayImage), "+", ActZoomIn, ""},
	{normalOnly, Overlay(OverlayImage), "=", ActZoomIn, ""},
	{normalOnly, Overlay(OverlayImage), "-", ActZoomOut, ""},
	{normalOnly, Overlay(OverlayImage), "0", ActZoomReset, ""},
	{normalOnly, Overlay(OverlayStory), "l", ActStoryNext, ""},
	{normalOnly, Overlay(OverlayStory), "<Right>", ActStoryNext, ""},
	{normalOnly, Overlay(OverlayStory), "h", ActStoryPrev, ""},
	{normalOnly, Overlay(OverlayStory), "<Left>", ActStoryPrev, ""},
	{normalOnly, Overlay(OverlayStory), "r", ActMessageReply, "Reply to story"},
	{normalOnly, Overlay(OverlayConfirm), "y", ActConfirmYes, ""},
	{normalOnly, Overlay(OverlayConfirm), "<CR>", ActConfirmYes, ""},
	{normalOnly, Overlay(OverlayConfirm), "n", ActConfirmNo, ""},
	{normalOnly, Overlay(OverlayConfirm), "<Esc>", ActConfirmNo, ""},
	{normalOnly, Overlay(OverlayConfirm), "q", ActConfirmNo, ""},

	// General.
	{normalOnly, Global, ":", ActModeCommand, ""},
	{normalOnly, Global, "?", ActHelp, ""},
	{normalOnly, Global, "<leader><leader>", ActFinderChats, ""},
	{normalOnly, Global, "<leader>s", ActStories, ""},
	{normalOnly, Global, "<leader>c", ActCalls, ""},
	{normalOnly, Global, "<leader>,", ActSettings, ""},
	{normalOnly, Global, "<leader>t", ActThemePicker, ""},
	{normalOnly, Global, "<leader>l", ActLock, ""},
	{drawModes, Global, "<C-l>", ActRedraw, ""},
	{normalOnly, Global, "q", ActBack, ""},
	{normalOnly, Global, "<Esc>", ActCancel, ""},
	{normalOnly, Global, "ZZ", ActQuit, ""},
	{normalOnly, Global, "<C-c>", ActQuitHint, ""},
	{normalOnly, Global, ".", ActRepeat, ""},
}

// ShortHelpItem is one status-line hint: one or more actions shown with a
// single label ("j/k move").
type ShortHelpItem struct {
	// Actions whose primary keys are shown, joined with "/".
	Actions []Action
	// Label is the short description.
	Label string
}

type shortKey struct {
	mode Mode
	ctx  Context
}

// defaultShortHelp lists the status-line hints per mode and context in
// priority order. Lookup falls back along the context chain.
var defaultShortHelp = map[shortKey][]ShortHelpItem{
	{Normal, Global}: {
		{[]Action{ActCursorDown, ActCursorUp}, "move"},
		{[]Action{ActFinderChats}, "find chat"},
		{[]Action{ActModeCommand}, "command"},
		{[]Action{ActHelp}, "help"},
		{[]Action{ActQuit}, "quit"},
	},
	{Normal, ChatList}: {
		{[]Action{ActCursorDown, ActCursorUp}, "move"},
		{[]Action{ActChatOpen}, "open"},
		{[]Action{ActChatFilter}, "filter"},
		{[]Action{ActFinderChats}, "find chat"},
		{[]Action{ActHelp}, "help"},
	},
	{Normal, Conversation}: {
		{[]Action{ActCursorDown, ActCursorUp}, "move"},
		{[]Action{ActModeInsert}, "write"},
		{[]Action{ActMessageReply}, "reply"},
		{[]Action{ActMessageReact}, "react"},
		{[]Action{ActHelp}, "help"},
	},
	{Normal, Sidebar}: {
		{[]Action{ActCursorDown, ActCursorUp}, "move"},
		{[]Action{ActItemSelect}, "open"},
		{[]Action{ActSidebarToggle}, "hide"},
		{[]Action{ActFocusLeft}, "back"},
		{[]Action{ActHelp}, "help"},
	},
	{Normal, AnyOverlay}: {
		{[]Action{ActCursorDown, ActCursorUp}, "move"},
		{[]Action{ActItemSelect}, "select"},
		{[]Action{ActBack}, "close"},
	},
	{Normal, Overlay(OverlayImage)}: {
		{[]Action{ActZoomIn, ActZoomOut}, "zoom"},
		{[]Action{ActCursorLeft, ActCursorDown, ActCursorUp, ActCursorRight}, "pan"},
		{[]Action{ActZoomReset}, "reset"},
		{[]Action{ActBack}, "close"},
	},
	{Normal, Overlay(OverlayStory)}: {
		{[]Action{ActStoryPrev, ActStoryNext}, "prev/next"},
		{[]Action{ActMessageReply}, "reply"},
		{[]Action{ActBack}, "close"},
	},
	{Normal, Overlay(OverlayConfirm)}: {
		{[]Action{ActConfirmYes}, "yes"},
		{[]Action{ActConfirmNo}, "no"},
	},
	{Insert, Global}: {
		{[]Action{ActComposerSend}, "send"},
		{[]Action{ActComposerNewline}, "newline"},
		{[]Action{ActComposerEditor}, "editor"},
		{[]Action{ActComposerAttach}, "attach"},
		{[]Action{ActModeNormal}, "normal"},
	},
	{Visual, Global}: {
		{[]Action{ActCursorDown, ActCursorUp}, "extend"},
		{[]Action{ActVisualYank}, "copy"},
		{[]Action{ActVisualDelete}, "delete"},
		{[]Action{ActVisualForward}, "forward"},
		{[]Action{ActModeNormal}, "cancel"},
	},
	{VisualLine, Global}: {
		{[]Action{ActCursorDown, ActCursorUp}, "extend"},
		{[]Action{ActVisualYank}, "copy"},
		{[]Action{ActVisualDelete}, "delete"},
		{[]Action{ActVisualForward}, "forward"},
		{[]Action{ActModeNormal}, "cancel"},
	},
	{Command, Global}: {
		{[]Action{ActCmdExecute}, "run"},
		{[]Action{ActCmdComplete}, "complete"},
		{[]Action{ActCmdHistoryPrev, ActCmdHistoryNext}, "history"},
		{[]Action{ActCmdCancel}, "cancel"},
	},
	{Search, Global}: {
		{[]Action{ActSearchSubmit}, "search"},
		{[]Action{ActSearchHistoryPrev, ActSearchHistoryNext}, "history"},
		{[]Action{ActSearchCancel}, "cancel"},
	},
}

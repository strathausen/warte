package ui

import (
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/keymap"
)

// Command is an ex command (":name args") contributed by a view package.
// Register it with RegisterCommand from an init function; the App picks up
// every registered command when it is created.
type Command struct {
	// Name is typed after ":"; Aliases are alternatives.
	Name    string
	Aliases []string
	// Args is a short usage string for help and completion ("<duration|off>").
	Args string
	// Help is a one-line description.
	Help string
	// Complete returns completion candidates for the last argument. args
	// holds the arguments typed so far (the last one possibly partial).
	// Optional.
	Complete func(ctx Context, args []string) []string
	// Run executes the command.
	Run func(ctx Context, args []string) tea.Cmd
}

// ActionHandler handles a keymap action the App itself does not implement
// (for example keymap.ActStories or keymap.ActFinderChats), typically by
// returning Push(newOverlay(ctx)).
type ActionHandler func(ctx Context) tea.Cmd

var (
	regMu    sync.RWMutex
	commands = map[string]Command{}
	actions  = map[keymap.Action]ActionHandler{}
)

// RegisterCommand adds or replaces an ex command. It is safe to call from
// init functions.
func RegisterCommand(c Command) {
	if strings.TrimSpace(c.Name) == "" || c.Run == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	commands[strings.ToLower(c.Name)] = c
}

// Commands returns the registered commands sorted by name.
func Commands() []Command {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Command, 0, len(commands))
	for _, c := range commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RegisterAction installs the handler for a keymap action. Built-in actions
// the App implements itself cannot be overridden this way; everything else
// (stories, calls, settings, finder, theme picker, chat info, media gallery,
// global search, new chat/group, lock, the full reaction picker
// ActReactionPicker, ...) is dispatched to registered handlers.
func RegisterAction(a keymap.Action, h ActionHandler) {
	if h == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	actions[a] = h
}

// ActionHandlerFor returns the registered handler for a, if any.
func ActionHandlerFor(a keymap.Action) (ActionHandler, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	h, ok := actions[a]
	return h, ok
}

// ActReactionPicker is the action the quick reaction bar dispatches for its
// "…" entry. Register a handler that pushes the full emoji picker; it reads
// the target message from Context.Selected and answers with ReactMsg.
const ActReactionPicker keymap.Action = "message.react_picker"

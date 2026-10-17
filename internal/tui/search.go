package tui

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

type searchScope int

const (
	scopeConv searchScope = iota
	scopeList
)

type searchState struct {
	input     textinput.Model
	scope     searchScope
	history   []string
	histIdx   int
	highlight string           // active query highlighted in the conversation
	hits      []core.MessageID // sorted oldest → newest
	hitSet    map[core.MessageID]bool
	idx       int
	prevList  string // chat list filter before editing (restored on esc)
	seq       int
}

func newSearch(a *App) *searchState {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 256
	ti.Placeholder = "search"
	return &searchState{input: ti, histIdx: -1, hitSet: map[core.MessageID]bool{}}
}

func (s *searchState) restyle(a *App) { styleInput(&s.input, a.styles) }

func (s *searchState) isHit(id core.MessageID) bool { return s.hitSet[id] }

func (s *searchState) clearHits() {
	s.highlight = ""
	s.hits = nil
	s.hitSet = map[core.MessageID]bool{}
	s.idx = 0
}

// startSearch enters Search mode for the chat list (filter) or the open
// conversation.
func (a *App) startSearch(scope searchScope) tea.Cmd {
	s := a.search
	s.scope = scope
	s.histIdx = -1
	if scope == scopeList {
		s.prevList = a.list.filter
		s.input.SetValue(a.list.filter)
		s.input.Placeholder = "filter chats"
	} else {
		if !a.conv.open {
			return a.toast(ui.ToastInfo, "Open a chat to search it")
		}
		s.input.SetValue("")
		s.input.Placeholder = "search " + clean(a.conv.chat.Title)
	}
	s.input.CursorEnd()
	a.keys.SetMode(keymap.Search)
	return s.input.Focus()
}

func (a *App) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	s := a.search
	if msg.String() == "backspace" && s.input.Value() == "" {
		return a.cancelSearch()
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return tea.Batch(cmd, s.live(a))
}

// live applies filter-as-you-type in the chat list.
func (s *searchState) live(a *App) tea.Cmd {
	if s.scope == scopeList {
		a.list.filter = strings.TrimSpace(s.input.Value())
		a.list.sel = 0
		a.refreshChats()
	}
	return nil
}

func (a *App) cancelSearch() tea.Cmd {
	s := a.search
	s.input.Blur()
	a.keys.SetMode(keymap.Normal)
	if s.scope == scopeList {
		a.list.filter = s.prevList
		a.refreshChats()
	}
	return nil
}

func (a *App) submitSearch() tea.Cmd {
	s := a.search
	q := strings.TrimSpace(s.input.Value())
	s.input.Blur()
	a.keys.SetMode(keymap.Normal)
	if q != "" && (len(s.history) == 0 || s.history[len(s.history)-1] != q) {
		s.history = append(s.history, q)
	}
	if s.scope == scopeList {
		a.list.filter = q
		a.refreshChats()
		return nil
	}
	return a.runSearch(q)
}

func (a *App) searchHistory(dir int) {
	s := a.search
	if len(s.history) == 0 {
		return
	}
	if s.histIdx == -1 {
		if dir > 0 {
			return
		}
		s.histIdx = len(s.history)
	}
	s.histIdx += dir
	if s.histIdx >= len(s.history) {
		s.histIdx = -1
		s.input.SetValue("")
	} else {
		s.histIdx = clamp(s.histIdx, 0, len(s.history)-1)
		s.input.SetValue(s.history[s.histIdx])
	}
	s.input.CursorEnd()
}

type searchResultMsg struct {
	seq   int
	query string
	chat  core.ChatID
	hits  []core.SearchHit
	err   error
}

// runSearch searches loaded messages immediately and asks the service for
// older matches.
func (a *App) runSearch(q string) tea.Cmd {
	s := a.search
	if !a.conv.open || q == "" {
		s.clearHits()
		return nil
	}
	s.clearHits()
	s.highlight = q
	lq := strings.ToLower(q)
	for _, e := range a.conv.msgs {
		if e.m.Kind != core.KindNormal || e.m.RemoteDeleted {
			continue
		}
		plain := strings.ToLower(format.PlainText(format.Spans(e.m.Body, e.m.Ranges, a.mentionName)))
		if strings.Contains(plain, lq) {
			s.addHit(e.m.ID)
		}
	}
	s.sortHits()
	cmd := a.jumpToHit(-1, true)
	s.seq++
	seq := s.seq
	chat := a.conv.chat.ID
	svc := a.svc
	remote := func() tea.Msg {
		ctx, cancel := a.opCtx()
		defer cancel()
		hits, err := svc.Search(ctx, core.SearchQuery{Text: q, Chat: chat, Limit: 500})
		return searchResultMsg{seq: seq, query: q, chat: chat, hits: hits, err: err}
	}
	return tea.Batch(cmd, remote)
}

func (s *searchState) addHit(id core.MessageID) {
	if s.hitSet[id] {
		return
	}
	s.hitSet[id] = true
	s.hits = append(s.hits, id)
}

func (s *searchState) sortHits() {
	sort.Slice(s.hits, func(i, j int) bool { return s.hits[i].Timestamp < s.hits[j].Timestamp })
}

func (s *searchState) applyResults(a *App, msg searchResultMsg) tea.Cmd {
	if msg.seq != s.seq || msg.query != s.highlight {
		return nil
	}
	if msg.err != nil {
		return a.toast(ui.ToastError, "search: "+msg.err.Error())
	}
	var cur core.MessageID
	if s.idx >= 0 && s.idx < len(s.hits) {
		cur = s.hits[s.idx]
	}
	for _, h := range msg.hits {
		s.addHit(h.Message.ID)
	}
	s.sortHits()
	for i, h := range s.hits {
		if h == cur {
			s.idx = i
		}
	}
	a.conv.invalidate()
	if len(s.hits) == 0 {
		return a.toast(ui.ToastInfo, "No matches for "+clean(msg.query))
	}
	if cur.IsZero() {
		return a.jumpToHit(-1, true)
	}
	return nil
}

// jumpToHit moves to the next (dir=1) or previous (dir=-1) hit relative to
// the selection. first=true picks the newest hit at or before the selection.
func (a *App) jumpToHit(dir int, first bool) tea.Cmd {
	s := a.search
	if len(s.hits) == 0 {
		if s.highlight != "" && !first {
			return a.toast(ui.ToastInfo, "No matches for "+clean(s.highlight))
		}
		return nil
	}
	var selTS uint64
	if m := a.conv.selected(); m != nil {
		selTS = m.ID.Timestamp
	}
	idx := -1
	switch {
	case first:
		idx = len(s.hits) - 1
		for i := len(s.hits) - 1; i >= 0; i-- {
			if s.hits[i].Timestamp <= selTS {
				idx = i
				break
			}
		}
	case dir > 0:
		idx = 0
		for i, h := range s.hits {
			if h.Timestamp > selTS {
				idx = i
				break
			}
			if i == len(s.hits)-1 {
				idx = 0 // wrap
			}
		}
	default:
		idx = len(s.hits) - 1
		for i := len(s.hits) - 1; i >= 0; i-- {
			if s.hits[i].Timestamp < selTS {
				idx = i
				break
			}
			if i == 0 {
				idx = len(s.hits) - 1
			}
		}
	}
	s.idx = idx
	a.setFocus(FocusConversation)
	if m := a.conv.selected(); m == nil || m.ID != s.hits[idx] {
		a.recordJump()
	}
	return a.jumpTo(s.hits[idx])
}

// searchFilterKeys are the filter keys of the global search syntax
// (views/finder ParseQuery): "from:bob", "has:image", "before:2026-01", …
var searchFilterKeys = []string{"from", "in", "has", "before", "after", "on"}

// searchFilterKey returns the filter key of a "key:value" word, or "".
func searchFilterKey(word string) string {
	k, v, ok := strings.Cut(word, ":")
	if !ok || v == "" {
		return ""
	}
	k = strings.ToLower(k)
	for _, f := range searchFilterKeys {
		if f == k {
			return k
		}
	}
	return ""
}

// hasSearchFilter reports whether any argument is a search filter.
func hasSearchFilter(args []string) bool {
	for _, w := range args {
		if searchFilterKey(w) != "" {
			return true
		}
	}
	return false
}

// filteredSearch runs ":search" with filters through the message search
// view (":find", registered by views/finder), which parses them. Without an
// "in:" filter the search is limited to the open chat ("in:here"); with one
// it searches the chats it names.
func (a *App) filteredSearch(args []string) tea.Cmd {
	find, ok := a.commands["find"]
	if !ok || find.Run == nil {
		return a.toast(ui.ToastWarning, "Search filters need the search view (not available in this build)")
	}
	scoped := false
	for _, w := range args {
		if searchFilterKey(w) == "in" {
			scoped = true
		}
	}
	out := append([]string(nil), args...)
	if !scoped && a.conv.open {
		out = append(out, "in:here")
	}
	return find.Run(a, out)
}

// completeSearchFilters completes filter keys and has: values.
func completeSearchFilters(_ *App, args []string) []string {
	if len(args) == 0 {
		return nil
	}
	last := args[len(args)-1]
	if strings.HasPrefix(strings.ToLower(last), "has:") {
		return []string{"has:image", "has:video", "has:audio", "has:file", "has:link"}
	}
	if !strings.Contains(last, ":") {
		var out []string
		for _, k := range searchFilterKeys {
			if strings.HasPrefix(k, strings.ToLower(last)) {
				out = append(out, k+":")
			}
		}
		return out
	}
	return nil
}

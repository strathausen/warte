package stories

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// queryTimeout bounds backend calls made from commands.
const queryTimeout = 15 * time.Second

// authorGroup is every live story of one author, oldest first (viewing
// order).
type authorGroup struct {
	Author  core.ACI
	Name    string
	Color   string // Signal avatar color code ("A100"), may be ""
	Self    bool
	Stories []core.Story
}

// latest is the time of the author's newest story.
func (g *authorGroup) latest() time.Time {
	if len(g.Stories) == 0 {
		return time.Time{}
	}
	return g.Stories[len(g.Stories)-1].At
}

// unviewed counts stories not seen yet (own stories never count).
func (g *authorGroup) unviewed() int {
	if g.Self {
		return 0
	}
	n := 0
	for _, s := range g.Stories {
		if !s.Viewed {
			n++
		}
	}
	return n
}

// firstUnviewed is the index of the first story not seen yet (0 if all are).
func (g *authorGroup) firstUnviewed() int {
	if g.Self {
		return 0
	}
	for i, s := range g.Stories {
		if !s.Viewed {
			return i
		}
	}
	return 0
}

// replies sums the replies to the author's stories.
func (g *authorGroup) replies() int {
	n := 0
	for _, s := range g.Stories {
		n += s.Replies
	}
	return n
}

// storiesLoadedMsg carries the result of loadStories.
type storiesLoadedMsg struct {
	groups []authorGroup
	titles map[core.ChatID]string // group chat titles for group stories
	err    error
}

// groupStories groups stories by author: the local account first, then
// authors with unviewed stories, then the rest, each by newest story.
func groupStories(list []core.Story, self core.ACI) []authorGroup {
	idx := map[core.ACI]int{}
	var groups []authorGroup
	for _, s := range list {
		a := s.ID.Author
		i, ok := idx[a]
		if !ok {
			i = len(groups)
			idx[a] = i
			groups = append(groups, authorGroup{Author: a, Name: s.AuthorName, Self: a != "" && a == self})
		}
		if groups[i].Name == "" {
			groups[i].Name = s.AuthorName
		}
		groups[i].Stories = append(groups[i].Stories, s)
	}
	for i := range groups {
		g := &groups[i]
		sort.SliceStable(g.Stories, func(a, b int) bool { return g.Stories[a].At.Before(g.Stories[b].At) })
	}
	rank := func(g *authorGroup) int {
		switch {
		case g.Self:
			return 0
		case g.unviewed() > 0:
			return 1
		}
		return 2
	}
	sort.SliceStable(groups, func(a, b int) bool {
		ra, rb := rank(&groups[a]), rank(&groups[b])
		if ra != rb {
			return ra < rb
		}
		return groups[a].latest().After(groups[b].latest())
	})
	return groups
}

// loadStories fetches the stories and the names/colors needed to show them.
func loadStories(svc core.Service, self core.ACI) tea.Cmd {
	if svc == nil {
		return func() tea.Msg { return storiesLoadedMsg{} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		list, err := svc.Stories(ctx)
		if err != nil {
			return storiesLoadedMsg{err: err}
		}
		groups := groupStories(list, self)
		titles := map[core.ChatID]string{}
		for i := range groups {
			g := &groups[i]
			if g.Author != "" {
				if c, err := svc.Chat(ctx, core.DirectChatID(g.Author)); err == nil {
					g.Color = c.Color
					if g.Name == "" {
						g.Name = c.Title
					}
				}
			}
			for _, s := range g.Stories {
				if s.Group == "" {
					continue
				}
				if _, ok := titles[s.Group]; ok {
					continue
				}
				if c, err := svc.Chat(ctx, s.Group); err == nil {
					titles[s.Group] = c.Title
				} else {
					titles[s.Group] = ""
				}
			}
		}
		return storiesLoadedMsg{groups: groups, titles: titles}
	}
}

// selfACI is the local account's ACI from ctx ("" when unknown).
func selfACI(ctx ui.Context) core.ACI {
	if ctx.Self.ACI != "" {
		return ctx.Self.ACI
	}
	if ctx.Svc != nil {
		return ctx.Svc.Self().ACI
	}
	return ""
}

// relTime formats how long ago t was, compactly ("now", "5m", "3h", "2d").
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return itoa(int(d/time.Hour)) + "h"
	}
	return itoa(int(d/(24*time.Hour))) + "d"
}

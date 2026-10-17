package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
)

// findZone scrolls up through the open chats until a zone of kind k is on
// screen.
func findZone(t *testing.T, h *harness, k zoneKind) zone {
	t.Helper()
	for i := range h.a.list.chats {
		h.run(h.a.openChat(h.a.list.chats[i].ID, nil, true), 0)
		h.keys("<esc>")
		for range 40 {
			h.view()
			for _, z := range h.a.zones.zones {
				if z.kind == k {
					return z
				}
			}
			h.keys("k")
		}
	}
	t.Fatalf("no zone of kind %v in the demo", k)
	return zone{}
}

// Clicking a link inside a message opens it (the message zone must not
// cover the links, attachments and reactions drawn inside it).
func TestClickLinkOpensIt(t *testing.T) {
	h := newHarness(t, 140, 50, withConfig(func(c *config.Config) {
		c.Mouse = true
		c.OpenWith = map[string]string{config.OpenURL: "/usr/bin/true"}
	}))
	z := findZone(t, h, zoneLink)
	if top, _ := h.a.zones.at(z.x+1, z.y); top.kind != zoneLink {
		t.Fatalf("link covered by zone kind %v", top.kind)
	}
	h.send(tea.MouseClickMsg{X: z.x + 1, Y: z.y, Button: tea.MouseLeft})
	if !h.waitFor(time.Second, func() bool { return strings.Contains(h.plain(), "Opening") }) {
		t.Fatalf("link click did not open %q", z.url)
	}
}

func TestClickImageOpensViewer(t *testing.T) {
	h := newHarness(t, 140, 50, withConfig(func(c *config.Config) { c.Mouse = true }))
	z := findZone(t, h, zoneAttachment)
	h.send(tea.MouseClickMsg{X: z.x + 1, Y: z.y, Button: tea.MouseLeft})
	if len(h.a.overlays) == 0 && !strings.Contains(h.plain(), "Opening") {
		t.Fatalf("attachment click did nothing")
	}
}

// Cycling through every folder with gt comes back to the same chat, not
// to whatever was first in the previous folder.
func TestFolderCycleKeepsSelection(t *testing.T) {
	h := newHarness(t, 140, 50, withConfig(func(c *config.Config) { c.Mouse = true }))
	z := findZone(t, h, zoneChat) // the top chat
	h.send(tea.MouseClickMsg{X: z.x + 2, Y: z.y, Button: tea.MouseLeft})
	h.keys("<esc>")
	start := h.a.list.current().ID
	n := len(h.a.list.tabs)
	if n < 2 {
		t.Skip("demo has one folder")
	}
	for i := 0; i < n; i++ {
		h.keys("gt")
		if c := h.a.list.current(); c != nil && h.a.list.indexOf(start) >= 0 && c.ID != start {
			t.Fatalf("folder %d: selected %v, want %v (still in this folder)", h.a.list.tab, c.ID, start)
		}
	}
	if h.a.list.tab != 0 || h.a.list.current().ID != start {
		t.Fatalf("after a full cycle: tab %d, selected %v, want %v", h.a.list.tab, h.a.list.current().ID, start)
	}
}

// Clicking a folder tab switches to it.
func TestClickFolderTab(t *testing.T) {
	h := newHarness(t, 140, 50, withConfig(func(c *config.Config) { c.Mouse = true }))
	if len(h.a.list.tabs) < 3 {
		t.Skip("demo has fewer than three folders")
	}
	for _, want := range []int{2, 1, 0} {
		h.view()
		var hit *zone
		for _, z := range h.a.zones.zones {
			if z.kind == zoneFolder && z.index == want {
				hit = &z // the label if visible, else the "‹" marker
			}
		}
		if hit == nil {
			t.Fatalf("no zone for folder %d", want)
		}
		row := strings.Split(h.plain(), "\n")[hit.y]
		h.send(tea.MouseClickMsg{X: hit.x + hit.w - 1, Y: hit.y, Button: tea.MouseLeft})
		if h.a.list.tab != want {
			t.Fatalf("clicked folder %d, now on %d", want, h.a.list.tab)
		}
		if got, name := ansi.Cut(row, hit.x, hit.x+hit.w), h.a.list.tabs[want].name; got != "‹" && (!strings.HasPrefix(name, strings.TrimSuffix(got, "…")) || got == "") {
			t.Fatalf("zone covers %q, want tab %q", got, name)
		}
	}
}

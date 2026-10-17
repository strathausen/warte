package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"warte/internal/core"
)

// FolderConfig defines a chat folder, written in TOML as an array of tables:
//
//	[[folders]]
//	name = "Work"
//	include_groups = true
//	chats = ["<chat id>"]
//
// A chat belongs to the folder if it is included (by type or explicitly via
// Chats) and not excluded by ExcludeChats or one of the Show* filters.
type FolderConfig struct {
	// Name is the tab label. Required; folders without a name are dropped.
	Name string `toml:"name"`
	// IncludeDirect includes all 1:1 chats (and Note to Self).
	IncludeDirect bool `toml:"include_direct"`
	// IncludeGroups includes all group chats.
	IncludeGroups bool `toml:"include_groups"`
	// Chats lists chat IDs that are always included.
	Chats []string `toml:"chats"`
	// ExcludeChats lists chat IDs that are never included (wins over Chats).
	ExcludeChats []string `toml:"exclude_chats"`
	// UnreadOnly shows only chats with unread messages or marked unread.
	UnreadOnly bool `toml:"unread_only"`
	// HideMuted hides currently muted chats.
	HideMuted bool `toml:"hide_muted"`
	// ShowArchived includes archived chats (hidden by default).
	ShowArchived bool `toml:"show_archived"`
}

// Match reports whether chat belongs to the folder at time now (used for
// mute expiry).
func (f FolderConfig) Match(chat core.Chat, now time.Time) bool {
	id := string(chat.ID)
	if slices.Contains(f.ExcludeChats, id) {
		return false
	}
	if chat.Archived && !f.ShowArchived {
		return false
	}
	included := slices.Contains(f.Chats, id)
	switch chat.Kind {
	case core.ChatGroup:
		included = included || f.IncludeGroups
	default:
		included = included || f.IncludeDirect
	}
	if !included {
		return false
	}
	if f.UnreadOnly && chat.Unread == 0 && !chat.MarkedUnread {
		return false
	}
	if f.HideMuted && chat.Muted(now) {
		return false
	}
	return true
}

func (c *Config) validateFolders() []Warning {
	var w []Warning
	seen := map[string]bool{}
	out := c.Folders[:0]
	for i, f := range c.Folders {
		key := fmt.Sprintf("folders[%d]", i)
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" {
			w = append(w, Warning{Key: key, Message: "folder without a name; ignored"})
			continue
		}
		lname := strings.ToLower(f.Name)
		if seen[lname] {
			w = append(w, Warning{Key: key, Message: fmt.Sprintf("duplicate folder name %q; ignored", f.Name)})
			continue
		}
		seen[lname] = true
		if !f.IncludeDirect && !f.IncludeGroups && len(f.Chats) == 0 {
			w = append(w, Warning{Key: key, Message: fmt.Sprintf("folder %q includes no chats", f.Name)})
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		c.Folders = nil
	} else {
		c.Folders = out
	}
	return w
}

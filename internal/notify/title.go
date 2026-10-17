package notify

import "strconv"

// Title returns the plain window title for unread messages, e.g.
// "(3) warte", or just appName when unread <= 0. It is suitable for
// tea.View.WindowTitle.
func Title(unread int, appName string) string {
	appName = Sanitize(appName, MaxTitleRunes)
	if unread <= 0 {
		return appName
	}
	count := strconv.Itoa(unread)
	if unread > 999 {
		count = "999+"
	}
	if appName == "" {
		return "(" + count + ")"
	}
	return "(" + count + ") " + appName
}

// TitleSequence returns the OSC 2 sequence that sets the window title,
// e.g. "ESC ] 2 ; (3) warte BEL".
func TitleSequence(unread int, appName string) string {
	return "\x1b]2;" + Title(unread, appName) + bel
}

// IconTitleSequence returns the OSC 0 sequence that sets both the icon name
// (tab title) and the window title, e.g. "ESC ] 0 ; (3) warte BEL".
func IconTitleSequence(unread int, appName string) string {
	return "\x1b]0;" + Title(unread, appName) + bel
}

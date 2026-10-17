package media

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/termimg"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Item is one attachment together with the message that carries it. Viewers,
// the gallery and the player all work on Items.
type Item struct {
	Message    core.Message
	Attachment core.Attachment
}

// Ref returns the image reference of the item for ui.Images.
func (it Item) Ref() ui.ImageRef {
	return ui.ImageRef{Kind: ui.ImageAttachment, Message: it.Message.ID, Attachment: it.Attachment, Label: kindLabel(it.Attachment)}
}

// key identifies an item (message + attachment).
func (it Item) key() string { return it.Message.ID.String() + "#" + it.Attachment.ID }

// IsVisual reports whether the attachment belongs on the Media tab (photos,
// GIFs and videos).
func IsVisual(a core.Attachment) bool { return a.IsImage() || a.IsVideo() }

// MediaItems returns the visual attachments (images and videos) of m in
// order. View-once and deleted messages have none.
func MediaItems(m core.Message) []Item {
	if m.ViewOnce || m.RemoteDeleted || m.Kind != core.KindNormal {
		return nil
	}
	var out []Item
	for _, a := range m.Attachments {
		if IsVisual(a) {
			out = append(out, Item{Message: m, Attachment: a})
		}
	}
	return out
}

// opTimeout bounds backend calls made from commands.
const opTimeout = 30 * time.Second

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// clean sanitizes untrusted single-line text.
func clean(s string) string { return theme.SanitizeLine(s) }

// width measures a rendered string in cells.
func width(s string) int { return ansi.StringWidth(s) }

// fit pads or cuts s (which may contain ANSI) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := width(s)
	if n > w {
		s = ansi.Truncate(s, w, "")
		n = width(s)
	}
	if n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// ellipsis shortens plain or styled text to w cells, ending in "…".
func ellipsis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return format.Truncate(s, w)
}

// center pads s on both sides to w cells.
func center(s string, w int) string {
	n := width(s)
	if n >= w {
		return ansi.Truncate(s, w, "")
	}
	l := (w - n) / 2
	return strings.Repeat(" ", l) + s + strings.Repeat(" ", w-n-l)
}

// fitLines returns exactly h lines of at most w cells (padding with blank
// lines, dropping extra ones).
func fitLines(lines []string, w, h int) []string {
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out = append(out, fit(lines[i], w))
		} else {
			out = append(out, strings.Repeat(" ", max(w, 0)))
		}
	}
	return out
}

// kindLabel names the kind of an attachment ("Photo", "GIF", …).
func kindLabel(a core.Attachment) string {
	switch {
	case a.GIF || a.ContentType == "image/gif":
		return "GIF"
	case a.IsImage():
		return "Photo"
	case a.IsVideo():
		return "Video"
	case a.VoiceNote:
		return "Voice message"
	case a.IsAudio():
		return "Audio"
	}
	return "File"
}

// kindIcon returns the icon for an attachment.
func kindIcon(ic theme.IconSet, a core.Attachment) string {
	switch {
	case a.IsImage():
		return ic.Image
	case a.IsVideo():
		return ic.Video
	case a.VoiceNote:
		return ic.Voice
	case a.IsAudio():
		return ic.Audio
	}
	return ic.File
}

// isAnimatedType reports whether the attachment may be animated.
func isAnimatedType(a core.Attachment) bool {
	return a.GIF || a.ContentType == "image/gif" || a.ContentType == "image/webp" || a.ContentType == "image/apng"
}

// humanSize formats a byte count ("1.2 MB").
func humanSize(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.0f kB", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}

// senderName is the display name of a message's author.
func senderName(m core.Message) string {
	if m.Outgoing {
		return "You"
	}
	if n := clean(m.SenderName); strings.TrimSpace(n) != "" {
		return n
	}
	return "Unknown"
}

// timeFormatter returns the formatter matching the config.
func timeFormatter(cfg config.Config) format.TimeFormatter {
	return format.TimeFormatter{Hour12: cfg.TimeFormat == config.Time12h}
}

// dateLine formats a message date for footers ("Tue 14 Oct 2026, 12:41").
func dateLine(cfg config.Config, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Mon 2 Jan 2006") + ", " + timeFormatter(cfg).Clock(t.Local())
}

// fileName returns a safe base name for saving an attachment.
func fileName(it Item) string {
	name := strings.TrimSpace(it.Attachment.FileName)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		case r == '/' || r == '\\' || r == ':':
			return '_'
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ". ")
	if name == "" || name == "." || name == ".." {
		ts := it.Message.SentAt
		if ts.IsZero() {
			ts = time.UnixMilli(int64(it.Message.ID.Timestamp))
		}
		name = "signal-" + ts.Local().Format("2006-01-02-150405")
	}
	if filepath.Ext(name) == "" {
		name += extFor(it.Attachment)
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 12 {
			ext = ""
		}
		name = strings.ToValidUTF8(name[:200-len(ext)], "") + ext
	}
	return name
}

// extFor guesses a file extension for an attachment's content type.
func extFor(a core.Attachment) string {
	switch a.ContentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/aac":
		return ".aac"
	case "audio/mpeg":
		return ".mp3"
	case "application/pdf":
		return ".pdf"
	}
	if exts, _ := mime.ExtensionsByType(a.ContentType); len(exts) > 0 {
		return sanitizeExt(exts[0])
	}
	return ""
}

// sanitizeExt keeps short alphanumeric extensions only.
func sanitizeExt(ext string) string {
	if len(ext) > 10 || len(ext) < 2 || ext[0] != '.' {
		return ""
	}
	for _, r := range ext[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

// isKittyRows reports whether rendered rows are kitty Unicode placeholders.
func isKittyRows(rows []string) bool {
	for _, r := range rows {
		if strings.ContainsRune(r, termimg.PlaceholderRune) {
			return true
		}
	}
	return false
}

// kittyID extracts the kitty image id encoded in the foreground color of a
// placeholder row (see termimg.KittyEncoder.Placeholder). ok is false when
// the row is not a placeholder row.
func kittyID(row string) (uint32, bool) {
	if !strings.HasPrefix(row, "\x1b[38;") {
		return 0, false
	}
	end := strings.IndexByte(row, 'm')
	if end < 0 {
		return 0, false
	}
	parts := strings.Split(row[len("\x1b[38;"):end], ";")
	num := func(s string) (uint32, bool) {
		var n uint32
		if s == "" || len(s) > 3 {
			return 0, false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + uint32(c-'0')
		}
		return n, n <= 255
	}
	var id uint32
	switch {
	case len(parts) == 2 && parts[0] == "5":
		v, ok := num(parts[1])
		if !ok {
			return 0, false
		}
		id = v
	case len(parts) == 4 && parts[0] == "2":
		for _, p := range parts[1:] {
			v, ok := num(p)
			if !ok {
				return 0, false
			}
			id = id<<8 | v
		}
	default:
		return 0, false
	}
	// Optional most significant byte: the fourth combining mark of a cell.
	rs := []rune(row[end+1:])
	if len(rs) >= 4 && rs[0] == termimg.PlaceholderRune && rs[3] != termimg.PlaceholderRune {
		if v := termimg.DiacriticValue(rs[3]); v > 0 {
			id |= uint32(v) << 24
		}
	}
	return id, id != 0
}

// styledBox renders a cols x rows box with a centered label, used for
// videos and media that cannot be shown inline.
func styledBox(st *theme.Styles, cols, rows int, label string) []string {
	p := st.Theme.Palette
	bg := lipgloss.NewStyle().Background(p.BgAlt).Foreground(p.FgMuted)
	if st.Theme.Mono {
		bg = lipgloss.NewStyle()
	}
	out := make([]string, rows)
	for i := range out {
		text := ""
		if i == rows/2 {
			text = ellipsis(label, cols)
		}
		out[i] = bg.Render(center(text, cols))
	}
	return out
}

// blurRows renders a blurhash as half-blocks (cols x rows cells), or nil.
func blurRows(hash string, cols, rows int) []string {
	if hash == "" || cols <= 0 || rows <= 0 || cols > 400 || rows > 200 {
		return nil
	}
	img, err := termimg.DecodeBlurHash(hash, min(cols, 64), min(rows*2, 64), 1)
	if err != nil {
		return nil
	}
	return termimg.HalfBlockRenderer{Quality: termimg.Fast}.Render(img, cols, rows)
}

// toast is a shorthand for ui.Toast.
func toast(level ui.ToastLevel, text string) tea.Cmd { return ui.Toast(level, text) }

// errText formats an error for a toast without leaking control characters.
func errText(op string, err error) string {
	return op + ": " + clean(err.Error())
}

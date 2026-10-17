package stories

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // image.DecodeConfig for story photos
	_ "image/jpeg" // image.DecodeConfig for story photos
	_ "image/png"  // image.DecodeConfig for story photos
	"net/http"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// MaxMediaSize caps the file size accepted by ":story post <path>".
const MaxMediaSize = 100 << 20

func init() {
	ui.RegisterAction(keymap.ActStories, func(ctx ui.Context) tea.Cmd {
		return ui.Push(NewList(ctx))
	})
	ui.RegisterCommand(ui.Command{
		Name:     "story",
		Aliases:  []string{"stories"},
		Args:     "[view|new|post <path>]",
		Help:     "Stories: list, view, compose, or post a photo/video",
		Complete: completeStory,
		Run:      runStory,
	})
}

// runStory implements ":story [view|new|post <path>]".
func runStory(ctx ui.Context, args []string) tea.Cmd {
	if len(args) == 0 {
		return ui.Push(NewList(ctx))
	}
	switch strings.ToLower(args[0]) {
	case "list", "ls":
		return ui.Push(NewList(ctx))
	case "view", "play":
		return ui.Push(NewViewer(ctx))
	case "new", "text", "compose":
		return ui.Push(NewPost(ctx))
	case "post", "media":
		path := strings.TrimSpace(strings.Join(args[1:], " "))
		if path == "" {
			return ui.Push(NewPost(ctx))
		}
		return func() tea.Msg {
			att, err := LoadMedia(path)
			if err != nil {
				return ui.ToastMsg{Level: ui.ToastError, Text: err.Error()}
			}
			return ui.PushOverlayMsg{Overlay: NewMediaPost(ctx, att)}
		}
	}
	return ui.Toast(ui.ToastWarning, "usage: :story [view|new|post <path>]")
}

// completeStory completes the subcommand and, for "post", file paths.
func completeStory(_ ui.Context, args []string) []string {
	if len(args) <= 1 {
		return []string{"view", "new", "post"}
	}
	if strings.ToLower(args[0]) != "post" {
		return nil
	}
	paths := completePath(strings.Join(args[1:], " "))
	out := append([]string{}, paths...)
	// Also offer "post <path>" so paths with spaces complete as a whole.
	for _, p := range paths {
		out = append(out, "post "+p)
	}
	return out
}

// completePath completes a filesystem path (directories end in "/").
func completePath(word string) []string {
	exp := expandHome(word)
	dir, base := filepath.Split(exp)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	shownDir, _ := filepath.Split(word)
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(n, base) {
			continue
		}
		if e.IsDir() {
			n += "/"
		}
		out = append(out, shownDir+n)
		if len(out) >= 50 {
			break
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// sniffFallback maps extensions of common camera formats that content
// sniffing does not recognize to their media types.
var sniffFallback = map[string]string{
	".heic": "image/heic",
	".heif": "image/heif",
	".mov":  "video/quicktime",
	".m4v":  "video/x-m4v",
}

// LoadMedia reads a photo or video for a media story. The content type is
// sniffed from the bytes; anything that is not an image or a video is
// rejected. Image dimensions are filled in when they can be decoded.
func LoadMedia(path string) (core.OutgoingAttachment, error) {
	p := expandHome(strings.TrimSpace(path))
	fi, err := os.Stat(p)
	if err != nil {
		return core.OutgoingAttachment{}, fmt.Errorf("story: %w", err)
	}
	if fi.IsDir() {
		return core.OutgoingAttachment{}, fmt.Errorf("story: %s is a directory", filepath.Base(p))
	}
	if fi.Size() > MaxMediaSize {
		return core.OutgoingAttachment{}, fmt.Errorf("story: %s is too large (max %d MB)", filepath.Base(p), MaxMediaSize>>20)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return core.OutgoingAttachment{}, fmt.Errorf("story: %w", err)
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if ct == "application/octet-stream" {
		// http.DetectContentType does not know HEIC or QuickTime (iPhone
		// photos and videos); trust the extension for those only.
		if alt, ok := sniffFallback[strings.ToLower(filepath.Ext(p))]; ok {
			ct = alt
		}
	}
	if !strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "video/") {
		return core.OutgoingAttachment{}, fmt.Errorf("story: %s is not a photo or video (%s)", filepath.Base(p), ct)
	}
	att := core.OutgoingAttachment{FileName: filepath.Base(p), ContentType: ct, Data: data}
	if strings.HasPrefix(ct, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			att.Width, att.Height = cfg.Width, cfg.Height
		}
	}
	return att, nil
}

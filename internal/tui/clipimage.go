package tui

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/ui"
)

// errNoClipboardImage means the clipboard holds no image.
var errNoClipboardImage = errors.New("no image on the clipboard")

// clipboardImageMsg carries a pasted image (PNG bytes, in memory only).
type clipboardImageMsg struct{ data []byte }

// readClipboardImageFn is swapped out in tests.
var readClipboardImageFn = readClipboardImage

// readClipboardImage returns the clipboard image as PNG bytes. Terminals
// can only paste text, so the image is read from the OS clipboard directly:
// osascript on macOS, wl-paste or xclip elsewhere. Nothing touches disk.
func readClipboardImage() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		// Prints «data PNGf89504E47…» (hex) or fails when there is no image.
		out, err := exec.CommandContext(ctx, "osascript", "-e", "the clipboard as «class PNGf»").Output()
		if err != nil {
			return nil, errNoClipboardImage
		}
		return parseAppleScriptData(out)
	default:
		for _, argv := range [][]string{
			{"wl-paste", "--no-newline", "--type", "image/png"},
			{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
		} {
			if _, err := exec.LookPath(argv[0]); err != nil {
				continue
			}
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
			if err == nil && isPNG(out) {
				return out, nil
			}
		}
		return nil, errNoClipboardImage
	}
}

// parseAppleScriptData decodes osascript's «data PNGf<hex>» output.
func parseAppleScriptData(out []byte) ([]byte, error) {
	s := strings.TrimSpace(string(out))
	const prefix = "«data PNGf"
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, "»") {
		return nil, errNoClipboardImage
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, prefix), "»")
	data, err := hex.DecodeString(s)
	if err != nil || !isPNG(data) {
		return nil, errNoClipboardImage
	}
	return data, nil
}

func isPNG(b []byte) bool {
	return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n"))
}

// clipboardPaste pastes the clipboard into the composer: an image becomes
// an attachment, anything else is pasted as text.
func clipboardPaste() tea.Msg {
	if data, err := readClipboardImageFn(); err == nil {
		return clipboardImageMsg{data: data}
	}
	return clipboardTextPaste()
}

// pasteImageIfAny handles a terminal paste that carried no text, which is
// what some terminals send when the clipboard only holds an image.
func pasteImageIfAny() tea.Msg {
	data, err := readClipboardImageFn()
	if err != nil {
		return nil
	}
	return clipboardImageMsg{data: data}
}

// stagePastedImage adds an in-memory image to the composer.
func (a *App) stagePastedImage(data []byte) tea.Cmd {
	if !a.conv.open {
		return a.toast(ui.ToastWarning, "Open a chat first")
	}
	if int64(len(data)) > maxAttachmentSize {
		return a.toast(ui.ToastError, "paste: "+errTooLarge.Error())
	}
	name := "pasted-image.png"
	if n := len(a.comp.attachments); n > 0 {
		name = "pasted-image-" + strconv.Itoa(n+1) + ".png"
	}
	a.comp.attachments = append(a.comp.attachments, stagedFile{name: name, size: int64(len(data)), data: data})
	a.focusComposer()
	return nil
}

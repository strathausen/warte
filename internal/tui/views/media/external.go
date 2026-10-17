package media

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

// tempFileTTL is how long a decrypted temp copy handed to an external
// program stays on disk.
const tempFileTTL = 2 * time.Minute

// Indirections for tests.
var (
	lookPath = exec.LookPath
	// startDetached starts an external program without a terminal (its
	// output is discarded so it can never draw over the TUI).
	startDetached = func(name string, args ...string) error {
		cmd := exec.Command(name, args...) //nolint:gosec // user-configured opener
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	// copyImage puts PNG bytes on the system clipboard.
	copyImage = copyPNGToClipboard
	// downloadsDir is where "s" saves by default.
	downloadsDir = func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			return "."
		}
		d := filepath.Join(home, "Downloads")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
		return home
	}
	// removeAfter deletes a temp file after d (or at StopAll, whichever
	// comes first, so a decrypted copy never outlives the process).
	removeAfter = func(path string, d time.Duration) {
		pendingMu.Lock()
		pendingTemp[path] = struct{}{}
		pendingMu.Unlock()
		time.AfterFunc(d, func() { removePending(path) })
	}
)

// pendingTemp holds decrypted temp copies handed to external programs
// that are still waiting for their removeAfter timer.
var (
	pendingMu   sync.Mutex
	pendingTemp = map[string]struct{}{}
)

func removePending(path string) {
	_ = os.Remove(path)
	pendingMu.Lock()
	delete(pendingTemp, path)
	pendingMu.Unlock()
}

// removeAllPending deletes every pending temp copy (on exit).
func removeAllPending() {
	pendingMu.Lock()
	paths := make([]string, 0, len(pendingTemp))
	for p := range pendingTemp {
		paths = append(paths, p)
	}
	pendingMu.Unlock()
	for _, p := range paths {
		removePending(p)
	}
}

// warnedOpen makes the "decrypted copy on disk" warning appear once per run.
var warnedOpen atomic.Bool

// writeTemp writes data to a fresh 0600 temp file with the given extension
// (dropped if it is not a plain one or names a file type that runs code).
func writeTemp(data []byte, ext string) (string, error) {
	ext = sanitizeExt(ext)
	if ui.RiskyExtension(ext) {
		ext = ""
	}
	f, err := os.CreateTemp("", "warte-*"+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Chmod(0o600)
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return "", errors.New("cannot write temp file")
	}
	return path, nil
}

// openerKind maps an attachment to a config.OpenWith key.
func openerKind(it Item) string {
	a := it.Attachment
	switch {
	case a.IsImage():
		return config.OpenImage
	case a.IsVideo():
		return config.OpenVideo
	case a.IsAudio():
		return config.OpenAudio
	}
	return config.OpenFile
}

// openExternal fetches an attachment, writes it to a 0600 temp file, opens
// it with the configured program and deletes the file after tempFileTTL.
func openExternal(ctx ui.Context, it Item) tea.Cmd {
	if it.Message.ViewOnce {
		return toast(ui.ToastWarning, "View-once media can only be viewed inside warte")
	}
	opener := strings.Fields(ctx.Config.Opener(openerKind(it)))
	if len(opener) == 0 {
		return toast(ui.ToastError, "No opener configured")
	}
	ext := sanitizeExt(filepath.Ext(fileName(it)))
	if ui.RiskyExtension(ext) {
		return toast(ui.ToastWarning, ui.RiskyOpenText)
	}
	svc := ctx.Svc
	if svc == nil {
		return toast(ui.ToastError, "Open: not connected")
	}
	return func() tea.Msg {
		c, cancel := opCtx()
		defer cancel()
		data, err := svc.Attachment(c, it.Message.ID, it.Attachment.ID)
		if err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Open", err)}
		}
		path, err := writeTemp(data, ext)
		if err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Open", err)}
		}
		removeAfter(path, tempFileTTL)
		if err := startDetached(opener[0], append(opener[1:], path)...); err != nil {
			_ = os.Remove(path)
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Open", err)}
		}
		if !warnedOpen.Swap(true) {
			return ui.ToastMsg{Level: ui.ToastWarning, Text: "Opened in " + clean(filepath.Base(opener[0])) + ". A decrypted copy exists on disk for 2 minutes."}
		}
		return ui.ToastMsg{Level: ui.ToastInfo, Text: "Opened in " + clean(filepath.Base(opener[0]))}
	}
}

// copyCmd copies an image attachment to the system clipboard (as PNG).
func copyCmd(ctx ui.Context, it Item) tea.Cmd {
	switch {
	case it.Message.ViewOnce:
		return toast(ui.ToastWarning, "View-once media can't be copied")
	case it.Message.ExpiresIn > 0:
		return toast(ui.ToastWarning, "Disappearing media can't be copied")
	case !it.Attachment.IsImage():
		return toast(ui.ToastInfo, "Only images can be copied")
	}
	svc := ctx.Svc
	if svc == nil {
		return toast(ui.ToastError, "Copy: not connected")
	}
	return func() tea.Msg {
		c, cancel := opCtx()
		defer cancel()
		data, err := svc.Attachment(c, it.Message.ID, it.Attachment.ID)
		if err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Copy", err)}
		}
		img, err := termimg.DecodeImage(data)
		if err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Copy", err)}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Copy", err)}
		}
		if err := copyImage(buf.Bytes()); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Copy", err)}
		}
		return ui.ToastMsg{Level: ui.ToastSuccess, Text: "Image copied to the clipboard"}
	}
}

// copyPNGToClipboard writes PNG data to the system clipboard with the
// platform's tool: osascript on macOS (via a 0600 temp file that is removed
// right away), wl-copy or xclip elsewhere (via stdin).
func copyPNGToClipboard(data []byte) error {
	run := func(name string, stdin []byte, args ...string) error {
		cmd := exec.Command(name, args...) //nolint:gosec // fixed tool names
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			return errors.New(name + " timed out")
		}
	}
	switch runtime.GOOS {
	case "darwin":
		path, err := writeTemp(data, ".png")
		if err != nil {
			return err
		}
		defer os.Remove(path)
		script := `set the clipboard to (read (POSIX file "` + strings.ReplaceAll(path, `"`, `\"`) + `") as «class PNGf»)`
		return run("osascript", nil, "-e", script)
	case "windows":
		return errors.New("copying images is not supported on Windows")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if p, err := lookPath("wl-copy"); err == nil {
			return run(p, data, "--type", "image/png")
		}
	}
	if p, err := lookPath("xclip"); err == nil {
		return run(p, data, "-selection", "clipboard", "-t", "image/png", "-i")
	}
	return errors.New("no clipboard tool found (install wl-clipboard or xclip)")
}

// expandHome expands a leading "~/" in p.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// tildePath abbreviates the home directory in p as "~".
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}

// errExists reports that the target file exists and was not overwritten.
var errExists = errors.New("file exists")

// writeFile saves data at path with mode 0644. Without overwrite an
// existing file is left alone and errExists returned.
func writeFile(path string, data []byte, overwrite bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errExists
		}
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

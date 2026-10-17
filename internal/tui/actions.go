package tui

import (
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// tempFileTTL is how long an attachment opened externally stays on disk.
const tempFileTTL = 2 * time.Minute

// messageText is the plain text of a message as displayed.
func (a *App) messageText(m core.Message) string {
	if m.RemoteDeleted {
		return ""
	}
	t := format.PlainText(format.Spans(m.Body, m.Ranges, a.mentionName))
	if t == "" && m.Kind == core.KindSystem {
		t = m.SystemText
	}
	return t
}

// copyMessages yanks message text to a register (and the clipboard for the
// default/clipboard registers) via OSC 52 plus the native clipboard.
func (a *App) copyMessages(msgs []core.Message, reg rune) tea.Cmd {
	if len(msgs) == 0 {
		return nil
	}
	var parts []string
	for _, m := range msgs {
		if m.ViewOnce || m.ExpiresIn > 0 {
			return a.toast(ui.ToastWarning, "Disappearing and view-once messages can't be copied")
		}
		t := a.messageText(m)
		if len(msgs) > 1 {
			name := m.SenderName
			if m.Outgoing {
				name = "You"
			}
			t = "[" + a.tf.Clock(m.SentAt) + "] " + name + ": " + t
		}
		parts = append(parts, t)
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return a.toast(ui.ToastInfo, "Nothing to copy")
	}
	return a.yank(text, reg, plural(len(msgs), "message")+" copied")
}

func (a *App) yank(text string, reg rune, okText string) tea.Cmd {
	a.regs.Yank(reg, text)
	if reg != 0 && reg != keymap.RegUnnamed && !keymap.IsClipboard(reg) {
		return a.toast(ui.ToastSuccess, okText+" to register "+string(reg))
	}
	return tea.Batch(
		tea.Raw(ansi.SetSystemClipboard(text)),
		func() tea.Msg {
			_ = clipboard.WriteAll(text) // best effort; OSC 52 covers remote terminals
			return infoMsg{level: ui.ToastSuccess, text: okText}
		},
	)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

// messageURLs lists the links in a message (body links, then preview).
func messageURLs(m core.Message) []string {
	var out []string
	for _, l := range format.FindLinks(m.Body) {
		out = append(out, l.URL)
	}
	if m.Preview != nil && m.Preview.URL != "" {
		dup := false
		for _, u := range out {
			if u == m.Preview.URL {
				dup = true
			}
		}
		if !dup {
			out = append(out, m.Preview.URL)
		}
	}
	return out
}

func (a *App) copyLink(reg rune) tea.Cmd {
	m, cmd := a.requireSelected()
	if m == nil {
		return cmd
	}
	if m.ViewOnce || m.ExpiresIn > 0 {
		return a.toast(ui.ToastWarning, "Disappearing and view-once messages can't be copied")
	}
	urls := messageURLs(*m)
	if len(urls) == 0 {
		return a.toast(ui.ToastInfo, "No link in this message")
	}
	return a.yank(urls[0], reg, "Link copied")
}

// openURLOf opens the first link of a message with the configured opener.
func (a *App) openURLOf(m core.Message) tea.Cmd {
	urls := messageURLs(m)
	if len(urls) == 0 {
		return a.toast(ui.ToastInfo, "No link in this message")
	}
	return a.openURL(urls[0])
}

func (a *App) openURL(u string) tea.Cmd {
	if h := ui.LinkHandlerFor(); h != nil {
		if cmd, ok := h(a.uictx, u); ok {
			return cmd
		}
	}
	opener := strings.Fields(a.cfg.Opener(config.OpenURL))
	if len(opener) == 0 {
		return nil
	}
	u, ok := safeOpenURL(u)
	if !ok {
		return a.toast(ui.ToastWarning, "Only web, mail and phone links can be opened")
	}
	return func() tea.Msg {
		cmd := exec.Command(opener[0], append(opener[1:], u)...) //nolint:gosec // user-configured opener
		if err := cmd.Start(); err != nil {
			return errMsg{op: "open link", err: err}
		}
		go func() { _ = cmd.Wait() }()
		return infoMsg{level: ui.ToastInfo, text: "Opening " + clean(u)}
	}
}

// safeOpenURL strips control characters from an untrusted URL (message
// text, a peer's link preview) and accepts only absolute http(s), mailto
// and tel URLs, so the opener is never handed a local file, an app scheme
// or something that parses as a command-line flag.
func safeOpenURL(u string) (string, bool) {
	u = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, u))
	pu, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(pu.Scheme) {
	case "http", "https":
		if pu.Host == "" {
			return "", false
		}
	case "mailto", "tel":
		if pu.Opaque == "" {
			return "", false
		}
	default:
		return "", false
	}
	return pu.String(), true
}

// messageOpen implements Enter on a message.
func (a *App) messageOpen() tea.Cmd {
	m, cmd := a.requireSelected()
	if m == nil {
		return cmd
	}
	switch {
	case m.ViewOnce && !m.Outgoing:
		if m.ViewOnceOpened {
			return a.toast(ui.ToastInfo, "Already viewed")
		}
		if len(m.Attachments) > 0 {
			if cmd := a.dispatchRegistered(ui.ActViewOnce); cmd != nil {
				return cmd
			}
			return a.pushOverlay(newImageOverlay(a, *m, m.Attachments[0], true))
		}
		return a.toast(ui.ToastInfo, "Nothing to show")
	case len(m.Attachments) > 0:
		att := m.Attachments[0]
		switch {
		case att.IsImage():
			if cmd := a.dispatchRegistered(ui.ActViewImage); cmd != nil {
				return cmd
			}
			return a.pushOverlay(newImageOverlay(a, *m, att, false))
		case att.IsAudio():
			if cmd := a.dispatchRegistered(ui.ActPlayAudio); cmd != nil {
				return cmd
			}
		}
		return a.openAttachment(*m, att)
	case m.Sticker != nil:
		return nil
	case m.Poll != nil && !m.RemoteDeleted:
		if cmd := a.dispatchRegistered(ui.ActPollVote); cmd != nil {
			return cmd
		}
		return a.toast(ui.ToastInfo, "Vote with :vote <option>")
	case m.Quote != nil && m.Quote.OriginalFound:
		return a.jumpTo(m.Quote.ID)
	case len(messageURLs(*m)) > 0:
		return a.openURLOf(*m)
	}
	return nil
}

func (a *App) openFirstAttachment(m core.Message) tea.Cmd {
	if m.ViewOnce {
		return a.toast(ui.ToastWarning, "View-once media can only be viewed inside warte")
	}
	if len(m.Attachments) == 0 {
		if len(messageURLs(m)) > 0 {
			return a.openURLOf(m)
		}
		return a.toast(ui.ToastInfo, "No attachment")
	}
	return a.openAttachment(m, m.Attachments[0])
}

// openAttachment fetches an attachment, writes it to a 0600 temp file,
// opens it with the configured handler and deletes the file later.
func (a *App) openAttachment(m core.Message, att core.Attachment) tea.Cmd {
	kind := config.OpenFile
	switch {
	case att.IsImage():
		kind = config.OpenImage
	case att.IsVideo():
		kind = config.OpenVideo
	case att.IsAudio():
		kind = config.OpenAudio
	}
	opener := strings.Fields(a.cfg.Opener(kind))
	if len(opener) == 0 {
		return a.toast(ui.ToastError, "No opener configured")
	}
	ext := filepath.Ext(att.FileName)
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(att.ContentType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	ext = sanitizeExt(ext)
	if ui.RiskyExtension(ext) {
		return a.toast(ui.ToastWarning, ui.RiskyOpenText)
	}
	warn := !a.warnedOpen
	a.warnedOpen = true
	svc := a.svc
	id := m.ID
	return func() tea.Msg {
		ctx, cancel := a.opCtx()
		defer cancel()
		data, err := svc.Attachment(ctx, id, att.ID)
		if err != nil {
			return errMsg{op: "open attachment", err: err}
		}
		f, err := os.CreateTemp(a.tempDir(), tempPattern(tempOpenPrefix, ext))
		if err != nil {
			return errMsg{op: "open attachment", err: err}
		}
		path := f.Name()
		_ = f.Chmod(0o600)
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(path)
			return errMsg{op: "open attachment", err: tuiError("cannot write temp file")}
		}
		a.tempMu.Lock()
		a.tempFiles[path] = true
		a.tempMu.Unlock()
		if a.ctx.Err() != nil {
			// The App closed (and cleaned up) while this was in flight.
			a.cleanupTemp()
			return nil
		}
		time.AfterFunc(tempFileTTL, func() {
			_ = os.Remove(path)
			a.tempMu.Lock()
			delete(a.tempFiles, path)
			a.tempMu.Unlock()
		})
		cmd := exec.Command(opener[0], append(opener[1:], path)...) //nolint:gosec // user-configured opener
		if err := cmd.Start(); err != nil {
			_ = os.Remove(path)
			return errMsg{op: "open attachment", err: err}
		}
		go func() { _ = cmd.Wait() }()
		if warn {
			return infoMsg{level: ui.ToastWarning, text: "Opened in " + filepath.Base(opener[0]) + ". A decrypted copy exists on disk for 2 minutes."}
		}
		return nil
	}
}

func sanitizeExt(ext string) string {
	if len(ext) > 10 {
		return ""
	}
	for _, r := range ext[min(1, len(ext)):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

// confirmDelete asks before deleting messages.
func (a *App) confirmDelete(msgs []core.Message, everyone bool) tea.Cmd {
	if len(msgs) == 0 {
		return nil
	}
	if everyone {
		for _, m := range msgs {
			if !m.Outgoing {
				return a.toast(ui.ToastWarning, "You can only delete your own messages for everyone")
			}
		}
	}
	title := "Delete " + plural(len(msgs), "message") + " for me?"
	text := "Removed from your devices, including your phone. Others in the chat still see " + map[bool]string{true: "it", false: "them"}[len(msgs) == 1] + ". Use dD to delete for everyone."
	if everyone {
		title = "Delete " + plural(len(msgs), "message") + " for everyone?"
		text = "Removed for everyone in the chat. They will see “This message was deleted”."
	}
	ids := make([]core.MessageID, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	svc := a.svc
	run := func() tea.Msg {
		ctx, cancel := a.opCtx()
		defer cancel()
		for _, id := range ids {
			if err := svc.Delete(ctx, id, everyone); err != nil {
				return errMsg{op: "delete", err: err}
			}
		}
		return infoMsg{level: ui.ToastSuccess, text: "Deleted " + plural(len(ids), "message")}
	}
	return a.pushOverlay(newConfirm(a, title, text, run))
}

func (a *App) confirmDeleteChat() tea.Cmd {
	c := a.targetChat()
	if c == nil {
		return nil
	}
	id, title := c.ID, clean(c.Title)
	return a.pushOverlay(newConfirm(a, "Delete chat with "+title+"?", "All messages are removed from this device.",
		a.do("delete chat", func(ctx contextT) error { return a.svc.DeleteChat(ctx, id) }, "Deleted "+title)))
}

// forward opens a chat picker and forwards msgs to the chosen chat.
func (a *App) forward(msgs []core.Message) tea.Cmd {
	if len(msgs) == 0 {
		return nil
	}
	for _, m := range msgs {
		if m.ViewOnce || m.RemoteDeleted || m.Kind != core.KindNormal {
			return a.toast(ui.ToastWarning, "This message can't be forwarded")
		}
	}
	ids := make([]core.MessageID, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if f := ui.ForwarderFor(); f != nil {
		return f(a.uictx, ids)
	}
	svc := a.svc
	title := "Forward " + plural(len(msgs), "message")
	return a.pushOverlay(newChatPicker(a, title, func(to core.ChatID) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := a.opCtx()
			defer cancel()
			for _, id := range ids {
				if err := svc.Forward(ctx, id, []core.ChatID{to}); err != nil {
					return errMsg{op: "forward", err: err}
				}
			}
			return infoMsg{level: ui.ToastSuccess, text: "Forwarded " + plural(len(ids), "message")}
		}
	}))
}

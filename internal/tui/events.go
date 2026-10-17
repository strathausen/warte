package tui

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/notify"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// handleEvent applies a backend event to the UI state.
func (a *App) handleEvent(ev core.Event) tea.Cmd {
	switch ev := ev.(type) {
	case core.MessageAdded:
		cmd := a.convAdd(ev.Message)
		a.refreshChats()
		return tea.Batch(cmd, a.notifyMessage(ev))

	case core.MessageUpdated:
		a.convUpdate(ev.Message)
		return nil

	case core.MessageRemoved:
		a.convRemove(ev.ID)
		a.refreshChats()
		return nil

	case core.ChatUpdated:
		var cmd tea.Cmd
		if a.conv.open && a.conv.chat.ID == ev.Chat.ID {
			a.conv.chat = ev.Chat
			// New messages in the chat being looked at are read right away.
			if ev.Chat.Unread > 0 && !ev.Chat.MarkedUnread {
				cmd = a.markReadIfVisible()
			}
		}
		if ev.Chat.Unread == 0 && !ev.Chat.MarkedUnread {
			a.coalescer.Clear(string(ev.Chat.ID))
		}
		a.refreshChats()
		return cmd

	case core.ChatRemoved:
		if a.conv.open && a.conv.chat.ID == ev.ID {
			a.closeChat()
		}
		a.refreshChats()
		return nil

	case core.TypingChanged:
		a.setTyping(ev)
		return nil

	case core.ConnChanged:
		a.conn = ev.State
		if ev.State == core.ConnLoggedOut && a.loggedOut == nil {
			return a.enterLoggedOut(nil)
		}
		return nil

	case core.SyncProgress:
		sp := ev
		a.sync = &sp
		if sp.Stage == "storage" && sp.Total > 0 && sp.Done >= sp.Total {
			// Chat folders come from the storage service.
			a.rebuildTabs()
		}
		if sp.Stage == "history" && sp.Total > 0 && sp.Done >= sp.Total {
			// The history transfer imports messages without MessageAdded
			// events; merge them into the open conversation.
			return a.mergeImportedHistory()
		}
		return nil

	case core.IncomingCall:
		if !ev.Ringing {
			delete(a.calls, ev.Chat)
			return nil
		}
		a.calls[ev.Chat] = ev
		if a.cfg.Notifications == config.NotifyOff || a.cfg.NotifyMethod == config.MethodOff {
			return nil
		}
		n := notify.Notification{
			Title:   clean(ev.Name),
			Body:    "Incoming call — answer on your phone",
			ChatID:  string(ev.Chat),
			Urgency: notify.UrgencyCritical,
		}
		if notifyLevel(a.cfg.Notifications) == core.NotifyNone {
			// Privacy level "none": don't reveal who is calling.
			n.Title = config.AppName
			n.Body = "Incoming call"
		}
		return notify.Cmd(a.notifier, a.lockedNotification(n))

	case core.ContactUpdated:
		if ev.Contact.IsSelf {
			a.self = ev.Contact
			a.uictx.Self = ev.Contact
		}
		// Names, verification and "about" in the header and sidebar.
		if a.conv.open {
			ctx, cancel := a.queryCtx()
			if c, err := a.svc.Chat(ctx, a.conv.chat.ID); err == nil {
				a.conv.chat = c
				a.conv.invalidate()
			}
			cancel()
		}
		a.refreshChats()
		return nil

	case core.StoriesChanged:
		return a.loadStoriesCount()

	case core.IdentityChanged:
		return a.toast(ui.ToastWarning, "Safety number with "+clean(ev.Name)+" changed")

	case core.LoggedOut:
		return a.enterLoggedOut(ev.Err)

	case core.ErrorEvent:
		if ev.Err == nil {
			return nil
		}
		return a.toast(ui.ToastError, ev.Op+": "+ev.Err.Error())
	}
	return nil
}

// enterLoggedOut switches to the logged-out state: every overlay closes and
// the registered logged-out screen (views/info, which offers relinking) is
// pushed; without one the App draws its built-in screen.
func (a *App) enterLoggedOut(err error) tea.Cmd {
	first := a.loggedOut == nil
	a.loggedOut = err
	if a.loggedOut == nil {
		a.loggedOut = tuiError("")
	}
	if !first && len(a.overlays) > 0 {
		return nil // the screen is already up
	}
	cmd := a.dismissAll()
	a.keys.SetMode(keymap.Normal)
	return tea.Batch(cmd, a.dispatchRegistered(ui.ActLoggedOut))
}

// notifyMessage raises a desktop notification for a new incoming message
// unless the user is looking at the chat.
func (a *App) notifyMessage(ev core.MessageAdded) tea.Cmd {
	if !ev.Notify || ev.Message.Outgoing {
		return nil
	}
	m := ev.Message
	chatTitle := ""
	isGroup := m.ID.Chat.IsGroup()
	muted := false
	for _, c := range a.list.chats {
		if c.ID == m.ID.Chat {
			chatTitle = c.Title
			muted = c.Muted(a.now())
		}
	}
	if chatTitle == "" {
		ctx, cancel := a.queryCtx()
		if c, err := a.svc.Chat(ctx, m.ID.Chat); err == nil {
			chatTitle = c.Title
			muted = c.Muted(a.now())
		}
		cancel()
	}
	if muted && !m.MentionsSelf {
		return nil
	}
	a.syncActiveChat()
	body := format.Snippet(m.Body, m.Ranges, a.mentionName, 200)
	if body == "" && len(m.Attachments) > 0 {
		body = attachmentLabel(a.icons, m.Attachments[0])
	}
	n, ok := a.coalescer.Add(notify.Incoming{
		ChatID:    string(m.ID.Chat),
		Sender:    m.SenderName,
		ChatTitle: chatTitle,
		Body:      body,
		IsGroup:   isGroup,
	})
	cmds := []tea.Cmd{a.coalescer.Schedule()}
	if ok {
		cmds = append(cmds, notify.Cmd(a.notifier, a.lockedNotification(n)))
	}
	return tea.Batch(cmds...)
}

// syncActiveChat tells the notification coalescer which chat the user is
// looking at.
func (a *App) syncActiveChat() {
	if a.isLocked() {
		a.coalescer.SetActiveChat("") // nobody is looking at the chat
		return
	}
	if a.conv.open && !(a.layout.narrow && a.narrowPane == FocusChatList) {
		a.coalescer.SetActiveChat(string(a.conv.chat.ID))
		return
	}
	a.coalescer.SetActiveChat("")
}

// mergeImportedHistory adds messages that appeared in the open chat without
// events (history import) from the newest page.
func (a *App) mergeImportedHistory() tea.Cmd {
	if !a.conv.open {
		return nil
	}
	ctx, cancel := a.queryCtx()
	defer cancel()
	msgs, err := a.svc.Messages(ctx, a.conv.chat.ID, core.MessageID{}, pageSize)
	if err != nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, m := range msgs {
		if _, ok := a.conv.index[m.ID]; !ok {
			cmds = append(cmds, a.convAdd(m))
		}
	}
	a.conv.invalidate()
	return tea.Batch(cmds...)
}

// Package media holds warte's media views: the full-screen image viewer,
// the view-once viewer, the per-chat media gallery, the audio/voice-note
// player and the "save to Downloads" dialog. Every view is a ui.Overlay
// themed through ui.Context.Styles; images go through ui.Context.Images
// (kitty placeholders or half-blocks), so nothing here writes to stdout.
//
// # Constructors
//
//	NewViewer(ctx, msg, att) *Viewer          viewer; prev/next walk all photos+videos of msg's chat
//	NewViewerList(ctx, items, i) *Viewer      viewer over a fixed list (album, gallery), oldest first
//	NewViewOnce(ctx, msg) *Viewer             view-once viewer (single item, no open/copy/save)
//	ForSelected(ctx, *msg) ui.Overlay         picks view-once viewer, viewer or player (nil if none)
//	NewGallery(ctx, chat) *Gallery            gallery (Media tab)
//	NewGalleryTab(ctx, chat, tab) *Gallery    gallery on TabMedia/TabFiles/TabLinks/TabVoice
//	NewPlayer(ctx, msg, att) *Player          embeddable audio player model (not an overlay)
//	NewPlayerOverlay(ctx, msg, att)           player dialog; starts playing on Init
//	NewSaveDialog(ctx, item) *SaveDialog      path prompt, writes 0644 outside the encrypted store
//	SaveCmd(ctx, item) tea.Cmd                save dialog, or a warning toast for view-once
//	MediaItems(msg) []Item                    photos/videos of a message (none for view-once)
//
// # Keys
//
// Viewer and view-once viewer — keymap context KeyContextViewer
// (= keymap.OverlayImage, "overlay:image"):
//
//   - =      zoom in (ActZoomIn)           -   zoom out (ActZoomOut)
//     0        fit (ActZoomReset)
//     h l      previous/next item; pan left/right when zoomed (ActCursorLeft/Right)
//     j k      pan down/up when zoomed; ctrl+d/u ctrl+f/b pan by half a screen
//     n N      next/previous (ActSearchNext/Prev); also p and H/L (ActFocusLeft/Right)
//     gg G     first/last item (top/bottom edge when zoomed)
//     o        open externally (0600 temp file, removed after 2 minutes)
//     y        copy image to the clipboard (as PNG; blocked for view-once/disappearing)
//     s        save… (SaveDialog; blocked for view-once)
//     m        go to the message (closes; ui.OpenChatMsg with Focus)
//     a        pause/resume GIF animation (space too, if the leader is not space)
//     enter q esc  close (view-once: marks the message viewed)
//     mouse wheel  previous/next
//
// Gallery — keymap context KeyContextGallery ("overlay:media"):
//
//	h j k l  move in the grid (lists: j/k move, h/l switch tabs)
//	tab S-tab H L gt gT  next/previous tab
//	gg G ctrl+d/u ctrl+f/b   jump / page
//	enter    Media: viewer over the gallery list · Voice: player ·
//	         Files/Links: jump to the message (closes the gallery)
//	o        open externally (files/media) or open the link (open_with.url)
//	s y      save / copy the selected attachment
//	m        go to the message
//	gm       close (ActMediaGallery toggles)
//	mouse    click a tab, click to select, click again to open, wheel scrolls
//
// Player overlay — KeyContextPlayer ("overlay:media.player"); it captures
// keys (ui.KeyCapturer) because space is the default leader:
//
//	space enter p k  play/pause      s x  stop      r 0  restart
//	o                open externally  q esc  stop and close   click  play/pause
//
// Save dialog — KeyContextSave ("overlay:media.save"), captures keys:
// enter saves (a second enter overwrites an existing file), esc cancels.
//
// # Messages emitted
//
// ui.PushOverlayMsg (viewer from gallery, player, save dialog),
// ui.CloseOverlayMsg, ui.OpenChatMsg{Chat, Focus} (go to message),
// ui.ToastMsg (results and refusals) and tea.Raw kitty animation control
// (stop/resume a kitty-animated GIF under reduce_motion or "a"). Internal
// messages (chatMediaMsg, animFramesMsg, animTickMsg, galleryPageMsg,
// savedMsg, player*Msg) come back through the App's normal routing to the
// top overlay. A Player embedded elsewhere must be fed its own messages:
// route every msg for which Player.Owns(msg) is true to Player.Update.
//
// Backend calls: Service.Messages (chat media scan, gallery pages),
// Service.Chat (gallery title), Service.Attachment (frames, open, copy,
// save, audio) and Service.MarkViewOnceOpened (view-once close).
//
// # Integration hooks
//
// init registers (blank-import the package from cmd/warte):
//
//	action "viewer.image"     (App dispatches it on enter over an image) → viewer for ctx.Selected()
//	action "viewer.viewonce"  suggested: dispatch on enter over view-once media instead of the
//	                          built-in overlay (the built-in one marks on open, this one on close)
//	action "player.audio"     suggested: dispatch on enter over voice notes/audio instead of
//	                          handing them to the external opener
//	action "chat.media" (gm)  gallery of ctx.Chat()
//	:media [media|files|links|voice]  (alias :gallery)   :play   :view
//
// Notes for the integrator:
//   - View-once marking happens in HandleBack / enter. If the App closes an
//     overlay without asking it (click outside), MarkViewOnceOpened is not
//     called; a ui "OnClose() tea.Cmd" hook would close that gap.
//   - Kitty animates GIFs itself (the App transmits all frames); the
//     viewer only sends stop/run animation control. With half-blocks the
//     viewer decodes and plays the frames (tick based, off under
//     reduce_motion).
//   - Player pauses by SIGSTOP/SIGCONT of the external program (unix);
//     elsewhere pause stops. The audio command is open_with.audio, else
//     afplay on macOS (mpv/ffplay preferred for Ogg/Opus, which afplay can't
//     decode), else paplay/ffplay/mpv; it runs without a terminal.
package media

// Package stories implements warte's Signal stories views: the stories
// list, the full-screen story viewer and the story composer. All three are
// ui.Overlays themed through ui.Context.Styles.
//
// # Constructors
//
//	NewList(ctx ui.Context) *List                    stories list ("<leader>s", ":story")
//	NewViewer(ctx ui.Context) *Viewer                viewer over all stories, starting at the
//	                                                 first author with unviewed stories (":story view")
//	NewPost(ctx ui.Context) *Post                    text story composer (":story new", "c" in the list)
//	NewMediaPost(ctx ui.Context, a core.OutgoingAttachment) *Post
//	                                                 photo/video story composer (":story post <path>")
//	LoadMedia(path string) (core.OutgoingAttachment, error)
//	                                                 reads and sniffs a photo/video for NewMediaPost
//
// Tunables: StoryDuration (5s per story), Presets (composer backgrounds),
// FontStyles, MaxTextStoryLength, MaxMediaSize.
//
// # List (KeyContext "stories" → keymap.Overlay("stories"))
//
// Sections "My stories" (own row plus a "New story" row), "Recent" (authors
// with unviewed stories, accent ring around the avatar) and "Viewed" (muted
// ring). Each author row shows the newest story's age and a summary.
// Keys go through the keymap engine:
//
//	j/k, gg/G, ctrl+d/u      move (keymap.ActCursor*, ActHalfPage*, ActPage*)
//	enter, l                 open the viewer on the author / the composer on "New story"
//	c, +                     new text story (raw keys; also keymap.ActChatNew)
//	R, ctrl+r                reload
//	q, esc                   close (keymap.ActBack, handled by the App)
//	mouse                    click selects, second click opens; wheel moves
//
// It reloads on core.StoriesChanged.
//
// # Viewer (KeyContext keymap.OverlayStory = "story")
//
// Progress segments for the author's stories at the top (auto-advance every
// StoryDuration; longer texts get up to twice that), an author line (avatar,
// name, age, group), the story canvas and a bottom bar with the quick
// reactions, the reply input, or for own stories the audience and reply
// count. Text stories are drawn on their own background (ARGB colors;
// gradients are blended one color per row), short texts letter-spaced
// "large", with a link pill for TextStory.URL. Media stories are drawn with
// ctx.Images (videos and non-inline protocols get a card) plus the caption.
// With Config.ReduceMotion the progress bar is not animated (one tick per
// story instead of 10 per second); stories still advance.
//
// The viewer implements ui.KeyCapturer and always takes keys raw, because
// space (the leader) and the digits (counts) are its own shortcuts:
//
//	l, →, n                  next story (then next author; closes after the last)
//	h, ←, p                  previous story
//	j, ↓, tab                next author
//	k, ↑, shift+tab          previous author
//	space                    pause / resume
//	r                        reply (inline input: enter sends, esc cancels)
//	1-6                      quick reaction (format.DefaultReactions)
//	esc, q, ctrl+c           close
//	mouse click              left third: previous, rest: next
//
// It also accepts ui.ActionMsg with keymap.ActStoryNext/ActStoryPrev,
// ActCursorDown/Up/Left/Right, ActMessageReply and ActBack.
//
// Backend calls: Service.MarkStoryViewed when an unviewed story is shown
// (best effort), Service.ReplyToStory(id, body, "") for replies and
// Service.ReplyToStory(id, "", emoji) for reactions.
//
// # Composer (KeyContext "story.post")
//
// Text field, background preset and font style, a live preview and the
// audience ("My Story" plus every group, multi-select). It implements
// ui.KeyCapturer and always takes keys raw:
//
//	typing                   edit the text (or the caption of a media story)
//	tab / shift+tab          next / previous color preset (text stories)
//	ctrl+t                   next font style (text stories)
//	enter, ctrl+s            post
//	esc, ↓                   from the text field: move to the audience list
//	j/k, ↑/↓, g/G            audience: move (↑ on the first row returns to the text)
//	space, x                 audience: toggle
//	i, a, /                  audience: back to the text field
//	esc, q                   audience: close
//	ctrl+c                   close
//
// Posting calls Service.PostStory once for "My Story" (To empty) and once
// for the selected groups (To = groups). Errors (including
// errors.ErrUnsupported from backends without story support) become error
// toasts and keep the form open; success closes it with a success toast.
//
// # Messages emitted
//
// ui.PushOverlayMsg (list → viewer / composer, ":story" commands),
// ui.ToastMsg (reply/reaction/post results, validation, load errors).
// Overlays close by returning nil from Update. Internal messages
// (storiesLoadedMsg, tickMsg, replyDoneMsg, groupsLoadedMsg, postDoneMsg)
// are routed back by the App as overlay command results.
//
// # Registration (init)
//
//	ui.RegisterAction(keymap.ActStories, …)   "<leader>s" opens the list
//	ui.RegisterCommand ":story" (alias ":stories")
//	    :story                 list
//	    :story view            viewer
//	    :story new             text composer
//	    :story post <path>     media composer (path completion)
//
// cmd/warte must blank-import this package for the registrations to happen.
//
// Suggested integrator hooks: none are required beyond the blank import.
// Optionally bind keys in "overlay.stories" for remaps, show a story ring in
// the chat list / sidebar for contacts with unviewed stories, and open the
// viewer from a KindStoryReply message's StoryContext (NewViewer then
// navigate), which the ui package has no hook for yet.
package stories

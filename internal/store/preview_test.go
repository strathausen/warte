package store

import (
	"strings"
	"testing"

	"warte/internal/core"
)

func TestPreviewText(t *testing.T) {
	names := func(a core.ACI) string {
		return map[core.ACI]string{"bob": "Bob"}[a]
	}
	img := core.Attachment{ContentType: "image/jpeg"}
	tests := []struct {
		name string
		m    core.Message
		want string
	}{
		{"text", core.Message{Body: "hello\n  world"}, "hello world"},
		{"deleted", core.Message{Body: "x", RemoteDeleted: true}, "This message was deleted"},
		{"system", core.Message{Kind: core.KindSystem, SystemText: "Bob joined"}, "Bob joined"},
		{"missed call", core.Message{Kind: core.KindCall, Call: &core.CallInfo{Incoming: true, Missed: true}}, "📞 Missed voice call"},
		{"group video call", core.Message{Kind: core.KindCall, Call: &core.CallInfo{Video: true, Group: true}}, "📹 Outgoing group video call"},
		{"photo", core.Message{Attachments: []core.Attachment{img}}, "📷 Photo"},
		{"photo caption", core.Message{Body: "look", Attachments: []core.Attachment{img}}, "📷 Photo · look"},
		{"album", core.Message{Attachments: []core.Attachment{img, img}}, "📷 Album"},
		{"video", core.Message{Attachments: []core.Attachment{{ContentType: "video/mp4"}}}, "🎥 Video"},
		{"gif", core.Message{Attachments: []core.Attachment{{ContentType: "video/mp4", GIF: true}}}, "🎞️ GIF"},
		{"voice", core.Message{Attachments: []core.Attachment{{ContentType: "audio/aac", VoiceNote: true}}}, "🎤 Voice message"},
		{"audio", core.Message{Attachments: []core.Attachment{{ContentType: "audio/mpeg"}}}, "🔊 Audio"},
		{"file", core.Message{Attachments: []core.Attachment{{ContentType: "application/pdf", FileName: "a.pdf"}}}, "📎 a.pdf"},
		{"sticker", core.Message{Sticker: &core.Sticker{Emoji: "😺"}}, "😺 Sticker"},
		{"poll", core.Message{Poll: &core.Poll{Question: "Lunch?"}}, "📊 Poll: Lunch?"},
		{"contact", core.Message{Contact: &core.SharedContact{Name: "Ann"}}, "👤 Ann"},
		{"view once", core.Message{ViewOnce: true, Attachments: []core.Attachment{img}}, "📷 View-once media"},
		{"mention", core.Message{Body: "hi ￼!", Ranges: []core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "bob"}}}, "hi @Bob!"},
		{"unknown mention", core.Message{Body: "￼", Ranges: []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "zz"}}}, "@Unknown"},
		{"spoiler", core.Message{Body: "the end: he dies", Ranges: []core.BodyRange{{Start: 9, Length: 7, Style: core.StyleSpoiler}}}, "the end: ■■■■"},
		{"emoji offsets", core.Message{Body: "😀 ￼", Ranges: []core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "bob"}}}, "😀 @Bob"},
		{"truncate", core.Message{Body: strings.Repeat("a", 500)}, strings.Repeat("a", previewMaxRunes-1) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PreviewText(tt.m, names); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	m := core.Message{ID: core.MessageID{Chat: "g:x", Author: "bob", Timestamp: 1}, SenderName: "B", Body: "x",
		SentAt: t0, Status: core.StatusNone}
	p := Preview(m, func(core.ACI) string { return "Bob" })
	if p.Sender != "Bob" || p.Outgoing || p.Text != "x" || !p.At.Equal(t0) || p.ID != m.ID {
		t.Errorf("incoming preview = %+v", p)
	}
	if p := Preview(m, nil); p.Sender != "B" {
		t.Errorf("nil resolver sender = %q", p.Sender)
	}
	m.Outgoing, m.Status = true, core.StatusRead
	p = Preview(m, func(core.ACI) string { return "Me" })
	if p.Sender != "" || !p.Outgoing || p.Status != core.StatusRead {
		t.Errorf("outgoing preview = %+v", p)
	}
}

func TestMediaFlagsAndUnread(t *testing.T) {
	tests := []struct {
		m     core.Message
		flags int
	}{
		{core.Message{Body: "plain"}, 0},
		{core.Message{Body: "see https://x"}, flagLink},
		{core.Message{Preview: &core.LinkPreview{}}, flagLink},
		{core.Message{Attachments: []core.Attachment{{ContentType: "image/png"}, {ContentType: "video/mp4"}}}, flagImage | flagVideo},
		{core.Message{Attachments: []core.Attachment{{ContentType: "audio/ogg"}, {ContentType: ""}}}, flagAudio | flagFile},
	}
	for i, tt := range tests {
		if got := mediaFlags(tt.m); got != tt.flags {
			t.Errorf("#%d flags = %b, want %b", i, got, tt.flags)
		}
	}
	unread := []struct {
		m    core.Message
		want bool
	}{
		{core.Message{}, true},
		{core.Message{Outgoing: true}, false},
		{core.Message{RemoteDeleted: true}, false},
		{core.Message{Kind: core.KindSystem}, false},
		{core.Message{Kind: core.KindStoryReply}, true},
		{core.Message{Kind: core.KindCall, Call: &core.CallInfo{Incoming: true, Missed: true}}, true},
		{core.Message{Kind: core.KindCall, Call: &core.CallInfo{Incoming: true}}, false},
	}
	for i, tt := range unread {
		if got := countsAsUnread(tt.m); got != tt.want {
			t.Errorf("#%d countsAsUnread = %v", i, got)
		}
	}
}

func TestGroupCallPreviewUsesSystemText(t *testing.T) {
	started := core.Message{Kind: core.KindCall, SystemText: "Alice started a group call", Call: &core.CallInfo{Group: true, Incoming: true}}
	if got := PreviewText(started, nil); got != "📞 Alice started a group call" {
		t.Errorf("started: %q", got)
	}
	missed := started
	missed.Call = &core.CallInfo{Group: true, Incoming: true, Missed: true}
	if got := PreviewText(missed, nil); got != "📞 Missed group voice call" {
		t.Errorf("missed: %q", got)
	}
}

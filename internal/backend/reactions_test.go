package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

func reactionFixture(t *testing.T) (*Backend, core.MessageID) {
	t.Helper()
	b, clock := newTestBackend(t)
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(uint64(clock.Now().UnixMilli()), "reaction target")))
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: uint64(clock.Now().UnixMilli())}
	drain(t, b)
	return b, id
}

func awaitReactionUpdate(t *testing.T, b *Backend) core.Message {
	t.Helper()
	select {
	case event := <-b.Events():
		if update, ok := event.(core.MessageUpdated); ok {
			return update.Message
		}
		t.Fatalf("unexpected event %T", event)
	case <-time.After(time.Second):
		t.Fatal("reaction update did not arrive before send completed")
	}
	return core.Message{}
}

func ownEmoji(message core.Message) string {
	for _, reaction := range message.Reactions {
		if reaction.From == selfACI {
			return reaction.Emoji
		}
	}
	return ""
}

func TestReactionImmediateAndRollback(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
		after  string
		fail   bool
		cancel bool
	}{
		{name: "add", after: "👍"},
		{name: "failed add", after: "👍", fail: true},
		{name: "failed replacement", before: "❤️", after: "👍", fail: true},
		{name: "failed removal", before: "❤️", fail: true},
		{name: "cancelled replacement", before: "❤️", after: "👍", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, id := reactionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.before != "" {
				if _, err := b.db.SetReaction(ctx, id, core.Reaction{From: selfACI, Emoji: test.before, At: b.now()}); err != nil {
					t.Fatal(err)
				}
			}
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			done := make(chan error, 1)
			sendErr := errors.New("send failed")
			go func() {
				done <- b.reactLocally(ctx, id, core.Reaction{From: selfACI, Emoji: test.after, At: b.now()}, func(core.Reaction) error {
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if test.fail {
						return sendErr
					}
					return nil
				})
			}()
			if got := ownEmoji(awaitReactionUpdate(t, b)); got != test.after {
				t.Fatalf("immediate reaction = %q, want %q", got, test.after)
			}
			if got := ownEmoji(mustMessage(t, b, id)); got != test.after {
				t.Fatalf("stored reaction = %q", got)
			}
			if _, err := b.db.SetReaction(context.Background(), id, core.Reaction{From: bobACI, Emoji: "🎉", At: b.now()}); err != nil {
				t.Fatal(err)
			}
			if test.cancel {
				cancel()
			} else {
				release <- struct{}{}
			}
			select {
			case err := <-done:
				if test.cancel && !errors.Is(err, context.Canceled) || test.fail && !errors.Is(err, sendErr) || !test.cancel && !test.fail && err != nil {
					t.Fatalf("send error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("reaction did not finish")
			}
			want := test.after
			if test.fail || test.cancel {
				want = test.before
				if got := ownEmoji(awaitReactionUpdate(t, b)); got != want {
					t.Fatalf("rollback event = %q, want %q", got, want)
				}
			}
			message := mustMessage(t, b, id)
			if ownEmoji(message) != want {
				t.Fatalf("final reaction = %q, want %q", ownEmoji(message), want)
			}
			found := false
			for _, reaction := range message.Reactions {
				found = found || reaction.From == bobACI && reaction.Emoji == "🎉"
			}
			if !found {
				t.Fatal("concurrent reaction lost")
			}
		})
	}
}

func TestReactionFailurePreservesIncomingState(t *testing.T) {
	for _, scenario := range []string{"replacement", "removal", "deletion"} {
		t.Run(scenario, func(t *testing.T) {
			b, id := reactionFixture(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			done := make(chan error, 1)
			failure := errors.New("send failed")
			go func() {
				done <- b.reactLocally(context.Background(), id, core.Reaction{From: selfACI, Emoji: "👍", At: b.now()}, func(core.Reaction) error {
					<-release
					return failure
				})
			}()
			awaitReactionUpdate(t, b)
			if scenario == "deletion" {
				if err := b.Delete(context.Background(), id, false); err != nil {
					t.Fatal(err)
				}
			} else {
				emoji := "❤️"
				if scenario == "removal" {
					emoji = "👍"
				}
				mustHandle(t, b, chatEvent(selfUUID, aliceUUID.String(), &signalpb.DataMessage{
					Timestamp: proto.Uint64(id.Timestamp + 1),
					Reaction: &signalpb.DataMessage_Reaction{
						Emoji: proto.String(emoji), Remove: proto.Bool(scenario == "removal"),
						TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(id.Timestamp),
					},
				}))
			}
			release <- struct{}{}
			if err := <-done; !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if scenario == "deletion" {
				if _, err := b.Message(context.Background(), id); !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("deleted message restored: %v", err)
				}
			} else {
				want := "❤️"
				if scenario == "removal" {
					want = ""
				}
				if got := ownEmoji(mustMessage(t, b, id)); got != want {
					t.Fatalf("incoming state overwritten: got %q, want %q", got, want)
				}
			}
		})
	}
}

func TestReactionOwnSyncKeepsNewerIntent(t *testing.T) {
	b, id := reactionFixture(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	firstTime := msTime(id.Timestamp + 1)
	go func() {
		firstDone <- b.reactLocally(context.Background(), id, core.Reaction{From: selfACI, Emoji: "👍", At: firstTime}, func(core.Reaction) error {
			<-release
			return nil
		})
	}()
	awaitReactionUpdate(t, b)
	go func() {
		secondDone <- b.reactLocally(context.Background(), id, core.Reaction{From: selfACI, Emoji: "❤️", At: msTime(id.Timestamp + 2)}, func(core.Reaction) error { return nil })
	}()
	awaitReactionUpdate(t, b)
	mustHandle(t, b, chatEvent(selfUUID, aliceUUID.String(), &signalpb.DataMessage{
		Timestamp: proto.Uint64(id.Timestamp + 1),
		Reaction: &signalpb.DataMessage_Reaction{
			Emoji: proto.String("👍"), TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(id.Timestamp),
		},
	}))
	if got := ownEmoji(mustMessage(t, b, id)); got != "❤️" {
		t.Fatalf("self sync replaced newer intent: %q", got)
	}
	release <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if got := ownEmoji(mustMessage(t, b, id)); got != "❤️" {
		t.Fatalf("final reaction = %q", got)
	}
}

func TestReactionOverlappingSends(t *testing.T) {
	for _, firstFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "first succeeds", true: "both fail"}[firstFails], func(t *testing.T) {
			b, id := reactionFixture(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			failure := errors.New("send failed")
			go func() {
				firstDone <- b.reactLocally(context.Background(), id, core.Reaction{From: selfACI, Emoji: "👍", At: b.now()}, func(core.Reaction) error {
					<-release
					if firstFails {
						return failure
					}
					return nil
				})
			}()
			awaitReactionUpdate(t, b)
			go func() {
				secondDone <- b.reactLocally(context.Background(), id, core.Reaction{From: selfACI, Emoji: "❤️", At: b.now()}, func(core.Reaction) error { return failure })
			}()
			if got := ownEmoji(awaitReactionUpdate(t, b)); got != "❤️" {
				t.Fatalf("second reaction not immediate: %q", got)
			}
			release <- struct{}{}
			<-firstDone
			if err := <-secondDone; !errors.Is(err, failure) {
				t.Fatal(err)
			}
			want := "👍"
			if firstFails {
				want = ""
			}
			if got := ownEmoji(mustMessage(t, b, id)); got != want {
				t.Fatalf("final reaction = %q, want %q", got, want)
			}
		})
	}
}

package backend

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"warte/internal/core"
)

type reactionChange struct {
	previous   core.Reaction
	current    core.Reaction
	done       chan struct{}
	superseded bool
	parent     *reactionChange
}

func (b *Backend) reactLocally(ctx context.Context, id core.MessageID, reaction core.Reaction, send func(core.Reaction) error) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	b.wg.Add(1)
	b.mu.Unlock()
	defer b.wg.Done()
	b.reactionMu.Lock()
	if b.reactions == nil {
		b.reactions = make(map[core.MessageID]*reactionChange)
	}
	previous := b.reactions[id]
	change := &reactionChange{previous: core.Reaction{From: reaction.From}, current: reaction, done: make(chan struct{}), parent: previous}
	var removed core.Reaction
	updated, err := b.db.UpdateMessage(ctx, id, func(message *core.Message) error {
		if message.RemoteDeleted || message.Kind == core.KindSystem {
			return core.ErrReadOnly
		}
		for _, existing := range message.Reactions {
			if existing.From == reaction.From {
				change.previous = existing
			}
		}
		removed = change.previous
		if pending := b.reactions[id]; pending != nil && !pending.superseded {
			change.previous = pending.previous
		}
		replaceReaction(message, reaction)
		return nil
	})
	if err != nil {
		b.reactionMu.Unlock()
		return err
	}
	b.reactions[id] = change
	b.emit(core.MessageUpdated{Message: updated})
	b.reactionMu.Unlock()

	if previous != nil {
		<-previous.done
	}
	err = ctx.Err()
	if err == nil {
		err = send(removed)
	}
	b.reactionMu.Lock()
	defer b.reactionMu.Unlock()
	defer close(change.done)
	if b.reactions[id] != change {
		if pending := b.reactions[id]; pending != nil && !pending.superseded && !change.superseded && err == nil {
			pending.previous = reaction
		}
		return err
	}
	delete(b.reactions, id)
	if err == nil || change.superseded {
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
	defer cancel()
	var superseded = errors.New("reaction superseded")
	updated, rollbackErr := b.db.UpdateMessage(cleanup, id, func(message *core.Message) error {
		if message.RemoteDeleted {
			return superseded
		}
		current := core.Reaction{From: reaction.From}
		for _, existing := range message.Reactions {
			if existing.From == reaction.From {
				current = existing
			}
		}
		if current.Emoji != change.current.Emoji || !current.At.Equal(change.current.At) && current.Emoji != "" {
			return superseded
		}
		replaceReaction(message, change.previous)
		return nil
	})
	if rollbackErr == nil {
		b.emit(core.MessageUpdated{Message: updated})
	} else if !errors.Is(rollbackErr, superseded) && !errors.Is(rollbackErr, core.ErrNotFound) {
		b.log.Warn().Err(rollbackErr).Msg("Failed to restore reaction after send failure")
	}
	return err
}

func replaceReaction(message *core.Message, reaction core.Reaction) {
	message.Reactions = slices.DeleteFunc(message.Reactions, func(existing core.Reaction) bool {
		return existing.From == reaction.From
	})
	if reaction.Emoji != "" {
		message.Reactions = append(message.Reactions, reaction)
		sort.SliceStable(message.Reactions, func(first, second int) bool {
			return message.Reactions[first].At.Before(message.Reactions[second].At)
		})
	}
	if len(message.Reactions) == 0 {
		message.Reactions = nil
	}
}

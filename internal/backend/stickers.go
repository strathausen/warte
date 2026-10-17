package backend

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// packFromManifest converts a decrypted sticker pack manifest.
func packFromManifest(id, key string, m *signalpb.Pack, installed bool) core.StickerPack {
	conv := func(s *signalpb.Pack_Sticker) core.Sticker {
		return core.Sticker{
			PackID: id, PackKey: key, StickerID: s.GetId(), Emoji: s.GetEmoji(),
			Attachment: core.Attachment{
				ID:          stickerRecordID(id, s.GetId()),
				ContentType: s.GetContentType(),
				Borderless:  true,
			},
		}
	}
	p := core.StickerPack{ID: id, Key: key, Title: m.GetTitle(), Author: m.GetAuthor(), Installed: installed}
	for _, s := range m.GetStickers() {
		if s != nil {
			p.Stickers = append(p.Stickers, conv(s))
		}
	}
	if c := m.GetCover(); c != nil {
		cs := conv(c)
		p.Cover = &cs
	} else if len(p.Stickers) > 0 {
		cs := p.Stickers[0]
		p.Cover = &cs
	}
	return p
}

// decodePackRef validates and decodes hex pack ID (16 bytes) and key (32).
func decodePackRef(packID, packKey string) (id, key []byte, err error) {
	id, err = hex.DecodeString(strings.TrimSpace(packID))
	if err != nil || len(id) != 16 {
		return nil, nil, fmt.Errorf("%w: sticker pack id must be 32 hex digits", errBadID)
	}
	key, err = hex.DecodeString(strings.TrimSpace(packKey))
	if err != nil || len(key) != 32 {
		return nil, nil, fmt.Errorf("%w: sticker pack key must be 64 hex digits", errBadID)
	}
	return id, key, nil
}

// fetchStickerManifest downloads a pack's manifest and stores the pack.
func (b *Backend) fetchStickerManifest(ctx context.Context, packID, packKey string, installed bool) error {
	rawID, rawKey, err := decodePackRef(packID, packKey)
	if err != nil {
		return err
	}
	m, err := guard("download sticker pack", func() (*signalpb.Pack, error) {
		return signalmeow.DownloadStickerPackManifest(ctx, rawID, rawKey)
	})
	if err != nil {
		return fmt.Errorf("backend: download sticker pack: %w", err)
	}
	pack := packFromManifest(hex.EncodeToString(rawID), hex.EncodeToString(rawKey), m, installed)
	if err := b.db.PutStickerPack(ctx, pack); err != nil {
		return err
	}
	if pack.Cover != nil {
		b.goBackground(func() { _, _ = b.StickerImage(b.ctx, pack.ID, pack.Cover.StickerID) })
	}
	return nil
}

// rememberStickerPack records the pack of a received sticker so its other
// stickers can be browsed and installed later.
func (b *Backend) rememberStickerPack(ctx context.Context, s *core.Sticker) {
	if s == nil || s.PackID == "" || s.PackKey == "" || strings.Trim(s.PackID, "0") == "" {
		return
	}
	if _, err := b.db.GetStickerPack(ctx, s.PackID); err == nil {
		return
	} else if !errors.Is(err, core.ErrNotFound) {
		return
	}
	if err := b.db.PutStickerPack(ctx, core.StickerPack{ID: s.PackID, Key: s.PackKey}); err != nil {
		return
	}
	b.goBackground(func() { _ = b.fetchStickerManifest(b.ctx, s.PackID, s.PackKey, false) })
}

// InstallStickerPack installs (or removes) a sticker pack and syncs the
// change to our other devices.
func (b *Backend) InstallStickerPack(ctx context.Context, packID, packKey string, install bool) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	rawID, rawKey, err := decodePackRef(packID, packKey)
	if err != nil {
		if install || packKey != "" {
			return err
		}
		// Removing only needs the ID; use the stored key.
		p, gerr := b.db.GetStickerPack(ctx, strings.ToLower(packID))
		if gerr != nil {
			return gerr
		}
		if rawID, rawKey, err = decodePackRef(p.ID, p.Key); err != nil {
			return err
		}
	}
	id := hex.EncodeToString(rawID)
	if install {
		if err := b.fetchStickerManifest(ctx, id, hex.EncodeToString(rawKey), true); err != nil {
			return err
		}
	} else if err := b.db.SetStickerPackInstalled(ctx, id, false); err != nil {
		return err
	}
	b.queueStorage(stickerPackStorageOp(rawID, rawKey, install))
	return b.sendSync(ctx, cli, syncStickerPack(rawID, rawKey, install))
}

package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/ui"
)

// ProfileEditor edits your own profile: given and family name, about text
// and emoji, and the avatar (a file path; "remove" deletes it). Only changed
// fields are sent with Service.UpdateProfile.
type ProfileEditor struct {
	*formOverlay
	orig                      profileValues
	given, family, about, emo string
	avatar                    string
}

type profileValues struct{ given, family, about, emoji string }

// NewProfileEditor returns the profile editor overlay for the local account.
func NewProfileEditor(ctx ui.Context) *ProfileEditor {
	self := ctx.Self
	if ctx.Svc != nil {
		if s := ctx.Svc.Self(); s.ACI != "" {
			self = s
		}
	}
	self.ProfileName, self.About, self.AboutEmoji = clean(self.ProfileName), clean(self.About), clean(self.AboutEmoji)
	given, family, _ := strings.Cut(strings.TrimSpace(self.ProfileName), " ")
	p := &ProfileEditor{
		orig:   profileValues{given, strings.TrimSpace(family), self.About, self.AboutEmoji},
		given:  given,
		family: strings.TrimSpace(family),
		about:  self.About,
		emo:    self.AboutEmoji,
	}
	hasAvatar := self.AvatarID != ""
	build := func() *huh.Form {
		avatarDesc := "Path to a PNG or JPEG image; empty keeps the current one"
		if hasAvatar {
			avatarDesc += ", “remove” deletes it"
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(
			huh.NewInput().Title("First name").Value(&p.given).CharLimit(26).Validate(nonEmpty("A first name")),
			huh.NewInput().Title("Last name").Description("Optional").Value(&p.family).CharLimit(26),
			huh.NewInput().Title("About").Placeholder("Write a few words about yourself").Value(&p.about).CharLimit(140),
			huh.NewInput().Title("About emoji").Description("An emoji or :shortcode:, e.g. :sparkles:").
				Value(&p.emo).CharLimit(32).Validate(func(s string) error { _, err := normalizeEmoji(s); return err }),
			huh.NewInput().Title("Avatar").Description(avatarDesc).Placeholder("~/Pictures/me.png").
				Value(&p.avatar).Validate(func(s string) error {
				if isRemove(s) {
					return nil
				}
				return validateImagePath(s)
			}),
		))
	}
	p.formOverlay = newFormOverlay(ctx, "Edit profile", 60, build, nil)
	p.submit = func() tea.Cmd {
		u, avatarPath, err := p.update()
		svc := ctx.Svc
		return p.done(func() (tea.Msg, string, error) {
			if err != nil {
				return nil, "", err
			}
			if avatarPath != "" {
				img, err := readImage(avatarPath)
				if err != nil {
					return nil, "", fmt.Errorf("avatar: %w", err)
				}
				u.Avatar = img
			}
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			if err := svc.UpdateProfile(c, u); err != nil {
				return nil, "", err
			}
			return nil, "Profile updated", nil
		})
	}
	p.footer = []string{"tab", "next", "S-tab", "back", "enter", "save", "esc", "cancel"}
	return p
}

func isRemove(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "remove" || s == "none" || s == "-"
}

// normalizeEmoji accepts "", an emoji or a :shortcode:.
func normalizeEmoji(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, ":") {
		if e, ok := format.Shortcode(s); ok {
			return e, nil
		}
		return "", errors.New("unknown shortcode")
	}
	if _, ok := format.LookupEmoji(s); ok {
		return s, nil
	}
	return "", errors.New("enter a single emoji")
}

// update computes the ProfileUpdate for the changed fields and the avatar
// file to read ("" = none).
func (p *ProfileEditor) update() (core.ProfileUpdate, string, error) {
	var u core.ProfileUpdate
	set := func(cur, orig string, dst **string) {
		cur = strings.TrimSpace(cur)
		if cur != strings.TrimSpace(orig) {
			v := cur
			*dst = &v
		}
	}
	set(p.given, p.orig.given, &u.GivenName)
	set(p.family, p.orig.family, &u.FamilyName)
	set(p.about, p.orig.about, &u.About)
	emo, err := normalizeEmoji(p.emo)
	if err != nil {
		return u, "", err
	}
	set(emo, p.orig.emoji, &u.AboutEmoji)
	path := ""
	switch a := strings.TrimSpace(p.avatar); {
	case isRemove(a):
		u.Avatar = []byte{}
	case a != "":
		path = a
	}
	return u, path, nil
}

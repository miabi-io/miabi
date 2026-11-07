// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package branding owns the install's identity, kept separate from personal accents so neither leaks into the other.
package branding

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// A handful of keys read on every sign-in, so they live in the key/value store.
const (
	KeyName         = "brand.name"
	KeyLogo         = "brand.logo_url"
	KeyLogoDark     = "brand.logo_dark_url"
	KeyAccent       = "brand.accent"
	KeyAccentPolicy = "brand.accent_policy"
	KeySigninNotice = "brand.signin_notice"
	KeyLinks        = "brand.links"
)

type AccentPolicy string

const (
	AccentPolicyDefault  AccentPolicy = "default"
	AccentPolicyEnforced AccentPolicy = "enforced"
)

// Unbounded admin input on the sign-in page is a layout break waiting to happen.
const (
	MaxLinks       = 6
	MaxLabelRunes  = 32
	MaxNoticeRunes = 600
)

var (
	// ErrTooManyLinks caps the sign-in footer.
	ErrTooManyLinks = fmt.Errorf("at most %d links may be shown on the sign-in page", MaxLinks)
	// ErrLabelTooLong keeps a label from breaking the layout.
	ErrLabelTooLong = fmt.Errorf("a link label may be at most %d characters", MaxLabelRunes)
	// ErrLabelRequired rejects a link with a URL and nothing to click.
	ErrLabelRequired = errors.New("every link needs a label")
	// A javascript: href on a page shown to unauthenticated visitors is stored XSS if an admin is compromised.
	ErrUnsupportedScheme = errors.New("a link must be an http:// or https:// URL")
	// ErrInvalidAccent rejects a brand accent the console cannot render.
	ErrInvalidAccent = errors.New("brand accent must be one of the accents the console ships")
	// ErrInvalidAccentPolicy rejects a policy other than default or enforced.
	ErrInvalidAccentPolicy = errors.New("accent policy must be one of: default, enforced")
	// ErrNoticeTooLong caps the sign-in notice.
	ErrNoticeTooLong = fmt.Errorf("the sign-in notice may be at most %d characters", MaxNoticeRunes)
)

// Link is one entry in the sign-in page footer.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Empty fields mean "use Miabi's own".
type Branding struct {
	Name    string `json:"name,omitempty"`
	LogoURL string `json:"logo_url,omitempty"`
	// LogoDarkURL is for dark grounds: the console sidebar and the dark sign-in page.
	LogoDarkURL string `json:"logo_dark_url,omitempty"`
	// FaviconURL is only ever an uploaded image, filled in by Public; never saved.
	FaviconURL   string        `json:"favicon_url,omitempty"`
	Accent       models.Accent `json:"accent,omitempty"`
	AccentPolicy AccentPolicy  `json:"accent_policy,omitempty"`
	// Rendered escaped, with line breaks kept.
	SigninNotice string `json:"signin_notice,omitempty"`
	Links        []Link `json:"links,omitempty"`
}

// AccentEnforced reports whether every account wears the brand accent.
func (b Branding) AccentEnforced() bool { return b.AccentPolicy == AccentPolicyEnforced }

type Service struct {
	repo   *repositories.SettingRepository
	assets *repositories.BrandAssetRepository
}

func NewService(repo *repositories.SettingRepository, assets *repositories.BrandAssetRepository) *Service {
	return &Service{repo: repo, assets: assets}
}

// Unusable values fall back to Miabi's identity: the sign-in page must always render.
func (s *Service) Get() Branding {
	var b Branding
	b.Name = s.value(KeyName)
	b.LogoURL = s.value(KeyLogo)
	b.LogoDarkURL = s.value(KeyLogoDark)
	if a := models.Accent(s.value(KeyAccent)); models.ValidAccent(a) {
		b.Accent = a
	}
	b.AccentPolicy, _ = ParseAccentPolicy(s.value(KeyAccentPolicy))
	b.SigninNotice = s.value(KeySigninNotice)
	if raw := s.value(KeyLinks); raw != "" {
		var links []Link
		if json.Unmarshal([]byte(raw), &links) == nil {
			b.Links = links
		}
	}
	return b
}

func (s *Service) Public() Branding {
	b := s.Get()
	assets := s.Assets()
	if a, ok := assets[AssetLogo]; ok {
		b.LogoURL = a.URL
	}
	if a, ok := assets[AssetLogoDark]; ok {
		b.LogoDarkURL = a.URL
	}
	if a, ok := assets[AssetFavicon]; ok {
		b.FaviconURL = a.URL
	}
	return b
}

func (s *Service) value(key string) string {
	set, err := s.repo.Get(key)
	if err != nil || set == nil {
		return ""
	}
	return strings.TrimSpace(set.Value)
}

// Validated on write: a bad stored value is one some future consumer renders without asking.
func (s *Service) Save(in Branding) error {
	if in.Accent != "" && !models.ValidAccent(in.Accent) {
		return ErrInvalidAccent
	}
	policy, err := ParseAccentPolicy(string(in.AccentPolicy))
	if err != nil {
		return err
	}
	notice, err := NormalizeNotice(in.SigninNotice)
	if err != nil {
		return err
	}
	links, err := NormalizeLinks(in.Links)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return err
	}
	for _, logo := range []string{in.LogoURL, in.LogoDarkURL} {
		if logo = strings.TrimSpace(logo); logo != "" {
			if err := checkURL(logo); err != nil {
				return err
			}
		}
	}
	return s.repo.BulkUpsert([]models.Setting{
		{Key: KeyName, Value: strings.TrimSpace(in.Name), Type: models.SettingTypeString},
		{Key: KeyLogo, Value: strings.TrimSpace(in.LogoURL), Type: models.SettingTypeString},
		{Key: KeyLogoDark, Value: strings.TrimSpace(in.LogoDarkURL), Type: models.SettingTypeString},
		{Key: KeyAccent, Value: string(in.Accent), Type: models.SettingTypeString},
		{Key: KeyAccentPolicy, Value: string(policy), Type: models.SettingTypeString},
		{Key: KeySigninNotice, Value: notice, Type: models.SettingTypeString},
		{Key: KeyLinks, Value: string(encoded), Type: models.SettingTypeJSON},
	})
}

// Blank is the default so pre-policy installs keep letting accounts choose.
func ParseAccentPolicy(raw string) (AccentPolicy, error) {
	switch p := AccentPolicy(strings.ToLower(strings.TrimSpace(raw))); p {
	case "", AccentPolicyDefault:
		return AccentPolicyDefault, nil
	case AccentPolicyEnforced:
		return p, nil
	}
	return AccentPolicyDefault, ErrInvalidAccentPolicy
}

// Length is capped in runes so every script gets the same allowance.
func NormalizeNotice(raw string) (string, error) {
	notice := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if utf8.RuneCountInString(notice) > MaxNoticeRunes {
		return "", ErrNoticeTooLong
	}
	return notice, nil
}

func NormalizeLinks(in []Link) ([]Link, error) {
	out := make([]Link, 0, len(in))
	for _, l := range in {
		label := strings.TrimSpace(l.Label)
		raw := strings.TrimSpace(l.URL)
		if label == "" && raw == "" {
			continue // an empty row from the form is a deletion, not an error
		}
		if label == "" {
			return nil, ErrLabelRequired
		}
		if len([]rune(label)) > MaxLabelRunes {
			return nil, ErrLabelTooLong
		}
		if err := checkURL(raw); err != nil {
			return nil, err
		}
		out = append(out, Link{Label: label, URL: raw})
	}
	if len(out) > MaxLinks {
		return nil, ErrTooManyLinks
	}
	return out, nil
}

// A protocol-relative "//evil.example" parses cleanly, so the scheme must be present and known.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return ErrUnsupportedScheme
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return ErrUnsupportedScheme
	}
	if u.Host == "" {
		return ErrUnsupportedScheme
	}
	return nil
}

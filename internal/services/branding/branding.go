// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package branding owns the operator's identity on the surfaces that have no user
// yet: the name, logo, accent and links shown on the sign-in page.
//
// It is deliberately separate from a user's own appearance preference. A personal
// accent belongs to a person and follows them between browsers; a brand belongs to
// the install and is what everyone sees, including someone who has not signed in.
// Conflating them produces the two bugs this split avoids — a personal accent
// leaking onto the sign-in page, and an operator's identity being overridable by
// anyone who fancies a different colour.
package branding

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Setting keys. Values live in the platform key/value store rather than a table of
// their own: there are four of them and they are read on every sign-in.
const (
	KeyName   = "brand.name"
	KeyLogo   = "brand.logo_url"
	KeyAccent = "brand.accent"
	KeyLinks  = "brand.links"
)

// Limits on the sign-in links. The page is not a nav bar, and an unbounded list
// from an admin form is a layout break waiting to happen.
const (
	MaxLinks      = 6
	MaxLabelRunes = 32
)

var (
	// ErrTooManyLinks caps the sign-in footer.
	ErrTooManyLinks = fmt.Errorf("at most %d links may be shown on the sign-in page", MaxLinks)
	// ErrLabelTooLong keeps a label from breaking the layout.
	ErrLabelTooLong = fmt.Errorf("a link label may be at most %d characters", MaxLabelRunes)
	// ErrLabelRequired rejects a link with a URL and nothing to click.
	ErrLabelRequired = errors.New("every link needs a label")
	// ErrUnsupportedScheme is the one that matters. An admin-supplied URL is
	// attacker-influenced input the moment an admin account is compromised, and a
	// javascript: href on a page shown to unauthenticated visitors is stored XSS
	// with extra steps.
	ErrUnsupportedScheme = errors.New("a link must be an http:// or https:// URL")
	// ErrInvalidAccent rejects a brand accent the console cannot render.
	ErrInvalidAccent = errors.New("brand accent must be one of the accents the console ships")
)

// Link is one entry in the sign-in page footer.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Branding is the operator's identity. Empty fields mean "use Miabi's own", so a
// Community install and an Enterprise one that has set nothing look identical.
type Branding struct {
	Name    string        `json:"name,omitempty"`
	LogoURL string        `json:"logo_url,omitempty"`
	Accent  models.Accent `json:"accent,omitempty"`
	Links   []Link        `json:"links,omitempty"`
}

type Service struct {
	repo *repositories.SettingRepository
}

func NewService(repo *repositories.SettingRepository) *Service { return &Service{repo: repo} }

// Get reads the stored branding. A missing or unreadable value is not an error:
// the sign-in page must render whatever else happens, so anything unusable falls
// back to Miabi's own identity rather than failing the request.
func (s *Service) Get() Branding {
	var b Branding
	b.Name = s.value(KeyName)
	b.LogoURL = s.value(KeyLogo)
	if a := models.Accent(s.value(KeyAccent)); models.ValidAccent(a) {
		b.Accent = a
	}
	if raw := s.value(KeyLinks); raw != "" {
		var links []Link
		if json.Unmarshal([]byte(raw), &links) == nil {
			b.Links = links
		}
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

// Save validates and stores the branding. Validation happens on write, not only on
// render: a bad value that reaches the database is a bad value some future consumer
// renders without asking.
func (s *Service) Save(in Branding) error {
	if in.Accent != "" && !models.ValidAccent(in.Accent) {
		return ErrInvalidAccent
	}
	links, err := NormalizeLinks(in.Links)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return err
	}
	if in.LogoURL != "" {
		if err := checkURL(in.LogoURL); err != nil {
			return err
		}
	}
	return s.repo.BulkUpsert([]models.Setting{
		{Key: KeyName, Value: strings.TrimSpace(in.Name), Type: models.SettingTypeString},
		{Key: KeyLogo, Value: strings.TrimSpace(in.LogoURL), Type: models.SettingTypeString},
		{Key: KeyAccent, Value: string(in.Accent), Type: models.SettingTypeString},
		{Key: KeyLinks, Value: string(encoded), Type: models.SettingTypeJSON},
	})
}

// NormalizeLinks trims, validates and caps the list. Exported because the same
// rules apply wherever a link list arrives.
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

// checkURL enforces the scheme allow-list. Parsing is not enough on its own: a
// protocol-relative "//evil.example" parses cleanly and inherits whatever scheme
// the page was served over, so the scheme is required to be present and known.
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

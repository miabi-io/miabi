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

// Package registration owns self-service sign-up, off by default so upgraded installs never silently accept accounts.
package registration

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/services/settings"
)

var (
	// Deliberately the same answer whether off or the domain is not allowed; see Check.
	ErrClosed = errors.New("self-service registration is not available on this platform")
	// Must never reach an HTTP response; see Check.
	ErrDomainNotAllowed = errors.New("that email domain is not allowed to sign up")
	// Without mail, required verification would create accounts only an admin could activate.
	ErrVerificationUnavailable = errors.New("registration requires email verification, but this platform has no mail server configured")
)

// Narrow on purpose: registration needs the answer, not the mailer.
type Mailer interface{ IsConfigured() bool }

type Service struct {
	enabled  bool
	settings *settings.Provider
	mailer   Mailer
}

// enabled comes from the environment and is fixed for the process lifetime.
func NewService(enabled bool, s *settings.Provider, m Mailer) *Service {
	return &Service{enabled: enabled, settings: s, mailer: m}
}

// Enabled is read from MIABI_REGISTRATION_ENABLED at boot, never from the settings table.
func (s *Service) Enabled() bool { return s.enabled }

// The same setting gates login, so an unverified account cannot sign in.
func (s *Service) RequiresVerification() bool {
	return s.settings.Bool(settings.KeyRequireEmailVerification, false)
}

// Independent of Enabled: the invitation is the authorization, so it works while sign-up is closed.
func (s *Service) InvitationSignupEnabled() bool {
	return s.settings.Bool(settings.KeyInvitationSignupEnabled, true)
}

// CanSendMail reports whether the platform has a usable mail server.
func (s *Service) CanSendMail() bool {
	return s.mailer != nil && s.mailer.IsConfigured()
}

// AllowedDomains is the sign-up allow-list, empty when any domain may register.
func (s *Service) AllowedDomains() []string {
	return ParseDomains(s.settings.String(settings.KeyAllowedSignupDomains, ""))
}

// Available refuses when verification is required but mail can't be sent, which would strand new accounts.
func (s *Service) Available() error {
	if !s.Enabled() {
		return ErrClosed
	}
	if s.RequiresVerification() && !s.CanSendMail() {
		return ErrVerificationUnavailable
	}
	return nil
}

// Callers must answer ErrDomainNotAllowed exactly like success, or anyone could probe the allow-list.
func (s *Service) Check(email string) error {
	if err := s.Available(); err != nil {
		return err
	}
	allowed := s.AllowedDomains()
	if len(allowed) == 0 {
		return nil
	}
	if !DomainAllowed(email, allowed) {
		return ErrDomainNotAllowed
	}
	return nil
}

// A leading "@" or "." is tolerated because operators write both.
func ParseDomains(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		d := strings.ToLower(strings.TrimSpace(part))
		d = strings.TrimPrefix(d, "@")
		d = strings.TrimPrefix(d, ".")
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// DomainAllowed matches subdomains only on a label boundary, so "notacme.com" does not match "acme.com".
func DomainAllowed(email string, allowed []string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	got := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, d := range allowed {
		if got == d || strings.HasSuffix(got, "."+d) {
			return true
		}
	}
	return false
}

// Describe renders the allow-list for an admin-facing message.
func Describe(allowed []string) string {
	if len(allowed) == 0 {
		return "any domain"
	}
	return strings.Join(allowed, ", ")
}

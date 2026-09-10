// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registration owns self-service sign-up: whether it is open, who may
// take it, and what state a new account lands in.
//
// Sign-up is off by default and stays off until an operator turns it on in the
// environment. A self-hosted platform that upgrades into this feature must not
// silently begin accepting accounts from anyone who can reach it.
package registration

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/services/settings"
)

var (
	// ErrClosed means self-service sign-up is switched off. Deliberately the same
	// answer whether the setting is off or the caller's domain is not allowed —
	// see Check.
	ErrClosed = errors.New("self-service registration is not available on this platform")
	// ErrDomainNotAllowed reports a rejected domain to the caller of Check. It must
	// never reach an HTTP response: see Check.
	ErrDomainNotAllowed = errors.New("that email domain is not allowed to sign up")
	// ErrVerificationUnavailable refuses to open sign-up that would create accounts
	// nobody can activate: verification is required but the platform cannot send
	// mail, and only an admin can verify an address by hand.
	ErrVerificationUnavailable = errors.New("registration requires email verification, but this platform has no mail server configured")
)

// Mailer reports whether the platform can actually send email. Narrow on purpose:
// registration needs the answer, not the mailer.
type Mailer interface{ IsConfigured() bool }

type Service struct {
	enabled  bool
	settings *settings.Provider
	mailer   Mailer
}

// NewService builds the policy. enabled comes from the environment and is fixed
// for the life of the process — see Enabled.
func NewService(enabled bool, s *settings.Provider, m Mailer) *Service {
	return &Service{enabled: enabled, settings: s, mailer: m}
}

// Enabled reports whether sign-up is switched on.
//
// Read from MIABI_REGISTRATION_ENABLED at boot, never from the settings table.
func (s *Service) Enabled() bool { return s.enabled }

// RequiresVerification reports whether a new account starts unverified. The same
// setting already gates login, so an unverified account cannot sign in.
func (s *Service) RequiresVerification() bool {
	return s.settings.Bool(settings.KeyRequireEmailVerification, false)
}

// CanSendMail reports whether the platform has a usable mail server.
func (s *Service) CanSendMail() bool {
	return s.mailer != nil && s.mailer.IsConfigured()
}

// AllowedDomains is the sign-up allow-list, empty when any domain may register.
func (s *Service) AllowedDomains() []string {
	return ParseDomains(s.settings.String(settings.KeyAllowedSignupDomains, ""))
}

// Available reports whether sign-up can be offered at all, and why not.
//
// It refuses when verification is required but no mail can be sent: the account
// would be created, unable to log in, and unable to verify itself, leaving the
// user stuck and the operator with a support request. Better to keep the door
// shut than to open one that leads nowhere.
func (s *Service) Available() error {
	if !s.Enabled() {
		return ErrClosed
	}
	if s.RequiresVerification() && !s.CanSendMail() {
		return ErrVerificationUnavailable
	}
	return nil
}

// Check validates an email against the sign-up policy.
//
// ErrClosed means the platform is not accepting sign-ups at all, which is public
// (the sign-in page has to know). ErrDomainNotAllowed is different: the caller
// must answer it exactly as it answers success, because a distinguishable answer
// lets anyone probe candidate domains one at a time and read back the allow-list,
// which describes the organisation.
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

// ParseDomains splits and normalises the configured allow-list. A leading "@" or
// "." is tolerated because operators write both.
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

// DomainAllowed reports whether an address belongs to one of the allowed domains.
//
// A subdomain matches its parent — an "acme.com" allow-list admits
// "someone@mail.acme.com" — but only on a label boundary, so "notacme.com" does
// not match "acme.com".
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

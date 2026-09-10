// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registration

import (
	"errors"
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/settings"
)

func TestParseDomains(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"empty":             {"", nil},
		"single":            {"acme.com", []string{"acme.com"}},
		"comma separated":   {"acme.com,acme.co.uk", []string{"acme.com", "acme.co.uk"}},
		"spaces and commas": {" acme.com ,  acme.co.uk ", []string{"acme.com", "acme.co.uk"}},
		"newlines":          {"acme.com\nacme.co.uk", []string{"acme.com", "acme.co.uk"}},
		"uppercase":         {"ACME.com", []string{"acme.com"}},
		// Operators write these; accept both rather than silently allowing nobody.
		"at prefix":  {"@acme.com", []string{"acme.com"}},
		"dot prefix": {".acme.com", []string{"acme.com"}},
		"junk only":  {" , , ", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ParseDomains(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseDomains(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// The allow-list is a security control, so the interesting cases are the ones that
// look allowed and are not.
func TestDomainAllowed(t *testing.T) {
	allowed := []string{"acme.com", "acme.co.uk"}
	cases := map[string]struct {
		email string
		want  bool
	}{
		"exact":             {"someone@acme.com", true},
		"uppercase address": {"Someone@ACME.com", true},
		"subdomain":         {"someone@mail.acme.com", true},
		"second entry":      {"someone@acme.co.uk", true},

		// The one that matters: a suffix match without a label boundary would let
		// anyone registering notacme.com straight in.
		"suffix without boundary": {"someone@notacme.com", false},
		"prefix":                  {"someone@acme.com.evil.example", false},
		"different tld":           {"someone@acme.org", false},
		"no at":                   {"someone", false},
		"trailing at":             {"someone@", false},
		"empty":                   {"", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DomainAllowed(c.email, allowed); got != c.want {
				t.Errorf("DomainAllowed(%q) = %v, want %v", c.email, got, c.want)
			}
		})
	}
}

type fakeMailer struct{ configured bool }

func (f fakeMailer) IsConfigured() bool { return f.configured }

func TestDescribe(t *testing.T) {
	if got := Describe(nil); got != "any domain" {
		t.Errorf("Describe(nil) = %q", got)
	}
	if got := Describe([]string{"acme.com", "acme.co.uk"}); got != "acme.com, acme.co.uk" {
		t.Errorf("Describe = %q", got)
	}
}

func provider(t *testing.T, kv map[string]string) *settings.Provider {
	t.Helper()
	cache := map[string]models.Setting{}
	for k, v := range kv {
		cache[k] = models.Setting{Key: k, Value: v}
	}
	return settings.NewFixedProvider(cache)
}

// Sign-up stays shut until an operator opens it. An upgrade must not start
// accepting accounts from anyone who can reach the platform.
func TestClosedByDefault(t *testing.T) {
	s := NewService(false, provider(t, nil), fakeMailer{configured: true})
	if s.Enabled() {
		t.Error("registration is enabled with the environment unset")
	}
	if err := s.Available(); !errors.Is(err, ErrClosed) {
		t.Errorf("Available = %v, want ErrClosed", err)
	}
}

// Verification required with no mail server would create accounts that cannot log
// in and cannot verify themselves. Refuse the door rather than open one that
// leads nowhere.
func TestRefusesWhenVerificationCannotBeDelivered(t *testing.T) {
	on := map[string]string{settings.KeyRequireEmailVerification: "true"}
	if err := NewService(true, provider(t, on), fakeMailer{configured: false}).Available(); !errors.Is(err, ErrVerificationUnavailable) {
		t.Errorf("Available = %v, want ErrVerificationUnavailable", err)
	}
	if err := NewService(true, provider(t, on), fakeMailer{configured: true}).Available(); err != nil {
		t.Errorf("Available with a mailer = %v, want nil", err)
	}
	// Without verification a mailer is irrelevant.
	if err := NewService(true, provider(t, nil), fakeMailer{configured: false}).Available(); err != nil {
		t.Errorf("Available without verification = %v, want nil", err)
	}
}

// A rejected domain is reported to the caller of Check, which is trusted, and
// never conflated with the platform being shut — the handler is what has to make
// the two indistinguishable from outside.
func TestRejectedDomainIsDistinctFromClosed(t *testing.T) {
	s := NewService(true, provider(t, map[string]string{
		settings.KeyAllowedSignupDomains: "acme.com",
	}), fakeMailer{configured: true})

	if err := s.Check("someone@acme.com"); err != nil {
		t.Errorf("an allowed domain was refused: %v", err)
	}
	err := s.Check("someone@evil.example")
	if !errors.Is(err, ErrDomainNotAllowed) {
		t.Fatalf("Check = %v, want ErrDomainNotAllowed", err)
	}
	if errors.Is(err, ErrClosed) {
		t.Error("a rejected domain reported the platform as closed, which it is not")
	}
}

func TestNoAllowListAdmitsAnyone(t *testing.T) {
	s := NewService(true, provider(t, nil), fakeMailer{configured: true})
	if err := s.Check("someone@anywhere.example"); err != nil {
		t.Errorf("Check = %v, want nil with no allow-list", err)
	}
}

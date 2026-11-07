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

// The allow-list is a security control, so test cases that look allowed and are not.
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

		// Without a label boundary anyone registering notacme.com would get straight in.
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

// An upgrade must not start accepting accounts from anyone who can reach the platform.
func TestClosedByDefault(t *testing.T) {
	s := NewService(false, provider(t, nil), fakeMailer{configured: true})
	if s.Enabled() {
		t.Error("registration is enabled with the environment unset")
	}
	if err := s.Available(); !errors.Is(err, ErrClosed) {
		t.Errorf("Available = %v, want ErrClosed", err)
	}
}

// Without mail such accounts could neither log in nor verify themselves.
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

// The handler, not Check, must make rejected-domain and closed indistinguishable from outside.
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

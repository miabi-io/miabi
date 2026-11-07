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

// Package passkey runs WebAuthn ceremonies; state in Redis is single-use so a challenge can't be answered twice.
package passkey

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
)

const (
	ceremonyTTL = 5 * time.Minute
	// MaxPerUser bounds how many passkeys one account may register.
	MaxPerUser = 10
)

var (
	// ErrDisabled means the server has no public URL to bind passkeys to.
	ErrDisabled = errors.New("passkeys are not configured on this server")
	// ErrCeremony is an unknown, expired or mismatched ceremony; the browser has to start over.
	ErrCeremony = errors.New("the passkey request expired; try again")
	// ErrInvalid is any failed verification. Deliberately vague, so a response reveals nothing about which check failed.
	ErrInvalid = errors.New("this passkey could not be verified")
	// ErrTooMany refuses a registration beyond MaxPerUser.
	ErrTooMany = fmt.Errorf("an account can have at most %d passkeys", MaxPerUser)
)

// Config binds passkeys to the console's web origin.
type Config struct {
	RPID        string
	DisplayName string
	Origins     []string
}

// An explicit rpID overrides the URL host, e.g. to share passkeys with a parent domain.
func ConfigFromWebURL(webURL, rpID, displayName string, extraOrigins []string) (Config, bool) {
	u, err := url.Parse(strings.TrimSpace(webURL))
	if err != nil || u.Hostname() == "" {
		return Config{}, false
	}
	if rpID = strings.TrimSpace(rpID); rpID == "" {
		rpID = u.Hostname()
	}
	origins := []string{u.Scheme + "://" + u.Host}
	for _, o := range extraOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins = append(origins, o)
		}
	}
	return Config{RPID: rpID, DisplayName: displayName, Origins: origins}, true
}

// Service runs registration and sign-in ceremonies. A nil *Service is a server without passkeys.
type Service struct {
	wa   *webauthn.WebAuthn
	rdb  *redis.Client
	repo *repositories.PasskeyRepository
	now  func() time.Time
}

// New builds the service.
func New(cfg Config, rdb *redis.Client, repo *repositories.PasskeyRepository) (*Service, error) {
	if rdb == nil {
		return nil, errors.New("passkeys need redis for ceremony state")
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: cfg.RPID, RPDisplayName: cfg.DisplayName, RPOrigins: cfg.Origins,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Service{wa: wa, rdb: rdb, repo: repo, now: time.Now}, nil
}

// Enabled reports whether passkeys can be used.
func (s *Service) Enabled() bool { return s != nil }

type account struct {
	id          []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (a *account) WebAuthnID() []byte                         { return a.id }
func (a *account) WebAuthnName() string                       { return a.name }
func (a *account) WebAuthnDisplayName() string                { return a.displayName }
func (a *account) WebAuthnCredentials() []webauthn.Credential { return a.creds }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decodeCredentials(rows []models.Passkey) []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(rows))
	for _, r := range rows {
		var c webauthn.Credential
		if json.Unmarshal([]byte(r.Credential), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

type ceremony struct {
	UserID  uint                 `json:"uid,omitempty"`
	Handle  string               `json:"handle,omitempty"`
	Session webauthn.SessionData `json:"session"`
}

func ceremonyKey(kind, token string) string { return "miabi:passkey:" + kind + ":" + token }

func (s *Service) save(ctx context.Context, kind string, c ceremony) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := b64(raw)
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return token, s.rdb.Set(ctx, ceremonyKey(kind, token), b, ceremonyTTL).Err()
}

// take returns a ceremony and deletes it, so it can only ever be answered once.
func (s *Service) take(ctx context.Context, kind, token string) (*ceremony, error) {
	if token == "" {
		return nil, ErrCeremony
	}
	b, err := s.rdb.GetDel(ctx, ceremonyKey(kind, token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCeremony
	}
	if err != nil {
		return nil, err
	}
	var c ceremony
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, ErrCeremony
	}
	return &c, nil
}

// The options go to navigator.credentials.create; the token comes back with the result.
func (s *Service) BeginRegistration(ctx context.Context, user *models.User) (string, *protocol.CredentialCreation, error) {
	if s == nil {
		return "", nil, ErrDisabled
	}
	rows, err := s.repo.ListByUser(user.ID)
	if err != nil {
		return "", nil, err
	}
	if len(rows) >= MaxPerUser {
		return "", nil, ErrTooMany
	}
	handle := ""
	if len(rows) > 0 {
		handle = rows[0].UserHandle
	}
	id, err := base64.RawURLEncoding.DecodeString(handle)
	if handle == "" || err != nil {
		id = make([]byte, 32)
		if _, err := rand.Read(id); err != nil {
			return "", nil, err
		}
		handle = b64(id)
	}
	acct := &account{id: id, name: user.Email, displayName: user.Name, creds: decodeCredentials(rows)}
	exclude := make([]protocol.CredentialDescriptor, 0, len(acct.creds))
	for _, c := range acct.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, session, err := s.wa.BeginRegistration(acct,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		return "", nil, err
	}
	token, err := s.save(ctx, "reg", ceremony{UserID: user.ID, Handle: handle, Session: *session})
	if err != nil {
		return "", nil, err
	}
	return token, opts, nil
}

// FinishRegistration verifies the authenticator's response and stores the new passkey.
func (s *Service) FinishRegistration(ctx context.Context, user *models.User, token, name string, response []byte) (*models.Passkey, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	c, err := s.take(ctx, "reg", token)
	if err != nil {
		return nil, err
	}
	if c.UserID != user.ID {
		return nil, ErrCeremony
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, ErrInvalid
	}
	id, _ := base64.RawURLEncoding.DecodeString(c.Handle)
	rows, err := s.repo.ListByUser(user.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) >= MaxPerUser {
		return nil, ErrTooMany
	}
	acct := &account{id: id, name: user.Email, displayName: user.Name, creds: decodeCredentials(rows)}
	cred, err := s.wa.CreateCredential(acct, c.Session, parsed)
	if err != nil {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	p := &models.Passkey{
		UserID: user.ID, Name: Name(name), CredentialID: b64(cred.ID), UserHandle: c.Handle,
		Credential: string(b), Synced: cred.Flags.BackupEligible,
	}
	if err := s.repo.Create(p); err != nil {
		return nil, err
	}
	return p, nil
}

// Name normalizes a passkey label.
func Name(name string) string {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	if name == "" {
		return "Passkey"
	}
	return name
}

// BeginLogin starts a sign-in where the browser offers whichever of this server's passkeys it holds.
func (s *Service) BeginLogin(ctx context.Context) (string, *protocol.CredentialAssertion, error) {
	if s == nil {
		return "", nil, ErrDisabled
	}
	opts, session, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, err
	}
	token, err := s.save(ctx, "login", ceremony{Session: *session})
	if err != nil {
		return "", nil, err
	}
	return token, opts, nil
}

// FinishLogin verifies a sign-in assertion and returns the user it belongs to.
func (s *Service) FinishLogin(ctx context.Context, token string, response []byte) (uint, error) {
	if s == nil {
		return 0, ErrDisabled
	}
	c, err := s.take(ctx, "login", token)
	if err != nil {
		return 0, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return 0, ErrInvalid
	}
	var row *models.Passkey
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		p, err := s.repo.FindByCredentialID(b64(rawID))
		if err != nil {
			return nil, ErrInvalid
		}
		// The handle binds the credential to the account it was created for; a mismatch is a forged response.
		if p.UserHandle != b64(userHandle) {
			return nil, ErrInvalid
		}
		rows, err := s.repo.ListByUser(p.UserID)
		if err != nil {
			return nil, err
		}
		row = p
		return &account{id: userHandle, creds: decodeCredentials(rows)}, nil
	}
	_, cred, err := s.wa.ValidatePasskeyLogin(lookup, c.Session, parsed)
	if err != nil || row == nil {
		return 0, ErrInvalid
	}
	// A sign count that went backwards means a second copy of a non-synced key is in use.
	if cred.Authenticator.CloneWarning {
		return 0, ErrInvalid
	}
	if b, err := json.Marshal(cred); err == nil {
		_ = s.repo.RecordUse(row.ID, string(b), s.now())
	}
	return row.UserID, nil
}

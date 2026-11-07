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

// Package dr holds disaster-recovery primitives and depends on nothing else in Miabi: restore runs on a bare host.
package dr

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Restore refuses an unknown schema rather than guessing at fields.
const IdentitySchema = 1

// Self-describing header so a future format change is a clean refusal, not a garbled decrypt.
const (
	magic       = "MBID1"
	saltLen     = 16
	nonceLen    = 12
	keyLen      = 32
	headerLen   = len(magic) + 1 + saltLen + nonceLen
	formatVer   = 1
	minPassLen  = 12
	argonTime   = 3
	argonMemory = 64 * 1024 // KiB
	argonLanes  = 4
)

var (
	// Wrong passphrase and tampering are indistinguishable by design.
	ErrBadPassphrase = errors.New("identity envelope did not decrypt: wrong passphrase or corrupt file")
	// ErrNotEnvelope means the bytes are not an identity envelope at all.
	ErrNotEnvelope    = errors.New("not a Miabi identity envelope")
	ErrWeakPassphrase = fmt.Errorf("backup passphrase must be at least %d characters and mix letters with digits or symbols", minPassLen)
)

// Identity omits the database and Redis passwords; those belong to the new host.
type Identity struct {
	Schema int `json:"schema"`

	InstallID    string `json:"install_id"`
	MiabiVersion string `json:"miabi_version"`
	DBSchema     string `json:"db_schema,omitempty"`

	// EncryptionKey wraps every workspace data key; it is the reason this envelope exists.
	EncryptionKey string `json:"encryption_key"`
	// JWTSecret keeps issued API tokens and sessions valid across the restore.
	JWTSecret string `json:"jwt_secret"`

	Domain          string `json:"domain,omitempty"`
	WebURL          string `json:"web_url,omitempty"`
	ControlURL      string `json:"control_url,omitempty"`
	ACMEEmail       string `json:"acme_email,omitempty"`
	RegistryHost    string `json:"registry_host,omitempty"`
	RegistryStorage string `json:"registry_storage,omitempty"`

	NetworkName         string `json:"network_name,omitempty"`
	NetworkSubnet       string `json:"network_subnet,omitempty"`
	InternalNetworkName string `json:"internal_network_name,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Validate checks an opened envelope carries what restore depends on.
func (i *Identity) Validate() error {
	if i.Schema != IdentitySchema {
		return fmt.Errorf("identity envelope schema %d is not supported by this build (expected %d)", i.Schema, IdentitySchema)
	}
	if strings.TrimSpace(i.EncryptionKey) == "" {
		return errors.New("identity envelope carries no encryption key — a restore from it could not decrypt any secret")
	}
	if strings.TrimSpace(i.InstallID) == "" {
		return errors.New("identity envelope carries no install id")
	}
	return nil
}

// Enforced, not suggested: compromising this artifact hands over the whole platform.
func ValidatePassphrase(pass string) error {
	if len([]rune(pass)) < minPassLen {
		return ErrWeakPassphrase
	}
	var hasLetter, hasOther bool
	for _, r := range pass {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			hasOther = true
		}
	}
	if !hasLetter || !hasOther {
		return ErrWeakPassphrase
	}
	return nil
}

func Seal(id *Identity, passphrase string) ([]byte, error) {
	if id == nil {
		return nil, errors.New("nil identity")
	}
	if err := ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	id.Schema = IdentitySchema
	plaintext, err := json.Marshal(id)
	if err != nil {
		return nil, fmt.Errorf("marshal identity: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, headerLen+len(plaintext)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, byte(formatVer))
	out = append(out, salt...)
	out = append(out, nonce...)
	// The header is authenticated data, so editing the salt or version fails to open.
	out = gcm.Seal(out, nonce, plaintext, out[:headerLen])
	zero(plaintext)
	return out, nil
}

// A wrong passphrase and tampering look identical, or Open would be a decryption oracle.
func Open(sealed []byte, passphrase string) (*Identity, error) {
	if len(sealed) < headerLen || string(sealed[:len(magic)]) != magic {
		return nil, ErrNotEnvelope
	}
	if sealed[len(magic)] != formatVer {
		return nil, fmt.Errorf("%w: unsupported envelope format version %d", ErrNotEnvelope, sealed[len(magic)])
	}
	off := len(magic) + 1
	salt := sealed[off : off+saltLen]
	nonce := sealed[off+saltLen : headerLen]
	ct := sealed[headerLen:]

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, sealed[:headerLen])
	if err != nil {
		return nil, ErrBadPassphrase
	}
	defer zero(plaintext)

	var id Identity
	if err := json.Unmarshal(plaintext, &id); err != nil {
		return nil, fmt.Errorf("decode identity: %w", err)
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return &id, nil
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonLanes, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("identity cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("identity cipher: %w", err)
	}
	return gcm, nil
}

// Go's GC may copy the array, but leaving live keys in the heap for no reason is worse.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

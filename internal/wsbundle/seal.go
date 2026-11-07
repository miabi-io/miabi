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

package wsbundle

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// The self-describing header makes a future format change a clean refusal instead of a garbled decrypt.
const (
	magic       = "MBWS1"
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
	// A wrong passphrase and a tampered file are indistinguishable by design.
	ErrBadPassphrase = errors.New("bundle state did not decrypt: wrong passphrase or corrupt file")
	// ErrNotState means the bytes are not a bundle state file at all.
	ErrNotState = errors.New("not a Miabi workspace bundle state file")
	// ErrPassphraseRequired means the state file is sealed but no passphrase was given.
	ErrPassphraseRequired = errors.New("this bundle is encrypted: set the bundle passphrase it was created with to restore it")
	ErrWeakPassphrase     = fmt.Errorf("backup passphrase must be at least %d characters and mix letters with digits or symbols", minPassLen)
)

// A bundle holds every workspace secret, so the floor is enforced rather than suggested.
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

// A fresh salt and nonce per call, so sealing the same state twice never yields the same bytes.
func Seal(st *State, passphrase string) ([]byte, error) {
	if st == nil {
		return nil, errors.New("nil state")
	}
	if err := ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	st.Schema = StateSchema
	plaintext, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	defer zero(plaintext)

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
	// The header is authenticated so an edited salt or version fails to open rather than decrypting to nonsense.
	out = gcm.Seal(out, nonce, plaintext, out[:headerLen])
	return out, nil
}

// Wrong passphrase and tampering are reported identically; distinguishing them would be a decryption oracle.
func Open(sealed []byte, passphrase string) (*State, error) {
	if len(sealed) < headerLen || string(sealed[:len(magic)]) != magic {
		return nil, ErrNotState
	}
	if sealed[len(magic)] != formatVer {
		return nil, fmt.Errorf("%w: unsupported format version %d", ErrNotState, sealed[len(magic)])
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

	var st State
	if err := json.Unmarshal(plaintext, &st); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return &st, nil
}

// An empty passphrase writes plain JSON, carrying the workspace's secrets in the clear.
func Encode(st *State, passphrase string) ([]byte, error) {
	if passphrase != "" {
		return Seal(st, passphrase)
	}
	if st == nil {
		return nil, errors.New("nil state")
	}
	st.Schema = StateSchema
	return json.MarshalIndent(st, "", "  ")
}

// The form is read from the bytes, not the index, so it decodes whatever the workspace's current setting is.
func Decode(data []byte, passphrase string) (*State, error) {
	if len(data) >= len(magic) && string(data[:len(magic)]) == magic {
		if passphrase == "" {
			return nil, ErrPassphraseRequired
		}
		return Open(data, passphrase)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, ErrNotState
	}
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return &st, nil
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonLanes, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("bundle cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("bundle cipher: %w", err)
	}
	return gcm, nil
}

// Go's GC may still copy the array, but leaving the plaintext vault in the heap for no reason is worse.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

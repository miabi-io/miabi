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

// Package dbenvelope seals per-set data keys under the backup passphrase so the passphrase is cheaply rotatable.
package dbenvelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// Self-describing header so a future format change is a clean refusal rather than a garbled decrypt.
const (
	magic     = "MBDK1"
	formatVer = 1
	saltLen   = 16
	nonceLen  = 12
	keyLen    = 32
	headerLen = len(magic) + 1 + saltLen + nonceLen

	// Matches internal/dr so both envelopes are equally expensive to attack offline.
	argonTime   = 3
	argonMemory = 64 * 1024 // KiB
	argonLanes  = 4

	// DataKeyLen is the size of a generated data key in bytes.
	DataKeyLen = 32
)

var (
	// Wrong passphrase and tampering are indistinguishable by design, or this would be a decryption oracle.
	ErrBadPassphrase = errors.New("backup envelope did not decrypt: wrong passphrase or corrupt envelope")
	// ErrNotEnvelope means the bytes are not a data-key envelope at all.
	ErrNotEnvelope = errors.New("not a Miabi backup data-key envelope")
)

// Base64 keeps the key a single shell-safe token for use as a GPG passphrase.
func NewDataKey() (string, error) {
	raw := make([]byte, DataKeyLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate data key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func Seal(dataKey, passphrase string) (string, error) {
	if dataKey == "" {
		return "", errors.New("refusing to seal an empty data key")
	}
	if passphrase == "" {
		return "", errors.New("refusing to seal without a passphrase")
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}

	out := make([]byte, 0, headerLen+len(dataKey)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, byte(formatVer))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, []byte(dataKey), out[:headerLen])
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open is the passphrase check and runs before any artifact is fetched.
func Open(sealed, passphrase string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", ErrNotEnvelope
	}
	if len(raw) < headerLen || string(raw[:len(magic)]) != magic {
		return "", ErrNotEnvelope
	}
	if raw[len(magic)] != formatVer {
		return "", fmt.Errorf("%w: unsupported envelope format version %d", ErrNotEnvelope, raw[len(magic)])
	}
	off := len(magic) + 1
	salt := raw[off : off+saltLen]
	nonce := raw[off+saltLen : headerLen]
	ct := raw[headerLen:]

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, raw[:headerLen])
	if err != nil {
		return "", ErrBadPassphrase
	}
	defer zero(plaintext)
	return string(plaintext), nil
}

// Rewrap never touches the data key, so rotation never re-reads the encrypted artifacts.
func Rewrap(sealed, oldPassphrase, newPassphrase string) (string, error) {
	dataKey, err := Open(sealed, oldPassphrase)
	if err != nil {
		return "", err
	}
	return Seal(dataKey, newPassphrase)
}

// IsEnvelope tells a sealed set from one taken before envelopes existed.
func IsEnvelope(sealed string) bool {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	return err == nil && len(raw) >= headerLen && string(raw[:len(magic)]) == magic
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return gcm, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonLanes, keyLen)
}

// Spec documents the real envelope parameters in recovery kits so they cannot drift from the code.
type Spec struct {
	Magic         string `json:"magic"`
	FormatVersion int    `json:"format_version"`
	KDF           string `json:"kdf"`
	ArgonTime     int    `json:"argon_time"`
	ArgonMemoryKB int    `json:"argon_memory_kib"`
	ArgonLanes    int    `json:"argon_lanes"`
	KeyLen        int    `json:"key_len"`
	SaltLen       int    `json:"salt_len"`
	NonceLen      int    `json:"nonce_len"`
	HeaderLen     int    `json:"header_len"`
	Cipher        string `json:"cipher"`
}

// Format returns the parameters this build seals with.
func Format() Spec {
	return Spec{
		Magic:         magic,
		FormatVersion: formatVer,
		KDF:           "argon2id",
		ArgonTime:     argonTime,
		ArgonMemoryKB: argonMemory,
		ArgonLanes:    argonLanes,
		KeyLen:        keyLen,
		SaltLen:       saltLen,
		NonceLen:      nonceLen,
		HeaderLen:     headerLen,
		Cipher:        "AES-256-GCM",
	}
}

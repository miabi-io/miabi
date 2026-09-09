// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dbenvelope seals the data key that encrypts a database recovery point.
//
// Each set is encrypted with its own random data key, and that key is sealed here
// under the workspace's backup passphrase. The indirection is what makes the
// passphrase rotatable: re-sealing an envelope rewrites a few hundred bytes,
// whereas encrypting the artifacts with the passphrase directly would strand every
// existing set behind the secret it was taken with.
//
// The construction matches internal/dr's identity envelope — Argon2id to a key,
// AES-256-GCM over the payload, a self-describing header authenticated as
// additional data. It is a separate implementation because dr deliberately depends
// on nothing else in Miabi (its restore path runs on a bare host) and its payload
// is an Identity, not a key.
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

// Framing. The header is self-describing so a future format change is a clean
// refusal rather than a garbled decrypt.
const (
	magic     = "MBDK1"
	formatVer = 1
	saltLen   = 16
	nonceLen  = 12
	keyLen    = 32
	headerLen = len(magic) + 1 + saltLen + nonceLen

	// Argon2id cost. Matches internal/dr so the two envelopes are equally
	// expensive to attack offline.
	argonTime   = 3
	argonMemory = 64 * 1024 // KiB
	argonLanes  = 4

	// DataKeyLen is the size of a generated data key in bytes.
	DataKeyLen = 32
)

var (
	// ErrBadPassphrase means the envelope did not authenticate: a wrong
	// passphrase, or a tampered envelope. The two are indistinguishable by design
	// — saying which would make this a decryption oracle.
	ErrBadPassphrase = errors.New("backup envelope did not decrypt: wrong passphrase or corrupt envelope")
	// ErrNotEnvelope means the bytes are not a data-key envelope at all.
	ErrNotEnvelope = errors.New("not a Miabi backup data-key envelope")
)

// NewDataKey returns a fresh random data key, encoded so it can be handed to the
// backup tools as a GPG passphrase. Base64 keeps it to one shell-safe token.
func NewDataKey() (string, error) {
	raw := make([]byte, DataKeyLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate data key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Seal encrypts a data key under a key derived from passphrase, returning the
// framed envelope base64-encoded for storage in a text column. A fresh salt and
// nonce are drawn per call, so sealing the same key twice never produces the same
// bytes.
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

// Open recovers the data key from a sealed envelope. Opening IS the passphrase
// check: it costs one Argon2id derivation and happens before any artifact is
// fetched, so a wrong passphrase is rejected without downloading anything.
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

// Rewrap moves an envelope from one passphrase to another without touching the
// data key inside it, which is what makes rotation cheap: the artifacts the key
// encrypts are never re-read.
func Rewrap(sealed, oldPassphrase, newPassphrase string) (string, error) {
	dataKey, err := Open(sealed, oldPassphrase)
	if err != nil {
		return "", err
	}
	return Seal(dataKey, newPassphrase)
}

// IsEnvelope reports whether a stored value looks like one of ours, so callers can
// tell a sealed set from one taken before envelopes existed.
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

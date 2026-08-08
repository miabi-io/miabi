// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Envelope framing. The header is self-describing so a future format change is a
// clean refusal instead of a garbled decrypt.
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
	// ErrBadPassphrase means the state file did not authenticate: a wrong
	// passphrase, or a tampered file. The two are indistinguishable by design.
	ErrBadPassphrase = errors.New("bundle state did not decrypt: wrong passphrase or corrupt file")
	// ErrNotState means the bytes are not a sealed bundle state file at all.
	ErrNotState = errors.New("not a Miabi workspace bundle state file")
	// ErrWeakPassphrase is returned when a passphrase is too weak to protect a
	// workspace's entire vault.
	ErrWeakPassphrase = fmt.Errorf("backup passphrase must be at least %d characters and mix letters with digits or symbols", minPassLen)
)

// ValidatePassphrase rejects passphrases too weak for the bundle. A bundle holds
// every secret the workspace owns, so the floor is enforced rather than
// suggested.
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

// Seal encrypts a state document under a key derived from passphrase with Argon2id, returning the
// framed file. A fresh random salt and nonce are generated per call, so sealing the same state
// twice never produces the same bytes.
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
	// The header is authenticated as additional data, so a file whose salt or
	// version was edited fails to open rather than decrypting to nonsense.
	out = gcm.Seal(out, nonce, plaintext, out[:headerLen])
	return out, nil
}

// Open decrypts a sealed state file. A wrong passphrase and a tampered file are
// reported identically — the AEAD cannot tell them apart, and pretending
// otherwise would be a decryption oracle.
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

// zero wipes material we are done with. Go's GC gives no guarantee the backing
// array is not copied, but leaving a workspace's plaintext vault in a
// long-running control plane's heap for no reason is worse.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

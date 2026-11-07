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

// Package crypto provides AES-GCM at-rest encryption; without a key values are only base64 (dev only).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const (
	encryptedPrefix = "enc:"
	// The header is self-describing so Decrypt resolves the key without a workspace argument.
	wsPrefix = "e2:w:"
)

// nil means EncryptWS falls back to the master key, which is safe during rollout and in dev.
type Keyring interface {
	ActiveDEK(workspaceID uint) (version int, dek []byte, err error)
	// DEK returns a specific key version's DEK (to decrypt older ciphertext).
	DEK(workspaceID uint, version int) (dek []byte, err error)
}

var (
	key     []byte
	keyring Keyring
	keyMu   sync.RWMutex
)

// Empty secret disables encryption (dev); does not clear an injected keyring.
func Init(secret string) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if secret == "" {
		key = nil
		return
	}
	h := sha256.Sum256([]byte(secret))
	key = h[:]
}

// SetKeyring wires the per-workspace keyring (call once at startup, after Init).
func SetKeyring(kr Keyring) {
	keyMu.Lock()
	defer keyMu.Unlock()
	keyring = kr
}

// Lets the composition root reuse the live keyring instead of building a second cache.
func CurrentKeyring() Keyring {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return keyring
}

// Enabled reports whether a real encryption key is configured.
func Enabled() bool {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return key != nil
}

// IsEncrypted reports whether a stored value is AES-encrypted.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedPrefix)
}

// Decrypt would base64-decode a bare value and corrupt plaintext that happens to be valid base64.
func LooksEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedPrefix) || strings.HasPrefix(s, wsPrefix)
}

// Encrypt returns the encrypted, prefixed, base64-encoded form of plaintext.
func Encrypt(plaintext string) (string, error) {
	keyMu.RLock()
	k := key
	keyMu.RUnlock()
	if k == nil {
		return base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
	}
	ct, err := aesGCMEncrypt(k, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("crypto: failed to encrypt: %w", err)
	}
	return encryptedPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Dev only; never security-relevant since dev has no real secrets.
var devMasterKey = []byte("miabi-dev-insecure-master-key")

// DeriveToken lets every process sharing the master key derive identical internal secrets without storage.
func DeriveToken(label string) string {
	keyMu.RLock()
	k := key
	keyMu.RUnlock()
	if k == nil {
		k = devMasterKey
	}
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(label))
	return "mrt_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Disaster recovery uses it to check a backup's key; an empty secret yields "", never the dev fallback.
func DeriveTokenFrom(secret, label string) string {
	if secret == "" {
		return ""
	}
	h := sha256.Sum256([]byte(secret))
	mac := hmac.New(sha256.New, h[:])
	mac.Write([]byte(label))
	return "mrt_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Falls back to the master key when no keyring or master key is configured.
func EncryptWS(workspaceID uint, plaintext string) (string, error) {
	keyMu.RLock()
	kr := keyring
	k := key
	keyMu.RUnlock()
	if kr == nil || k == nil {
		return Encrypt(plaintext)
	}
	ver, dek, err := kr.ActiveDEK(workspaceID)
	if err != nil {
		return "", fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	ct, err := aesGCMEncrypt(dek, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("crypto: failed to encrypt: %w", err)
	}
	return fmt.Sprintf("%s%d:%d:", wsPrefix, workspaceID, ver) + base64.StdEncoding.EncodeToString(ct), nil
}

// Idempotent: a no-op without a keyring, on empty values, or when already at the active version.
func Reencrypt(workspaceID uint, stored string) (string, bool, error) {
	keyMu.RLock()
	kr := keyring
	keyMu.RUnlock()
	if kr == nil || stored == "" {
		return stored, false, nil
	}
	ver, _, err := kr.ActiveDEK(workspaceID)
	if err != nil {
		return "", false, fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	if strings.HasPrefix(stored, fmt.Sprintf("%s%d:%d:", wsPrefix, workspaceID, ver)) {
		return stored, false, nil // already at the active version
	}
	pt, err := Decrypt(stored)
	if err != nil {
		return "", false, err
	}
	enc, err := EncryptWS(workspaceID, pt)
	if err != nil {
		return "", false, err
	}
	return enc, true, nil
}

// A bare value is dev passthrough.
func Decrypt(stored string) (string, error) {
	if strings.HasPrefix(stored, wsPrefix) {
		return decryptWS(stored)
	}
	if IsEncrypted(stored) {
		keyMu.RLock()
		k := key
		keyMu.RUnlock()
		if k == nil {
			return "", fmt.Errorf("crypto: encrypted value found but no encryption key configured")
		}
		encoded := strings.TrimPrefix(stored, encryptedPrefix)
		ct, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decode ciphertext: %w", err)
		}
		pt, err := aesGCMDecrypt(k, ct)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
		}
		return string(pt), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return stored, nil
	}
	return string(decoded), nil
}

func decryptWS(stored string) (string, error) {
	keyMu.RLock()
	kr := keyring
	keyMu.RUnlock()
	if kr == nil {
		return "", fmt.Errorf("crypto: per-workspace ciphertext found but no keyring configured")
	}
	rest := strings.TrimPrefix(stored, wsPrefix)
	parts := strings.SplitN(rest, ":", 3) // <ws>, <ver>, <b64>
	if len(parts) != 3 {
		return "", fmt.Errorf("crypto: malformed per-workspace ciphertext")
	}
	ws, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("crypto: bad workspace id in ciphertext: %w", err)
	}
	ver, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", fmt.Errorf("crypto: bad key version in ciphertext: %w", err)
	}
	dek, err := kr.DEK(uint(ws), ver)
	if err != nil {
		return "", fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decode ciphertext: %w", err)
	}
	pt, err := aesGCMDecrypt(dek, ct)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
	}
	return string(pt), nil
}

func aesGCMEncrypt(k, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func aesGCMDecrypt(k, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

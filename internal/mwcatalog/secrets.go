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

package mwcatalog

import "github.com/miabi-io/miabi/internal/services/crypto"

// On update, a value equal to the sentinel means keep the stored secret.
const RedactedSentinel = "***"

// Idempotent: already-encrypted values are untouched; with encryption disabled plaintext is stored as-is.
func EncryptSecrets(mwType string, workspaceID uint, rule map[string]any) (map[string]any, error) {
	return transformSecrets(mwType, rule, func(v string) (string, error) {
		if v == "" || crypto.LooksEncrypted(v) || !crypto.Enabled() {
			return v, nil
		}
		return crypto.EncryptWS(workspaceID, v)
	})
}

// Legacy plaintext not yet re-saved is passed through untouched.
func DecryptSecrets(mwType string, rule map[string]any) (map[string]any, error) {
	return transformSecrets(mwType, rule, func(v string) (string, error) {
		if !crypto.LooksEncrypted(v) {
			return v, nil
		}
		return crypto.Decrypt(v)
	})
}

// Legacy plaintext is left alone: crypto.Reencrypt would mangle it.
func ReencryptSecrets(mwType string, workspaceID uint, rule map[string]any) (map[string]any, bool, error) {
	changed := false
	out, err := transformSecrets(mwType, rule, func(v string) (string, error) {
		if !crypto.LooksEncrypted(v) {
			return v, nil
		}
		nv, ch, err := crypto.Reencrypt(workspaceID, v)
		if err != nil {
			return "", err
		}
		changed = changed || ch
		return nv, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// Never expose ciphertext or plaintext in API responses.
func Redact(mwType string, rule map[string]any) map[string]any {
	out, _ := transformSecrets(mwType, rule, func(string) (string, error) {
		return RedactedSentinel, nil
	})
	return out
}

// Keeps a client editing a redacted rule from wiping stored secrets; basicAuth users match by username.
func MergeKeptSecrets(mwType string, incoming, existing map[string]any) map[string]any {
	d, ok := Get(mwType)
	if !ok || existing == nil {
		return incoming
	}
	out := shallowCopy(incoming)
	for _, sp := range d.secretPaths() {
		kept, hasKept := valueAt(existing, sp.keys)
		_ = updateAt(out, sp.keys, func(v any) (any, error) {
			if sp.field.Type == FieldUsers {
				return mergeKeptUsers(v, kept), nil
			}
			if s, _ := v.(string); s == RedactedSentinel && hasKept {
				return kept, nil
			}
			return v, nil
		})
	}
	return out
}

func transformSecrets(mwType string, rule map[string]any, fn func(string) (string, error)) (map[string]any, error) {
	d, ok := Get(mwType)
	if !ok || rule == nil {
		return rule, nil
	}
	out := shallowCopy(rule)
	for _, sp := range d.secretPaths() {
		err := updateAt(out, sp.keys, func(v any) (any, error) {
			if sp.field.Type == FieldUsers {
				return transformUsers(v, fn)
			}
			s, ok := v.(string)
			if !ok {
				return v, nil
			}
			return fn(s)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Copies every level it walks so the stored rule is never mutated in place.
func updateAt(root map[string]any, keys []string, fn func(any) (any, error)) error {
	node := root
	for _, k := range keys[:len(keys)-1] {
		child, ok := node[k].(map[string]any)
		if !ok {
			return nil
		}
		copied := shallowCopy(child)
		node[k] = copied
		node = copied
	}
	leaf := keys[len(keys)-1]
	v, present := node[leaf]
	if !present {
		return nil
	}
	nv, err := fn(v)
	if err != nil {
		return err
	}
	node[leaf] = nv
	return nil
}

// valueAt reads the value at keys, if every level exists.
func valueAt(root map[string]any, keys []string) (any, bool) {
	node := root
	for _, k := range keys[:len(keys)-1] {
		child, ok := node[k].(map[string]any)
		if !ok {
			return nil, false
		}
		node = child
	}
	v, ok := node[keys[len(keys)-1]]
	return v, ok
}

// Returns a fresh list so the stored rule is never mutated in place.
func transformUsers(v any, fn func(string) (string, error)) (any, error) {
	list, ok := v.([]any)
	if !ok {
		return v, nil
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		u, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		nu := shallowCopy(u)
		if pw, ok := nu["password"].(string); ok {
			np, err := fn(pw)
			if err != nil {
				return nil, err
			}
			nu["password"] = np
		}
		out = append(out, nu)
	}
	return out, nil
}

func mergeKeptUsers(incoming, existing any) any {
	inList, ok := incoming.([]any)
	if !ok {
		return incoming
	}
	exByName := map[string]string{}
	if exList, ok := existing.([]any); ok {
		for _, e := range exList {
			if u, ok := e.(map[string]any); ok {
				name, _ := u["username"].(string)
				pw, _ := u["password"].(string)
				exByName[name] = pw
			}
		}
	}
	out := make([]any, 0, len(inList))
	for _, e := range inList {
		u, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		nu := shallowCopy(u)
		if pw, _ := nu["password"].(string); pw == RedactedSentinel {
			name, _ := nu["username"].(string)
			if kept, ok := exByName[name]; ok {
				nu["password"] = kept
			}
		}
		out = append(out, nu)
	}
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

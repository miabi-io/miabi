// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mwcatalog

import "github.com/miabi-io/miabi/internal/services/crypto"

// RedactedSentinel is the placeholder returned in place of a secret value in API
// responses. On update, a secret still equal to the sentinel means "keep the
// stored value", so editing a policy without retyping its password preserves it.
const RedactedSentinel = "***"

// EncryptSecrets returns a copy of rule with every secret field encrypted at rest under the
// per-workspace key. Already-encrypted values are left untouched (idempotent, so kept secrets
// aren't double-encrypted); with encryption disabled the plaintext is stored as-is.
func EncryptSecrets(mwType string, workspaceID uint, rule map[string]any) (map[string]any, error) {
	return transformSecrets(mwType, rule, func(v string) (string, error) {
		if v == "" || crypto.LooksEncrypted(v) || !crypto.Enabled() {
			return v, nil
		}
		return crypto.EncryptWS(workspaceID, v)
	})
}

// DecryptSecrets returns a copy of rule with every secret field decrypted, for
// the render path. Only values carrying an encryption envelope are decrypted;
// legacy plaintext (pre-feature, not yet re-saved) is passed through untouched.
func DecryptSecrets(mwType string, rule map[string]any) (map[string]any, error) {
	return transformSecrets(mwType, rule, func(v string) (string, error) {
		if !crypto.LooksEncrypted(v) {
			return v, nil
		}
		return crypto.Decrypt(v)
	})
}

// Redact returns a copy of rule with every secret value replaced by the redaction
// sentinel, for API responses (never expose ciphertext or plaintext).
func Redact(mwType string, rule map[string]any) map[string]any {
	out, _ := transformSecrets(mwType, rule, func(string) (string, error) {
		return RedactedSentinel, nil
	})
	return out
}

// MergeKeptSecrets returns a copy of incoming where every secret still equal to the redaction
// sentinel is restored from existing, so a client that received a redacted rule and edited other
// fields doesn't wipe the stored secret. basicAuth users are matched by username.
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

// transformSecrets applies fn to each secret string value of a rule, returning a
// copy. It handles both simple secret string fields and the basicAuth users list
// (each user's password). Non-secret fields are copied unchanged.
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

// updateAt applies fn to the value at keys, copying every level it walks so the
// stored rule is never mutated in place. A path that does not exist is skipped.
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

// transformUsers applies fn to each user's password, returning a fresh list so
// the stored rule is never mutated in place.
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

// mergeKeptUsers restores each incoming user's password from the existing list
// (matched by username) when it is still the redaction sentinel.
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

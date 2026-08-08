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
	for _, f := range d.secretFields() {
		switch f.Type {
		case FieldUsers:
			out[f.Key] = mergeKeptUsers(out[f.Key], existing[f.Key])
		default:
			if s, _ := out[f.Key].(string); s == RedactedSentinel {
				out[f.Key] = existing[f.Key]
			}
		}
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
	for _, f := range d.secretFields() {
		v, present := out[f.Key]
		if !present {
			continue
		}
		switch f.Type {
		case FieldUsers:
			users, err := transformUsers(v, fn)
			if err != nil {
				return nil, err
			}
			out[f.Key] = users
		default:
			if s, ok := v.(string); ok {
				nv, err := fn(s)
				if err != nil {
					return nil, err
				}
				out[f.Key] = nv
			}
		}
	}
	return out, nil
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

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mwcatalog

import (
	"errors"
	"fmt"
)

// ErrInvalidRule is returned when a rule fails validation against its
// descriptor. Handlers map it to 422 MIDDLEWARE_INVALID_RULE.
var ErrInvalidRule = errors.New("invalid middleware rule")

// Validate checks a rule against a catalogued type. It returns nil for an
// uncatalogued type (the advanced escape hatch — passed through to Goma as-is).
// A catalogued type is checked for required keys, value types, and unknown keys,
// so a malformed rule is rejected before it can render into the workspace file
// and take Goma — and every route in the workspace — offline.
func Validate(mwType string, rule map[string]any) error {
	d, ok := Get(mwType)
	if !ok {
		return nil // uncatalogued: advanced passthrough
	}
	if err := validateFields(d.Fields, rule); err != nil {
		return err
	}
	// Cross-field constraints the per-field loop can't express (run last, so it
	// sees fully type-checked values).
	if d.Validate != nil {
		if err := d.Validate(rule); err != nil {
			return err
		}
	}
	return nil
}

// validateFields checks a rule map against a field set: rejects unknown keys,
// enforces required fields, and type-checks each present value. It is used for
// both the top-level rule and nested object/list sub-schemas, so a malformed
// nested value is caught with the same rigour as a top-level one.
func validateFields(fields []Field, rule map[string]any) error {
	allowed := make(map[string]Field, len(fields))
	for _, f := range fields {
		allowed[f.Key] = f
	}
	for k := range rule {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("%w: unknown field %q", ErrInvalidRule, k)
		}
	}
	for _, f := range fields {
		v, present := rule[f.Key]
		if !present || v == nil {
			if f.Required {
				return fmt.Errorf("%w: %q is required", ErrInvalidRule, f.Key)
			}
			continue
		}
		if err := validateField(f, v); err != nil {
			return err
		}
	}
	return nil
}

func validateField(f Field, v any) error {
	switch f.Type {
	case FieldString, FieldDuration:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%w: %q must be a string", ErrInvalidRule, f.Key)
		}
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%w: %q must be true or false", ErrInvalidRule, f.Key)
		}
	case FieldInt:
		if !isInt(v) {
			return fmt.Errorf("%w: %q must be a whole number", ErrInvalidRule, f.Key)
		}
	case FieldEnum:
		s, ok := v.(string)
		if !ok || !contains(f.Options, s) {
			return fmt.Errorf("%w: %q must be one of %v", ErrInvalidRule, f.Key, f.Options)
		}
	case FieldStrings:
		if !isStringSlice(v) {
			return fmt.Errorf("%w: %q must be a list of strings", ErrInvalidRule, f.Key)
		}
	case FieldInts:
		if !isIntSlice(v) {
			return fmt.Errorf("%w: %q must be a list of whole numbers", ErrInvalidRule, f.Key)
		}
	case FieldUsers:
		if err := validateUsers(f.Key, v); err != nil {
			return err
		}
	case FieldMap:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %q must be an object of string values", ErrInvalidRule, f.Key)
		}
		for k, mv := range m {
			if _, ok := mv.(string); !ok {
				return fmt.Errorf("%w: %q value for %q must be a string", ErrInvalidRule, f.Key, k)
			}
		}
	case FieldObject:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %q must be an object", ErrInvalidRule, f.Key)
		}
		// A structured object (with a sub-schema) is validated against it; a
		// free-form object (no Fields) is passed through unchecked.
		if len(f.Fields) > 0 {
			return validateFields(f.Fields, m)
		}
	case FieldList:
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%w: %q must be a list", ErrInvalidRule, f.Key)
		}
		if f.Required && len(list) == 0 {
			return fmt.Errorf("%w: %q must have at least one entry", ErrInvalidRule, f.Key)
		}
		for i, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: %q entry %d must be an object", ErrInvalidRule, f.Key, i+1)
			}
			if err := validateFields(f.Fields, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireAnyOf builds a descriptor cross-field validator asserting at least one
// of the named keys is present and non-nil — for types where every field is
// individually optional but the middleware is a no-op with none set.
func requireAnyOf(keys ...string) func(map[string]any) error {
	return func(rule map[string]any) error {
		for _, k := range keys {
			if v, ok := rule[k]; ok && v != nil {
				return nil
			}
		}
		return fmt.Errorf("%w: set at least one of %v", ErrInvalidRule, keys)
	}
}

// isIntSlice reports whether v is a list whose every element is a whole number.
func isIntSlice(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range list {
		if !isInt(e) {
			return false
		}
	}
	return true
}

// validateUsers checks the basicAuth users list: a non-empty array of objects,
// each with a username and password.
func validateUsers(key string, v any) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("%w: %q must be a non-empty list of users", ErrInvalidRule, key)
	}
	for i, e := range list {
		u, ok := e.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: user %d must be an object with username and password", ErrInvalidRule, i+1)
		}
		if s, _ := u["username"].(string); s == "" {
			return fmt.Errorf("%w: user %d is missing a username", ErrInvalidRule, i+1)
		}
		if s, _ := u["password"].(string); s == "" {
			return fmt.Errorf("%w: user %d is missing a password", ErrInvalidRule, i+1)
		}
	}
	return nil
}

// isInt accepts JSON numbers (float64) with no fractional part, as well as the
// native int kinds, since rules round-trip through JSON.
func isInt(v any) bool {
	switch n := v.(type) {
	case float64:
		return n == float64(int64(n))
	case int, int32, int64:
		return true
	default:
		return false
	}
}

func isStringSlice(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range list {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}

func contains(opts []string, s string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"reflect"
	"testing"
)

// requestBodies are the bound request DTOs whose enum tags this test guards.
// Add new ones here; the cost is one line and the failure it prevents is a
// 400 on every call to the endpoint.
var requestBodies = []any{
	UpdatePreferencesRequest{},
	SetDefaultWorkspaceRequest{},
	CreateAPIKeyRequest{},
	CreateDomainRequest{},
	UpdateDomainRequest{},
}

// TestEnumTagsAreValidatable pins a framework rule that fails loudly at runtime and
// silently at compile time: okapi's enum validator accepts a string field, or a slice
// of them, and rejects anything else — including a *string — on its KIND, before it
// ever looks at the value. So an enum tag on a pointer 400s every request to that
// endpoint, whether or not the field was sent.
//
// Optional fields still need pointers for partial-update semantics, so the rule is
// "no enum tag on a pointer"; validate those values in the service instead.
func TestEnumTagsAreValidatable(t *testing.T) {
	for _, req := range requestBodies {
		typ := reflect.TypeOf(req)
		body, ok := typ.FieldByName("Body")
		if !ok {
			continue
		}
		for i := 0; i < body.Type.NumField(); i++ {
			f := body.Type.Field(i)
			if f.Tag.Get("enum") == "" {
				continue
			}
			kind := f.Type.Kind()
			if kind == reflect.Slice {
				kind = f.Type.Elem().Kind()
			}
			if kind != reflect.String {
				t.Errorf("%s.Body.%s has an enum tag on a %s; okapi rejects it on kind, so every request to this endpoint returns 400. Drop the tag and validate in the service.",
					typ.Name(), f.Name, f.Type.Kind())
			}
		}
	}
}

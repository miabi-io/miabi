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
	CreateOAuthProviderRequest{},
	UpdateOAuthProviderRequest{},
	UpdateRegistrySettingsRequest{},
	CreatePlanRequest{},
	UpdatePlanRequest{},
	SetWorkspaceQuotaRequest{},
}

// TestEnumTagsAreValidatable pins a framework rule that fails loudly at runtime and
// silently at compile time: okapi's enum validator dereferences a pointer field but not
// the elements of a slice, so an enum tag on []*string 400s every request that sends it.
func TestEnumTagsAreValidatable(t *testing.T) {
	for _, req := range requestBodies {
		typ := reflect.TypeOf(req)
		body, ok := typ.FieldByName("Body")
		if !ok {
			continue
		}
		checkEnumFields(t, typ.Name()+".Body", body.Type)
	}
}

func checkEnumFields(t *testing.T, path string, typ reflect.Type) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			checkEnumFields(t, path, f.Type)
			continue
		}
		if f.Tag.Get("enum") == "" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() != reflect.String {
			t.Errorf("%s.%s has an enum tag on %s; okapi only validates enums on string, *string or []string, so every request sending it returns 400.",
				path, f.Name, f.Type)
		}
	}
}

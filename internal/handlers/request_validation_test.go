// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
)

func bindJSON(t *testing.T, dst any, body string) error {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return okapi.NewContext(okapi.New(), httptest.NewRecorder(), r).Bind(dst)
}

func TestRequestValidation(t *testing.T) {
	tests := []struct {
		name  string
		new   func() any
		body  string
		valid bool
	}{
		{"quota override omitted", func() any { return &SetWorkspaceQuotaRequest{} }, `{}`, true},
		{"quota unlimited", func() any { return &SetWorkspaceQuotaRequest{} }, `{"max_apps":-1}`, true},
		{"quota below unlimited", func() any { return &SetWorkspaceQuotaRequest{} }, `{"max_apps":-2}`, false},
		{"quota unknown profile", func() any { return &SetWorkspaceQuotaRequest{} }, `{"security_profile":"strict"}`, false},
		{"quota restricted profile", func() any { return &SetWorkspaceQuotaRequest{} }, `{"security_profile":"restricted"}`, true},
		{"plan embedded limit", func() any { return &UpdatePlanRequest{} }, `{"max_cpu_cores":-5}`, false},
		{"plan pointer limit", func() any { return &UpdatePlanRequest{} }, `{"max_database_memory_mb":-5}`, false},
		{"plan unknown profile", func() any { return &UpdatePlanRequest{} }, `{"security_profile":"strict"}`, false},
		{"org display name too long", func() any { return &AdminUpdateOrganizationRequest{} }, `{"display_name":"` + strings.Repeat("x", 121) + `"}`, false},
		{"org cap below unlimited", func() any { return &AdminUpdateOrganizationRequest{} }, `{"max_workspaces_per_user":-2}`, false},
		{"user limit clear", func() any { return &AdminSetWorkspaceLimitRequest{} }, `{"limit":null}`, true},
		{"user limit below unlimited", func() any { return &AdminSetWorkspaceLimitRequest{} }, `{"limit":-3}`, false},
		{"backup comment too long", func() any { return &UpdateBackupRequest{} }, `{"comment":"` + strings.Repeat("x", 201) + `"}`, false},
		{"oauth unknown role", func() any { return &UpdateOAuthProviderRequest{} }, `{"default_role":"root"}`, false},
		{"oauth clear role", func() any { return &UpdateOAuthProviderRequest{} }, `{"default_role":""}`, true},
		{"registry unknown storage", func() any { return &UpdateRegistrySettingsRequest{} }, `{"storage_type":"gcs"}`, false},
		{"bad timezone", func() any { return &UpdatePreferencesRequest{} }, `{"timezone":"Mars/Olympus"}`, false},
		{"timezone", func() any { return &UpdatePreferencesRequest{} }, `{"timezone":"Europe/Berlin"}`, true},
		{"negative key expiry", func() any { return &CreateAPIKeyRequest{} }, `{"name":"ci","expires_in_days":-1}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := bindJSON(t, tt.new(), tt.body)
			if tt.valid && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

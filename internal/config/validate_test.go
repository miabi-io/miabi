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

package config

import (
	gormlogger "gorm.io/gorm/logger"
	"os"
	"testing"
)

// prodConfig is a minimal non-dev Config with every guarded field set to a safe,
// non-default value. Each test mutates one field to assert its guard fires.
func prodConfig() *Config {
	return &Config{
		Env:           "production",
		JWTSecret:     "0123456789abcdef0123456789abcdef",
		EncryptionKey: "a-real-key",
		CORSOrigins:   "https://panel.example.com",
	}
}

func TestValidateAcceptsSafeNonDevConfig(t *testing.T) {
	if err := prodConfig().validate(); err != nil {
		t.Fatalf("safe non-dev config rejected: %v", err)
	}
}

func TestValidateAllowsInsecureDefaultsInDev(t *testing.T) {
	c := &Config{
		Env:           "dev",
		JWTSecret:     defaultJWTSecret,
		EncryptionKey: "",
		CORSOrigins:   "*",
	}
	if err := c.validate(); err != nil {
		t.Fatalf("dev config should permit defaults, got: %v", err)
	}
}

func TestValidateRejectsInsecureDefaultsInNonDev(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"default jwt secret", func(c *Config) { c.JWTSecret = defaultJWTSecret }},
		{"empty jwt secret", func(c *Config) { c.JWTSecret = "" }},
		{"short jwt secret", func(c *Config) { c.JWTSecret = "a-real-but-short-secret" }},
		{"empty encryption key", func(c *Config) { c.EncryptionKey = "" }},
		{"whitespace encryption key", func(c *Config) { c.EncryptionKey = "   " }},
		{"wildcard cors", func(c *Config) { c.CORSOrigins = "*" }},
		{"wildcard cors in list", func(c *Config) { c.CORSOrigins = "https://ok.example.com, *" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := prodConfig()
			tt.mutate(c)
			if err := c.validate(); err == nil {
				t.Fatalf("expected validate() to reject %q in non-dev, got nil", tt.name)
			}
		})
	}
}

// An unset MIABI_ENV must not skip the secret checks (H2): a manual `docker run` without it would
// otherwise boot on the public default JWT secret.
func TestUnsetEnvDefaultsToProduction(t *testing.T) {
	for _, k := range []string{"MIABI_ENV", "MIABI_JWT_SECRET", "MIABI_ENCRYPTION_KEY", "MIABI_ADMIN_PASSWORD"} {
		t.Setenv(k, "") // registers the restore; the unset below is what New sees
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	c := New()
	if c.Env != "production" {
		t.Fatalf("Env = %q with MIABI_ENV unset, want production", c.Env)
	}
	if err := c.validate(); err == nil {
		t.Fatal("default secrets accepted with MIABI_ENV unset")
	}
}

func TestHasWildcardOrigin(t *testing.T) {
	cases := map[string]bool{
		"*":                            true,
		" * ":                          true,
		"https://a.com, *":             true,
		"https://a.com":                false,
		"https://a.com, https://b.com": false,
		"":                             false,
	}
	for in, want := range cases {
		if got := hasWildcardOrigin(in); got != want {
			t.Errorf("hasWildcardOrigin(%q) = %v, want %v", in, got, want)
		}
	}
}

// The storage driver is environment-only, so a typo has no UI to correct it:
// "S3 " or "minio" would silently fall back to a local volume while the operator
// believes images are going to object storage. Refuse the boot instead.
func TestValidateRegistryStorageDriver(t *testing.T) {
	cases := []struct {
		driver string
		ok     bool
	}{
		{"", true},
		{"filesystem", true},
		{"s3", true},
		{"S3", true},
		{" s3 ", true},
		{"minio", false},
		{"gcs", false},
		{"local", false},
	}
	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			c := prodConfig()
			c.Registry.StorageType = tc.driver
			err := c.validate()
			if tc.ok && err != nil {
				t.Fatalf("MIABI_REGISTRY_STORAGE=%q rejected: %v", tc.driver, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("MIABI_REGISTRY_STORAGE=%q accepted, want a boot refusal", tc.driver)
			}
		})
	}
}

// The seeded admin's credentials are not a boot requirement: an install that already has an admin
// starts without them.
func TestValidateIgnoresAdminCredentials(t *testing.T) {
	c := prodConfig()
	c.AdminEmail, c.AdminPassword = "", ""
	if err := c.validate(); err != nil {
		t.Fatalf("validate() rejected a config without admin credentials: %v", err)
	}
}

func TestValidateAdminSeed(t *testing.T) {
	tests := []struct {
		name, env, email, password string
		ok                         bool
	}{
		{"real credentials", "production", "admin@acme.test", "a-real-password", true},
		{"default password", "production", "admin@acme.test", defaultAdminPassword, false},
		{"empty password", "production", "admin@acme.test", "", false},
		{"empty email", "production", "", "a-real-password", false},
		{"not an email", "production", "admin", "a-real-password", false},
		{"default password in dev", "dev", "admin@acme.test", defaultAdminPassword, true},
		{"empty email in dev", "dev", "", defaultAdminPassword, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Env: tt.env, AdminEmail: tt.email, AdminPassword: tt.password}
			if err := c.ValidateAdminSeed(); (err == nil) != tt.ok {
				t.Fatalf("ValidateAdminSeed() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestDBLogLevelFollowsLogLevel(t *testing.T) {
	for _, tc := range []struct {
		dev   bool
		value string
		want  gormlogger.LogLevel
	}{
		{false, "", gormlogger.Warn},
		{true, "", gormlogger.Info},
		{false, "debug", gormlogger.Info},
		{false, "info", gormlogger.Warn},
		{false, "warn", gormlogger.Warn},
		{false, "error", gormlogger.Error},
		{false, "nonsense", gormlogger.Error},
	} {
		c := &Config{DevMode: tc.dev, LogLevel: tc.value}
		if got := c.DBLogLevel(); got != tc.want {
			t.Errorf("dev=%v MIABI_LOG_LEVEL=%q: got %d, want %d", tc.dev, tc.value, got, tc.want)
		}
	}
}

func TestDoNotTrack(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true, "yes": true} {
		if got := doNotTrack(v); got != want {
			t.Errorf("doNotTrack(%q) = %v, want %v", v, got, want)
		}
	}
}

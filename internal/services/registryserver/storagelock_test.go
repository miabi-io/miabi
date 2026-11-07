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

package registryserver

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
)

type fakeEE map[string]bool

func (f fakeEE) Has(flag string) bool { return f[flag] }

func licensed() fakeEE   { return fakeEE{enterprise.FlagRegistryS3: true} }
func unlicensed() fakeEE { return fakeEE{} }

func TestStorageFollowsEnvOnlyWhenSet(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		stored     string
		want       string
		wantSource string
	}{
		{"env s3 wins over stored filesystem", "s3", "filesystem", "s3", "env"},
		{"env filesystem wins over stored s3", "filesystem", "s3", "filesystem", "env"},
		{"stored s3 survives an unset env", "", "s3", "s3", "stored"},
		{"nothing configured is filesystem", "", "", "filesystem", "default"},
		{"an unknown env driver falls back to filesystem", "gcs", "s3", "filesystem", "env"},
		{"an unknown stored driver falls back to filesystem", "", "gcs", "filesystem", "default"},
		{"case is not significant", "S3", "", "s3", "env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{cfg: config.RegistryConfig{StorageType: tc.env}}
			st := &models.RegistrySettings{StorageType: tc.stored}
			svc.applyEnvConfig(st)
			if st.StorageType != tc.want {
				t.Errorf("StorageType = %q, want %q", st.StorageType, tc.want)
			}
			if got := svc.StorageSource(st); got != tc.wantSource {
				t.Errorf("StorageSource = %q, want %q", got, tc.wantSource)
			}
		})
	}
}

func TestS3FieldsFollowEnv(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{
		StorageType: "s3",
		S3Bucket:    "env-bucket",
		S3Region:    "eu-west-1",
		S3ForcePath: true,
	}}
	st := &models.RegistrySettings{
		StorageType: "filesystem",
		S3Bucket:    "stored-bucket",
		S3Endpoint:  "https://minio.internal",
		S3AccessKey: "stored-key",
	}
	svc.applyEnvConfig(st)

	if st.S3Bucket != "env-bucket" {
		t.Errorf("S3Bucket = %q, want the env value to win", st.S3Bucket)
	}
	if st.S3Region != "eu-west-1" {
		t.Errorf("S3Region = %q, want the env value", st.S3Region)
	}
	if !st.S3ForcePathStyle {
		t.Error("S3ForcePathStyle = false, want the env value to win")
	}
	// An upgraded install must keep reading from the backend its images already live in.
	if st.S3Endpoint != "https://minio.internal" {
		t.Errorf("S3Endpoint = %q, want the stored value to survive", st.S3Endpoint)
	}
	if st.S3AccessKey != "stored-key" {
		t.Errorf("S3AccessKey = %q, want the stored value to survive", st.S3AccessKey)
	}
}

// With storage configured from the environment there is no API call left to gate, so the entitlement must decide.
func TestStorageUnavailableReason(t *testing.T) {
	cases := []struct {
		name    string
		ee      entitlementChecker
		st      *models.RegistrySettings
		wantErr bool
		wantHas string
	}{
		{
			"filesystem needs no license",
			unlicensed(),
			&models.RegistrySettings{StorageType: models.RegistryStorageFilesystem},
			false, "",
		},
		{
			"s3 without the entitlement is refused",
			unlicensed(),
			&models.RegistrySettings{StorageType: models.RegistryStorageS3, S3Bucket: "b"},
			true, "Enterprise license",
		},
		{
			// Nothing wired the checker: S3 is a paid feature, so silence is a no.
			"an unwired entitlement checker fails closed",
			nil,
			&models.RegistrySettings{StorageType: models.RegistryStorageS3, S3Bucket: "b"},
			true, "Enterprise license",
		},
		{
			// The bucket can come from the console or the env, so the reason names the missing thing, not one source.
			"licensed s3 without a bucket is refused",
			licensed(),
			&models.RegistrySettings{StorageType: models.RegistryStorageS3},
			true, "no bucket is configured",
		},
		{
			"licensed s3 with a bucket is allowed",
			licensed(),
			&models.RegistrySettings{StorageType: models.RegistryStorageS3, S3Bucket: "b"},
			false, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{ee: tc.ee}
			got := svc.StorageUnavailableReason(tc.st)
			if (got != "") != tc.wantErr {
				t.Fatalf("StorageUnavailableReason = %q, want error: %v", got, tc.wantErr)
			}
			if tc.wantHas != "" && !strings.Contains(got, tc.wantHas) {
				t.Errorf("reason %q does not mention %q", got, tc.wantHas)
			}
		})
	}
}

// renderEnv is the last gate before the container runs, so it refuses independently of callers.
func TestRenderEnvRefusesUnlicensedS3(t *testing.T) {
	st := &models.RegistrySettings{StorageType: models.RegistryStorageS3, S3Bucket: "b"}

	if _, err := (&Service{ee: unlicensed()}).renderEnv(st, false); err == nil {
		t.Fatal("renderEnv rendered an S3 config without the registry_s3 entitlement")
	}

	env, err := (&Service{ee: licensed()}).renderEnv(st, false)
	if err != nil {
		t.Fatalf("renderEnv with a license: %v", err)
	}
	if !hasEnv(env, "REGISTRY_STORAGE=s3") {
		t.Errorf("licensed S3 did not render the s3 driver: %v", env)
	}
}

// The filesystem driver is Community and must never depend on a license.
func TestRenderEnvFilesystemNeedsNoLicense(t *testing.T) {
	env, err := (&Service{ee: unlicensed()}).renderEnv(&models.RegistrySettings{
		StorageType: models.RegistryStorageFilesystem,
	}, false)
	if err != nil {
		t.Fatalf("renderEnv: %v", err)
	}
	if !hasEnv(env, "REGISTRY_STORAGE=filesystem") {
		t.Errorf("want the filesystem driver, got %v", env)
	}
}

// A deploy that cannot push must name the license as the cause, not a connection error.
func TestDistributionReportsUnlicensedStorage(t *testing.T) {
	svc := &Service{
		ee:       unlicensed(),
		cfg:      config.RegistryConfig{Enabled: true, EnabledSet: true, Host: "registry.example.com", StorageType: "s3", S3Bucket: "b"},
		external: baseDomain(""),
	}
	reason := svc.DistributionUnavailableReason()
	if !strings.Contains(reason, "Enterprise license") {
		t.Errorf("DistributionUnavailableReason = %q, want it to name the license", reason)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

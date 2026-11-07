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

package dr

import (
	"strings"
	"testing"
	"time"
)

func manifest() *Manifest {
	return &Manifest{
		Schema:         ManifestSchema,
		Ref:            NewRef("mbi_abc", time.Unix(1_760_000_000, 0).UTC()),
		InstallID:      "mbi_abc",
		MiabiVersion:   "1.7.3",
		KEKFingerprint: "mrt_deadbeef",
		Encrypted:      true,
		IdentitySealed: true,
		Prefix:         "platform/db",
		VolumePrefix:   "platform/volumes",
		Artifacts: []Artifact{
			{Subject: "identity", File: "platform/db/identity-x.mbid"},
			{Subject: "database", File: "miabi_20260731.sql.gz.gpg", Encrypted: true},
			{Subject: "volume", Volume: "mb-node-gateway-providers", File: "mb-node-gateway-providers_20260731.tar.gz.gpg"},
			{Subject: SubjectTenantDatabase, Workspace: "acme", Database: "orders", Engine: "postgres", File: "orders_20260731.sql.gz.gpg"},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	{
		{
			in := manifest()
			body, err := EncodeManifest(in)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			out, err := DecodeManifest(body)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Ref != in.Ref || out.KEKFingerprint != in.KEKFingerprint {
				t.Fatalf("round trip lost fields: %+v", out)
			}
			if got := out.DatabaseArtifact(); got == nil || got.File != "miabi_20260731.sql.gz.gpg" {
				t.Fatalf("DatabaseArtifact() = %+v", got)
			}
			if vols := out.VolumeArtifacts(); len(vols) != 1 || vols[0].Volume != "mb-node-gateway-providers" {
				t.Fatalf("VolumeArtifacts() = %+v", vols)
			}
			if tenants := out.TenantArtifacts(); len(tenants) != 1 || tenants[0].Database != "orders" {
				t.Fatalf("TenantArtifacts() = %+v", tenants)
			}
		}
	}
}

func TestManifestIsXMLOnly(t *testing.T) {
	if ManifestExt != ".xml" {
		t.Fatalf("ManifestExt = %q, want .xml", ManifestExt)
	}
	body, err := EncodeManifest(manifest())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "<?xml") {
		t.Errorf("the info file is not XML:\n%s", body[:60])
	}
	if _, err := DecodeManifest([]byte(`{"schema":1,"ref":"mbdr_x_20260101T000000Z"}`)); err == nil {
		t.Error("a JSON body decoded; the format is XML only")
	}
}

// A manifest is read when its platform is gone, so an unusable one must be rejected loudly.
func TestManifestValidateRejectsUnrestorable(t *testing.T) {
	t.Run("no database dump", func(t *testing.T) {
		m := manifest()
		m.Artifacts = []Artifact{{Subject: "identity", File: "x"}}
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "no control-plane database dump") {
			t.Fatalf("err = %v, want a missing-dump error", err)
		}
	})
	t.Run("future schema", func(t *testing.T) {
		m := manifest()
		m.Schema = ManifestSchema + 1
		if err := m.Validate(); err == nil {
			t.Fatal("a manifest from a newer format was accepted")
		}
	})
	t.Run("bad ref", func(t *testing.T) {
		m := manifest()
		m.Ref = "not-a-ref"
		if err := m.Validate(); err == nil {
			t.Fatal("a manifest with an invalid ref was accepted")
		}
	})
}

func TestManifestObjectRoundTrip(t *testing.T) {
	ref := NewRef("mbi_abc", time.Unix(1_760_000_000, 0).UTC())
	key := ManifestObject("platform", ref)
	if got := RefFromManifestObject(key); got != ref {
		t.Fatalf("RefFromManifestObject(%q) = %q, want %q", key, got, ref)
	}
	// Neither key may be mistaken for the other, or `miabi restore` would try to decrypt JSON.
	if RefFromManifestObject(IdentityObject("platform", ref)) != "" {
		t.Fatal("an identity object was read as a manifest object")
	}
	if RefFromIdentityObject(key) != "" {
		t.Fatal("a manifest object was read as an identity object")
	}
}

func TestManifestObjectWithoutPrefix(t *testing.T) {
	ref := NewRef("mbi_abc", time.Unix(1_760_000_000, 0).UTC())
	if got := ManifestObject("", ref); strings.HasPrefix(got, "/") {
		t.Fatalf("ManifestObject with no prefix = %q, want no leading slash", got)
	}
}

// Validate must not repair the schema, so an unset one fails with a misleading version error.
func TestValidateRejectsUnsetSchema(t *testing.T) {
	m := manifest()
	m.Schema = 0
	if err := m.Validate(); err == nil {
		t.Fatal("a manifest with no schema validated")
	}

	// And encoding must still stamp it, so a caller that only encodes is fine.
	m = manifest()
	m.Schema = 0
	body, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if m.Schema != ManifestSchema {
		t.Errorf("EncodeManifest left Schema = %d", m.Schema)
	}
	if _, err := DecodeManifest(body); err != nil {
		t.Fatalf("the encoded manifest does not decode: %v", err)
	}
}

func TestManifestCarriesItsNotice(t *testing.T) {
	body, err := EncodeManifest(manifest())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), ManifestNotice) {
		t.Errorf("the info file does not name itself:\n%s", body)
	}
}

// The notice claims nothing secret is inside; this keeps it true.
func TestManifestCarriesNoSecrets(t *testing.T) {
	body, err := EncodeManifest(manifest())
	if err != nil {
		t.Fatal(err)
	}
	// The fingerprint is allowed: it is an HMAC identifying a key, not the key.
	for _, forbidden := range []string{"encryption_key", "encryptionKey", "jwt_secret", "jwtSecret", "passphrase", "secret_key", "secretKey"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the info file contains %q:\n%s", forbidden, body)
		}
	}
}

// Restore once missed tenant artifacts: tenant-volume wasn't a volume and the tenants/<ws> branch was missing.
func TestArtifactKeyLocatesTenantArtifacts(t *testing.T) {
	m := &Manifest{Prefix: "databases", VolumePrefix: "volumes"}

	cases := []struct {
		name string
		a    Artifact
		want string
	}{
		{
			name: "control-plane dump",
			a:    Artifact{Subject: SubjectDatabase, File: "miabi_20260731.sql.gz.gpg"},
			want: "databases/miabi_20260731.sql.gz.gpg",
		},
		{
			name: "platform volume",
			a:    Artifact{Subject: SubjectVolume, Volume: "mb-node-gateway-providers", File: "providers_20260731.tar.gz.gpg"},
			want: "volumes/providers_20260731.tar.gz.gpg",
		},
		{
			name: "tenant database",
			a:    Artifact{Subject: SubjectTenantDatabase, Workspace: "system", Database: "goma", File: "goma_20260731_162303.sql.gz.gpg"},
			want: "databases/tenants/system/goma_20260731_162303.sql.gz.gpg",
		},
		{
			name: "tenant volume",
			a:    Artifact{Subject: SubjectTenantVolume, Workspace: "prod", Volume: "mb-vol-2-gitlab-logs", File: "mb-vol-2-gitlab-logs_20260731_162304.tar.gz.gpg"},
			want: "volumes/tenants/prod/mb-vol-2-gitlab-logs_20260731_162304.tar.gz.gpg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.ArtifactKey(tc.a); got != tc.want {
				t.Errorf("ArtifactKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// A recorded path wins: it is what the backup used and survives later layout changes.
func TestArtifactKeyPrefersTheRecordedPath(t *testing.T) {
	m := &Manifest{Prefix: "databases", VolumePrefix: "volumes"}
	a := Artifact{Subject: SubjectTenantDatabase, Workspace: "system", Path: "old/layout/system", File: "goma.sql.gz.gpg"}
	if got := m.ArtifactKey(a); got != "old/layout/system/goma.sql.gz.gpg" {
		t.Errorf("ArtifactKey = %q, want the recorded path", got)
	}
}

func TestTenantPathMatchesWhatTheBackupWrites(t *testing.T) {
	cases := map[string]string{
		"system":    "databases/tenants/system",
		"Prod Team": "databases/tenants/prod-team",
		"":          "databases/tenants/unknown",
	}
	for ws, want := range cases {
		if got := TenantPath("databases", ws); got != want {
			t.Errorf("TenantPath(%q) = %q, want %q", ws, got, want)
		}
	}
	if got := TenantPath("", "acme"); got != "tenants/acme" {
		t.Errorf("TenantPath with no base = %q", got)
	}
}

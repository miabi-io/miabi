// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// One format, one extension: an operator looking in a bucket has one thing to
// find rather than two to guess between.
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

// A manifest is only ever read at a moment when the platform that wrote it is
// gone, so an unusable one must be rejected loudly rather than half-trusted.
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
	// The identity envelope and the manifest sit side by side; neither key may be
	// mistaken for the other, or `miabi restore` would try to decrypt JSON.
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

// A manifest built without an explicit schema fails its own validation with "schema 0 is not
// supported", which reads like a version incompatibility rather than a field nobody set. This
// pins the contract that made that possible: Validate does not repair what it checks.
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

// The info file sits unencrypted next to encrypted artifacts, so it names itself
// — whoever finds one in a bucket should not have to guess what it is.
func TestManifestCarriesItsNotice(t *testing.T) {
	body, err := EncodeManifest(manifest())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), ManifestNotice) {
		t.Errorf("the info file does not name itself:\n%s", body)
	}
}

// And it must still contain nothing secret, in either format — the notice is a
// claim, and this is the test that keeps it true.
func TestManifestCarriesNoSecrets(t *testing.T) {
	body, err := EncodeManifest(manifest())
	if err != nil {
		t.Fatal(err)
	}
	// Anything resembling key material must never reach this file. The
	// fingerprint is allowed: it is an HMAC that identifies a key, not the key.
	for _, forbidden := range []string{"encryption_key", "encryptionKey", "jwt_secret", "jwtSecret", "passphrase", "secret_key", "secretKey"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("the info file contains %q:\n%s", forbidden, body)
		}
	}
}

// A restore searched for tenant artifacts under the top-level database prefix and declared a
// complete recovery point incomplete. Two faults: "tenant-volume" was not recognised as a volume,
// and nothing added the "tenants/<workspace>" branch the backup actually writes to.
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

// A recorded path always wins: it is what the backup actually used, and it
// survives any later change to how the layout is derived.
func TestArtifactKeyPrefersTheRecordedPath(t *testing.T) {
	m := &Manifest{Prefix: "databases", VolumePrefix: "volumes"}
	a := Artifact{Subject: SubjectTenantDatabase, Workspace: "system", Path: "old/layout/system", File: "goma.sql.gz.gpg"}
	if got := m.ArtifactKey(a); got != "old/layout/system/goma.sql.gz.gpg" {
		t.Errorf("ArtifactKey = %q, want the recorded path", got)
	}
}

// The path a restore looks under must be the path the backup wrote to. Both now
// come from TenantPath, so this pins them together.
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

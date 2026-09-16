// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"reflect"
	"strings"
	"testing"
)

// roundTrip renders a manifest as a document, parses it back, and converts it to a manifest again —
// through the YAML, so a wrong tag fails here rather than in production.
func roundTrip(t *testing.T, m *Manifest) *Manifest {
	t.Helper()
	b, err := MarshalDocument(NewDocument(m))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	d, err := ParseDocument(b)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, b)
	}
	return d.Manifest()
}

// The document is a second on-disk shape for the SAME internal type. If a field is lost here it is
// lost from the install: the converge that follows reads the manifest, not the file.
func TestDocumentRoundTripsThroughTheManifest(t *testing.T) {
	m := testManifest()
	got := roundTrip(t, m)

	if !reflect.DeepEqual(m, got) {
		t.Errorf("round trip changed the manifest\n before: %+v\n  after: %+v", m, got)
	}
}

// Secrets are the values a conversion may never mint or drop: a regenerated db_password is an
// install nobody can recover, because Postgres keeps the password its data directory was created with.
func TestEverySecretSurvivesTheRoundTrip(t *testing.T) {
	m := testManifest()
	got := roundTrip(t, m)

	if got.Secrets != m.Secrets {
		t.Errorf("secrets changed across the round trip\n before: %+v\n  after: %+v", m.Secrets, got.Secrets)
	}
	if got.Secrets.AdminEmail != m.Secrets.AdminEmail {
		t.Errorf("admin email = %q, want %q — it moves to spec.admin.email and must come back",
			got.Secrets.AdminEmail, m.Secrets.AdminEmail)
	}
}

// The digest of the default gateway config is what keeps a customized goma.yml from being
// overwritten. Recomputing or dropping it silently breaks that, one upgrade later.
func TestTheGatewayConfigDigestSurvivesTheRoundTrip(t *testing.T) {
	m := testManifest()
	m.Gateway.ConfigSHA = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	if got := roundTrip(t, m).Gateway.ConfigSHA; got != m.Gateway.ConfigSHA {
		t.Errorf("configSha = %q, want %q — a lost digest overwrites a customized goma.yml", got, m.Gateway.ConfigSHA)
	}
}

// The document keeps the gateway key with the other secrets; the manifest keeps it in gateway.env,
// which is where the gateway and control-plane specs already read it from.
func TestTheGatewayKeyMovesBetweenSecretsAndGatewayEnv(t *testing.T) {
	m := testManifest()
	if m.Gateway.Env == nil {
		m.Gateway.Env = map[string]string{}
	}
	m.Gateway.Env[gomaConfigEncryptionKey] = "s3cr3t"

	doc := NewDocument(m)
	if doc.Spec.Secrets.GomaConfigEncryptionKey != "s3cr3t" {
		t.Errorf("spec.secrets.gomaConfigEncryptionKey = %q, want it moved out of gateway.env",
			doc.Spec.Secrets.GomaConfigEncryptionKey)
	}
	if _, still := doc.Spec.Gateway.Env[gomaConfigEncryptionKey]; still {
		t.Error("the key is in both spec.secrets and spec.gateway.env — two homes is how the two sides disagree")
	}

	back := roundTrip(t, m)
	if back.Gateway.Env[gomaConfigEncryptionKey] != "s3cr3t" {
		t.Errorf("gateway.env[%s] = %q, want it back where the specs read it",
			gomaConfigEncryptionKey, back.Gateway.Env[gomaConfigEncryptionKey])
	}
}

// These have no home in the flat schema, so nothing but the document carries them.
func TestTheKindedOnlySettingsSurviveTheRoundTrip(t *testing.T) {
	yes := true
	m := testManifest()
	m.Install = InstallSettings{
		ACMEDirectoryURL:      "https://acme.example.com/directory",
		RegistryStorage:       "s3",
		RegistryPlatformToken: "tok",
		Pool:                  PoolSpec{CIDR: "10.64.0.0/12", SubnetPrefix: 24},
		HostPorts:             HostPortsSpec{Min: 1024, Max: 65535},
		External:              ExternalSpec{BaseDomain: "apps.example.com", CertProvider: "cloudflare"},
		DNS:                   DNSSpec{ReconcileMinutes: 30},
		License:               LicenseSpec{File: "/etc/miabi/license.jwt"},
		Backup: &BackupSpec{
			Schedule:    "0 3 * * *",
			Destination: BackupDestination{Bucket: "b", Region: "eu-west-3", UseSSL: &yes, Path: "prod"},
			Encryption:  BackupEncryption{Passphrase: "pp", Encrypt: &yes},
			Retention:   BackupRetention{Days: 30},
		},
	}

	if got := roundTrip(t, m); !reflect.DeepEqual(got.Install, m.Install) {
		t.Errorf("install settings changed\n before: %+v\n  after: %+v", m.Install, got.Install)
	}
}

// An unset optional must stay unset: the compiler emits a variable only for a field the manifest
// states, because an emitted variable pins the console's field to whatever it says.
func TestAnUnsetOptionalStaysUnset(t *testing.T) {
	m := testManifest()
	m.Install.Backup = nil

	got := roundTrip(t, m)
	if got.Install.Backup != nil {
		t.Error("backup came back set from a manifest that never mentioned it — that would pin the console's fields")
	}
	if got.Install.External.BaseDomain != "" {
		t.Errorf("external.baseDomain = %q, want empty", got.Install.External.BaseDomain)
	}
}

// Every other manifest parser in the platform refuses unknown fields. A tolerated `acme_emial:` is
// an install with no ACME contact and no error to show for it.
func TestUnknownFieldIsRefused(t *testing.T) {
	doc := `
apiVersion: install.miabi.io/v1
kind: ControlPlane
spec:
  domain: miabi.example.com
  acme_emial: you@example.com
`
	_, err := ParseDocument([]byte(doc))
	if err == nil {
		t.Fatal("a misspelled field parsed cleanly — the typo would cost an ACME contact silently")
	}
	if !strings.Contains(err.Error(), "acme_emial") {
		t.Errorf("error = %q, want it to name the offending field", err)
	}
}

func TestAPIVersionIsValidated(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"a newer install dialect": {
			doc:  "apiVersion: install.miabi.io/v2\nkind: ControlPlane\nspec:\n  domain: x.example.com\n",
			want: "upgrade the CLI",
		},
		"the workspace dialect": {
			doc:  "apiVersion: miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: x.example.com\n",
			want: "apiVersion must be",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDocument([]byte(tc.doc))
			if err == nil {
				t.Fatal("accepted a document this build does not understand")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestUnknownKindIsRefused(t *testing.T) {
	doc := "apiVersion: install.miabi.io/v1\nkind: Application\nspec:\n  domain: x.example.com\n"
	_, err := ParseDocument([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("error = %v, want it to refuse a kind that is not ControlPlane", err)
	}
}

func TestMetadataNameDefaultsAndIsValidated(t *testing.T) {
	d, err := ParseDocument([]byte("apiVersion: install.miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: x.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Metadata.Name != defaultDocumentName {
		t.Errorf("metadata.name = %q, want it defaulted to %q", d.Metadata.Name, defaultDocumentName)
	}

	bad := "apiVersion: install.miabi.io/v1\nkind: ControlPlane\nmetadata:\n  name: Not A Name\nspec:\n  domain: x.example.com\n"
	if _, err := ParseDocument([]byte(bad)); err == nil {
		t.Error("accepted a metadata.name the workspace dialect would refuse")
	}
}

// A file may hold several documents, and a trailing --- leaves an empty one. Two ControlPlanes are a
// mistake worth refusing: an install has exactly one, and silently taking the first hides the other.
func TestEmptyDocumentsAreSkippedAndDuplicatesRefused(t *testing.T) {
	one := "---\napiVersion: install.miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: x.example.com\n---\n"
	if _, err := ParseDocument([]byte(one)); err != nil {
		t.Fatalf("a trailing --- should be skipped, got %v", err)
	}

	two := one + "apiVersion: install.miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: y.example.com\n"
	if _, err := ParseDocument([]byte(two)); err == nil {
		t.Error("accepted two ControlPlane documents — an install has exactly one")
	}
}

func TestParseReportsInputThatHoldsNoDocument(t *testing.T) {
	if _, err := ParseDocument([]byte("# just a comment\n")); err != ErrNoDocument {
		t.Errorf("err = %v, want ErrNoDocument", err)
	}
}

// Load dispatches on this, so a malformed document must still read as one — otherwise it is reported
// as "not a Miabi stack manifest", which sends the operator looking in the wrong place.
func TestIsDocumentTellsTheTwoShapesApart(t *testing.T) {
	flat := "version: 1\ndomain: miabi.example.com\n"
	if IsDocument([]byte(flat)) {
		t.Error("the flat schema was read as a kinded document")
	}
	kinded := "apiVersion: install.miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: x.example.com\n"
	if !IsDocument([]byte(kinded)) {
		t.Error("a kinded document was not recognised")
	}
	broken := "apiVersion: install.miabi.io/v1\nkind: ControlPlane\nspec:\n  domain: [\n"
	if !IsDocument([]byte(broken)) {
		t.Error("a malformed document read as the flat schema — the operator would be told " +
			"\"this is not a Miabi stack manifest\" instead of being shown the YAML error")
	}
}

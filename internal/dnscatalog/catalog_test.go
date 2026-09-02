// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dnscatalog

import "testing"

func TestDescriptorsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range All() {
		if d.Type == "" || d.Label == "" {
			t.Fatalf("descriptor %+v needs a type and a label", d)
		}
		if seen[d.Type] {
			t.Fatalf("duplicate type %q", d.Type)
		}
		seen[d.Type] = true

		keys := map[string]bool{}
		required := 0
		for _, f := range d.Fields {
			if f.Key == "" || f.Label == "" || f.Type == "" {
				t.Fatalf("%s: field %+v needs a key, label and type", d.Type, f)
			}
			if keys[f.Key] {
				t.Fatalf("%s: duplicate field key %q", d.Type, f.Key)
			}
			keys[f.Key] = true
			if f.Required {
				required++
			}
			if f.Type == FieldEnum && len(f.Options) == 0 {
				t.Fatalf("%s: enum field %q has no options", d.Type, f.Key)
			}
		}
		if required == 0 {
			t.Fatalf("%s: no required field, so an empty credential would connect", d.Type)
		}
	}
}

func TestValidateRejectsBlankRequired(t *testing.T) {
	d, ok := Get("route53")
	if !ok {
		t.Fatal("route53 missing from the catalog")
	}
	if err := d.Validate(map[string]string{"access_key_id": "a", "secret_access_key": "s"}); err != nil {
		t.Fatalf("optional region should not be required: %v", err)
	}
	if err := d.Validate(map[string]string{"access_key_id": "a", "secret_access_key": "   "}); err == nil {
		t.Fatal("whitespace must not satisfy a required field")
	}
}

func TestPublicOmitsSecrets(t *testing.T) {
	d, _ := Get("route53")
	pub := d.Public(map[string]string{
		"access_key_id": "AKIA", "secret_access_key": "shh", "region": "eu-west-1",
	})
	if _, leaked := pub["secret_access_key"]; leaked {
		t.Fatal("a secret field reached the public view")
	}
	if pub["region"] != "eu-west-1" || pub["access_key_id"] != "AKIA" {
		t.Fatalf("non-secret fields missing: %v", pub)
	}
}

func TestGetIsCaseAndSpaceInsensitive(t *testing.T) {
	if _, ok := Get("  Cloudflare "); !ok {
		t.Fatal("lookup should normalize the type")
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("unknown type must not resolve")
	}
}

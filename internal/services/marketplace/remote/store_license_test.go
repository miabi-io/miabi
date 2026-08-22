// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/enterprise"
)

// fakeEE grants exactly the flags it is given.
type fakeEE struct{ flags map[string]bool }

func (f fakeEE) Has(flag string) bool { return f.flags[flag] }

func licensed() fakeEE {
	return fakeEE{flags: map[string]bool{enterprise.FlagPrivateRegistry: true}}
}

const officialURL = "https://marketplace.miabi.io"

// Unlicensed, a custom marketplace is refused — but the install keeps a working
// catalog rather than losing its marketplace, so it falls back to official.
func TestCustomMarketplaceRequiresLicense(t *testing.T) {
	s := New("https://templates.acme.internal", nil)
	s.SetEntitlements(fakeEE{}, officialURL)

	url, denied := s.Source()
	if !denied {
		t.Error("a custom marketplace must be refused without a license")
	}
	if url != officialURL {
		t.Errorf("fell back to %q, want the official catalog", url)
	}
	if !s.Enabled() {
		t.Error("the fallback must leave the marketplace working, not disabled")
	}
}

func TestCustomMarketplaceAllowedWhenLicensed(t *testing.T) {
	const custom = "https://templates.acme.internal"
	s := New(custom, nil)
	s.SetEntitlements(licensed(), officialURL)

	url, denied := s.Source()
	if denied || url != custom {
		t.Errorf("licensed install got %q (denied=%v), want the custom marketplace", url, denied)
	}
}

// The official catalog and the air-gap kill switch are not paid configurations
// and must never be gated.
func TestOfficialAndDisabledAreNotGated(t *testing.T) {
	official := New(officialURL, nil)
	official.SetEntitlements(fakeEE{}, officialURL)
	if url, denied := official.Source(); denied || url != officialURL {
		t.Errorf("official catalog was gated: %q denied=%v", url, denied)
	}

	// A trailing slash is the same marketplace, not a custom one.
	slashed := New(officialURL+"/", nil)
	slashed.SetEntitlements(fakeEE{}, officialURL)
	if _, denied := slashed.Source(); denied {
		t.Error("a trailing slash must not read as a custom marketplace")
	}

	off := New("", nil)
	off.SetEntitlements(fakeEE{}, officialURL)
	if url, denied := off.Source(); denied || url != "" {
		t.Errorf("the air-gap kill switch was gated: %q denied=%v", url, denied)
	}
	if off.Enabled() {
		t.Error("an empty URL must stay disabled")
	}
}

// A license installed at runtime takes effect on the next sync, without a
// restart — and a lapse returns the install to the official catalog the same way.
func TestMarketplaceLicenseChangeTakesEffectWithoutRestart(t *testing.T) {
	const custom = "https://templates.acme.internal"
	s := New(custom, nil)

	s.SetEntitlements(fakeEE{}, officialURL)
	if url, _ := s.Source(); url != officialURL {
		t.Fatalf("unlicensed source = %q, want official", url)
	}

	s.SetEntitlements(licensed(), officialURL)
	if url, denied := s.Source(); url != custom || denied {
		t.Errorf("after licensing, source = %q (denied=%v), want the custom marketplace", url, denied)
	}

	s.SetEntitlements(fakeEE{}, officialURL)
	if url, denied := s.Source(); url != officialURL || !denied {
		t.Errorf("after the license lapsed, source = %q (denied=%v), want official", url, denied)
	}
}

// Falling back must not keep serving the custom catalog's templates: they came
// from a marketplace this install may no longer sync from.
func TestFallbackDiscardsTheCustomCatalog(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, false)
	s := New(srv.URL, nil)
	s.SetEntitlements(licensed(), officialURL)
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, _, ok := s.Manifest("cooltool", "1.2.0"); !ok {
		t.Fatal("licensed sync should have populated the catalog")
	}

	// The license lapses; the store is held to official.
	s.SetEntitlements(fakeEE{}, officialURL)
	if _, _, ok := s.Manifest("cooltool", "1.2.0"); ok {
		t.Error("the custom marketplace's templates survived the fallback")
	}
	if len(s.Templates()) != 0 {
		t.Errorf("expected the decoded view to be discarded, got %d templates", len(s.Templates()))
	}
}

// A bundle cached from one marketplace must never be loaded for another, or a
// restart after a lapse would serve the custom catalog from Redis.
func TestCachedBundleIsScopedToItsSource(t *testing.T) {
	srv, _ := exportServer(t, `"v1"`, false)
	cache := &memCache{}

	custom := New(srv.URL, cache)
	custom.SetEntitlements(licensed(), officialURL)
	if err := custom.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if e := cache.get(srv.URL); len(e.data) == 0 {
		t.Fatal("custom sync did not populate the cache")
	}

	// A fresh, unlicensed process: same cache, held to official.
	restarted := New(srv.URL, cache)
	restarted.SetEntitlements(fakeEE{}, officialURL)
	if err := restarted.LoadCache(context.Background()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if _, _, ok := restarted.Manifest("cooltool", "1.2.0"); ok {
		t.Error("a cached bundle from the custom marketplace was served after the fallback")
	}
}

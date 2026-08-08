// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// These exercise the object client and probe against a REAL S3-compatible store, so the "Test
// connection" round trip is proven end to end. They skip unless MIABI_S3_IT_ENDPOINT (or, for
// AWS, MIABI_S3_IT_BUCKET) is set, so the default `go test` stays hermetic and offline.
func itConfig(t *testing.T) Config {
	t.Helper()
	bucket := os.Getenv("MIABI_S3_IT_BUCKET")
	if bucket == "" {
		t.Skip("set MIABI_S3_IT_BUCKET (and friends) to run the object-store integration tests")
	}
	return Config{
		Endpoint:       os.Getenv("MIABI_S3_IT_ENDPOINT"),
		Bucket:         bucket,
		Region:         os.Getenv("MIABI_S3_IT_REGION"),
		AccessKey:      os.Getenv("MIABI_S3_IT_ACCESS_KEY"),
		SecretKey:      os.Getenv("MIABI_S3_IT_SECRET_KEY"),
		UseSSL:         os.Getenv("MIABI_S3_IT_SSL") == "true",
		ForcePathStyle: os.Getenv("MIABI_S3_IT_PATH_STYLE") == "true",
	}
}

func itContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The probe's whole promise: it writes, reads back, and cleans up after itself.
func TestProbeRoundTrip_Integration(t *testing.T) {
	cfg := itConfig(t)
	ctx := itContext(t)

	p, err := RunProbe(ctx, cfg, "miabi-it/probe")
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !p.Wrote || !p.ReadBack {
		t.Fatalf("probe reported an incomplete round trip: %+v", p)
	}
	if !p.Removed {
		t.Errorf("probe could not delete %s: the store accepts writes but not deletes", p.Key)
	}
	t.Logf("probe ok: wrote, read back and removed %s", p.Key)

	// Nothing may be left behind: a test that litters the operator's bucket is a
	// test they stop running.
	store, err := New(cfg)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if exists, err := store.Exists(ctx, p.Key); err != nil {
		t.Fatalf("head %s: %v", p.Key, err)
	} else if exists {
		t.Fatalf("%s survived the probe", p.Key)
	}
}

// The bucket root is a valid target and the most common one for a fresh setup.
func TestProbeAtBucketRoot_Integration(t *testing.T) {
	cfg := itConfig(t)
	p, err := RunProbe(itContext(t), cfg, "")
	if err != nil {
		t.Fatalf("probe at the bucket root failed: %v", err)
	}
	if strings.Contains(p.Key, "/") {
		t.Fatalf("root probe wrote under a prefix: %s", p.Key)
	}
}

// A wrong bucket must be reported as a wrong bucket — the failure an operator
// can act on — and not as a generic 404 or, worse, a pass.
func TestProbeRejectsAWrongBucket_Integration(t *testing.T) {
	cfg := itConfig(t)
	cfg.Bucket += "-does-not-exist"

	_, err := RunProbe(itContext(t), cfg, "")
	if err == nil {
		t.Fatal("a nonexistent bucket passed the probe")
	}
	t.Logf("wrong bucket → %v", err)
	if !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("the failure does not name the bucket problem: %v", err)
	}
}

// Wrong credentials are the other half of what the old structural check could
// never catch: it had a secret, so it said the settings looked valid.
func TestProbeRejectsWrongCredentials_Integration(t *testing.T) {
	cfg := itConfig(t)
	cfg.SecretKey = "definitely-not-the-secret-key"

	_, err := RunProbe(itContext(t), cfg, "")
	if err == nil {
		t.Fatal("a wrong secret key passed the probe")
	}
	t.Logf("wrong secret → %v", err)

	cfg = itConfig(t)
	cfg.AccessKey = "MIABINOSUCHACCESSKEY"
	if _, err := RunProbe(itContext(t), cfg, ""); err == nil {
		t.Fatal("an unknown access key passed the probe")
	} else {
		t.Logf("wrong access key → %v", err)
	}
}

// List is what the bundle listing is built on: it must see an object written
// under a prefix, and stop at that prefix.
func TestListSeesWhatWasWritten_Integration(t *testing.T) {
	cfg := itConfig(t)
	ctx := itContext(t)
	store, err := New(cfg)
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	const prefix = "miabi-it/list"
	key := prefix + "/object.txt"
	if err := store.Put(ctx, key, []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	objects, err := store.List(ctx, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, o := range objects {
		if o.Key == key {
			found = true
			if o.Size != 5 {
				t.Errorf("listed size = %d, want 5", o.Size)
			}
		}
	}
	if !found {
		t.Fatalf("List(%q) did not return %q (got %d objects)", prefix, key, len(objects))
	}

	// A neighbouring prefix must not be swept in: the bundle listing reads every
	// index under its prefix and would otherwise pick up another install's.
	if others, err := store.List(ctx, "miabi-it/list-elsewhere"); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(others) != 0 {
		t.Fatalf("an unrelated prefix returned %d objects", len(others))
	}
}

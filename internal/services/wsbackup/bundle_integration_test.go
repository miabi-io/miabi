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

package wsbackup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Exercises the bucket layout a cross-install restore depends on; skips unless MIABI_S3_IT_BUCKET is set.
func itStore(t *testing.T) (*blob.Store, blob.Config) {
	t.Helper()
	bucket := os.Getenv("MIABI_S3_IT_BUCKET")
	if bucket == "" {
		t.Skip("set MIABI_S3_IT_BUCKET (and friends) to run the bundle integration test")
	}
	cfg := blob.Config{
		Endpoint:       os.Getenv("MIABI_S3_IT_ENDPOINT"),
		Bucket:         bucket,
		Region:         os.Getenv("MIABI_S3_IT_REGION"),
		AccessKey:      os.Getenv("MIABI_S3_IT_ACCESS_KEY"),
		SecretKey:      os.Getenv("MIABI_S3_IT_SECRET_KEY"),
		UseSSL:         os.Getenv("MIABI_S3_IT_SSL") == "true",
		ForcePathStyle: os.Getenv("MIABI_S3_IT_PATH_STYLE") == "true",
	}
	store, err := blob.New(cfg)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return store, cfg
}

func TestBundleLayoutRoundTrip_Integration(t *testing.T) {
	store, cfg := itStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const (
		prefix     = "miabi-it/bundles"
		passphrase = "correct-horse-42"
	)
	ref := wsbundle.NewRef("shop", time.Now())
	state := &wsbundle.State{
		Schema:    wsbundle.StateSchema,
		Workspace: wsbundle.Workspace{Name: "shop", DisplayName: "Shop"},
		Secrets:   []wsbundle.Secret{{Name: "stripe", Value: "sk_live_supersecret"}},
		Apps:      []wsbundle.Application{{Name: "api", Image: "ghcr.io/acme/api", Tag: "v2"}},
	}

	sealed, err := wsbundle.Seal(state, passphrase)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	stateKey := wsbundle.StateObject(prefix, ref, true)
	infoKey := wsbundle.InfoObject(prefix, ref)
	t.Cleanup(func() {
		_ = store.Delete(context.Background(), stateKey)
		_ = store.Delete(context.Background(), infoKey)
	})

	if err := store.Put(ctx, stateKey, sealed); err != nil {
		t.Fatalf("upload state: %v", err)
	}
	info := &wsbundle.Info{
		Schema: wsbundle.InfoSchema, Ref: ref, Workspace: "shop", Encrypted: true,
		Bucket: cfg.Bucket, Prefix: prefix, Apps: 1, Secrets: 1,
		Artifacts: []wsbundle.Artifact{{
			Subject: wsbundle.SubjectState, File: "state-" + ref + wsbundle.StateExt,
			Path: wsbundle.Root(prefix, ref), SizeBytes: int64(len(sealed)), Encrypted: true,
		}},
		CreatedAt: time.Now().UTC(),
	}
	body, err := wsbundle.EncodeInfo(info)
	if err != nil {
		t.Fatalf("encode info: %v", err)
	}
	if err := store.Put(ctx, infoKey, body); err != nil {
		t.Fatalf("upload info: %v", err)
	}

	objects, err := store.List(ctx, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found string
	for _, o := range objects {
		if r := wsbundle.RefFromInfoObject(o.Key); r == ref {
			found = o.Key
		}
	}
	if found == "" {
		t.Fatalf("listing %q did not surface the bundle index for %s (%d objects)", prefix, ref, len(objects))
	}

	raw, err := store.GetBytes(ctx, found)
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	got, err := wsbundle.DecodeInfo(raw)
	if err != nil {
		t.Fatalf("decode info: %v", err)
	}
	art := got.StateArtifact()
	if art == nil {
		t.Fatal("the index carries no state artifact")
	}
	if art.Key() != stateKey {
		t.Fatalf("the index points at %q; the state file is at %q", art.Key(), stateKey)
	}

	sealedBack, err := store.GetBytes(ctx, art.Key())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	opened, err := wsbundle.Open(sealedBack, passphrase)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if opened.Workspace.Name != "shop" || len(opened.Secrets) != 1 ||
		opened.Secrets[0].Value != "sk_live_supersecret" {
		t.Fatalf("the state did not survive the bucket: %+v", opened.Workspace)
	}

	if _, err := wsbundle.Open(sealedBack, "another-horse-42"); err == nil {
		t.Fatal("the state file opened with the wrong passphrase")
	}

	branch, err := store.List(ctx, wsbundle.Root(prefix, ref))
	if err != nil {
		t.Fatalf("list branch: %v", err)
	}
	for _, o := range branch {
		if err := store.Delete(ctx, o.Key); err != nil {
			t.Fatalf("delete %s: %v", o.Key, err)
		}
	}
	if err := store.Delete(ctx, infoKey); err != nil {
		t.Fatalf("delete index: %v", err)
	}
	left, err := store.List(ctx, prefix)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	for _, o := range left {
		if wsbundle.RefFromInfoObject(o.Key) == ref || o.Key == stateKey {
			t.Fatalf("%s survived the delete", o.Key)
		}
	}
}

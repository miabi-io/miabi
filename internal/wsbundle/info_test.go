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

package wsbundle

import (
	"strings"
	"testing"
	"time"
)

func testInfo() *Info {
	ref := NewRef("shop", time.Unix(1_760_000_000, 0).UTC())
	return &Info{
		Schema:       InfoSchema,
		Ref:          ref,
		Workspace:    "shop",
		MiabiVersion: "1.7.3",
		Encrypted:    true,
		Bucket:       "acme-backups",
		Prefix:       "bundles",
		Apps:         2,
		Databases:    1,
		Volumes:      1,
		Secrets:      3,
		Routes:       2,
		Artifacts: []Artifact{
			{Subject: SubjectState, File: "state-" + ref + StateExt, Path: Root("bundles", ref), Encrypted: true},
			{Subject: SubjectDatabase, Instance: "pg", Database: "orders", Engine: "postgres",
				File: "orders_20260731.sql.gz.gpg", Path: DatabasePath("bundles", ref), Encrypted: true},
			{Subject: SubjectVolume, Volume: "uploads",
				File: "uploads_20260731.tar.gz.gpg", Path: VolumePath("bundles", ref), Encrypted: true},
		},
		CreatedAt: time.Unix(1_760_000_000, 0).UTC(),
	}
}

func TestInfoRoundTrip(t *testing.T) {
	in := testInfo()
	body, err := EncodeInfo(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeInfo(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Ref != in.Ref || out.Workspace != "shop" || out.Apps != 2 {
		t.Fatalf("round trip lost fields: %+v", out)
	}
	if st := out.StateArtifact(); st == nil || !strings.HasSuffix(st.File, StateExt) {
		t.Fatalf("StateArtifact() = %+v", st)
	}
	if dbs := out.BySubject(SubjectDatabase); len(dbs) != 1 || dbs[0].Database != "orders" {
		t.Fatalf("BySubject(database) = %+v", dbs)
	}
	if vols := out.BySubject(SubjectVolume); len(vols) != 1 || vols[0].Volume != "uploads" {
		t.Fatalf("BySubject(volume) = %+v", vols)
	}
}

func TestInfoIsSelfDescribing(t *testing.T) {
	body, err := EncodeInfo(testInfo())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(body)
	if !strings.HasPrefix(s, "<?xml") {
		t.Fatalf("info file is not XML: %s", s[:20])
	}
	if !strings.Contains(s, InfoNotice) {
		t.Fatal("info file carries no notice explaining what it is")
	}
}

// Refusing early avoids a half-created workspace.
func TestInfoWithoutStateIsRejected(t *testing.T) {
	in := testInfo()
	in.Artifacts = in.Artifacts[1:] // drop the state file
	body, err := EncodeInfo(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := DecodeInfo(body); err == nil {
		t.Fatal("decoded a bundle with no state file")
	}
}

// A failed capture is never counted as present, or a bundle missing a database would look complete.
func TestFailedArtifactIsNotRestorable(t *testing.T) {
	in := testInfo()
	in.Artifacts = append(in.Artifacts, Artifact{
		Subject: SubjectDatabase, Instance: "pg", Database: "analytics", Error: "instance is not running",
	})
	body, _ := EncodeInfo(in)
	out, err := DecodeInfo(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out.BySubject(SubjectDatabase); len(got) != 1 {
		t.Fatalf("failed artifact was offered for restore: %+v", got)
	}
	var found bool
	for _, a := range out.Artifacts {
		if a.Database == "analytics" && a.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("the failure was dropped from the index instead of recorded")
	}
}

func TestArtifactKey(t *testing.T) {
	a := Artifact{File: "orders.sql.gz", Path: "bundles/mbwb_shop_x/databases"}
	if got, want := a.Key(), "bundles/mbwb_shop_x/databases/orders.sql.gz"; got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
	bare := Artifact{File: "orders.sql.gz"}
	if got := bare.Key(); got != "orders.sql.gz" {
		t.Fatalf("Key() with no path = %q", got)
	}
}

func TestRefRoundTripsThroughItsObjectName(t *testing.T) {
	ref := NewRef("Shop Prod!", time.Unix(1_760_000_000, 0).UTC())
	if !IsRef(ref) {
		t.Fatalf("NewRef produced a ref that is not one: %q", ref)
	}
	if strings.ContainsAny(ref, " !/") {
		t.Fatalf("ref is not object-key safe: %q", ref)
	}
	key := InfoObject("bundles", ref)
	if got := RefFromInfoObject(key); got != ref {
		t.Fatalf("RefFromInfoObject(%q) = %q, want %q", key, got, ref)
	}
	// Mistaking an artifact for an index would make a restore parse a dump as XML.
	if got := RefFromInfoObject(DatabasePath("bundles", ref) + "/orders.sql.gz"); got != "" {
		t.Fatalf("an artifact key was read as an index: %q", got)
	}
}

// Index at the top, artifacts in the bundle's branch, so listing a bucket shows bundles rather than contents.
func TestLayoutSeparatesIndexFromArtifacts(t *testing.T) {
	ref := NewRef("shop", time.Unix(1_760_000_000, 0).UTC())
	info := InfoObject("bundles", ref)
	root := Root("bundles", ref)
	if strings.HasPrefix(info, root) {
		t.Fatalf("the index %q lives inside the branch %q it indexes", info, root)
	}
	for _, p := range []string{StateObject("bundles", ref, true), StateObject("bundles", ref, false), DatabasePath("bundles", ref), VolumePath("bundles", ref)} {
		if !strings.HasPrefix(p, root+"/") {
			t.Fatalf("%q is outside the bundle's branch %q", p, root)
		}
	}
}

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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// memRegistry keeps tags as pointers to digests, and deletes a manifest the way a real registry does.
type memRegistry struct {
	mu       sync.Mutex
	tags     map[string]string // tag -> digest, in ws_7/web
	deletes  []string
	disabled bool
	broken   map[string]bool // tags whose manifest lookup fails with a 500
}

func (m *memRegistry) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	const prefix = "/v2/ws_7/web/"
	switch {
	case r.URL.Path == prefix+"tags/list":
		var tags []string
		for t := range m.tags {
			tags = append(tags, t)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "ws_7/web", "tags": tags})
	case strings.HasPrefix(r.URL.Path, prefix+"manifests/sha256:") && r.Method == http.MethodDelete:
		if m.disabled {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		d := strings.TrimPrefix(r.URL.Path, prefix+"manifests/")
		found := false
		for t, td := range m.tags {
			if td == d {
				delete(m.tags, t)
				found = true
			}
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.deletes = append(m.deletes, d)
		w.WriteHeader(http.StatusAccepted)
	case strings.HasPrefix(r.URL.Path, prefix+"manifests/"):
		tag := strings.TrimPrefix(r.URL.Path, prefix+"manifests/")
		if m.broken[tag] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		d, ok := m.tags[tag]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func memSvc(t *testing.T, tags map[string]string) (*Service, *memRegistry) {
	t.Helper()
	m := &memRegistry{tags: tags, broken: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(m.handler))
	t.Cleanup(srv.Close)
	return &Service{reg: NewClient(srv.URL, nil)}, m
}

func outcomeOf(res *DeleteResult, tag string) TagOutcome {
	for _, o := range res.Tags {
		if o.Tag == tag {
			return o
		}
	}
	return TagOutcome{}
}

func TestDeleteTagsReportsAndRemovesSiblings(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1.4.2": "sha256:a", "v1.4": "sha256:a", "latest": "sha256:a", "v1.3": "sha256:b"})
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1.4.2"}, false)
	if err != nil {
		t.Fatal(err)
	}
	o := outcomeOf(res, "v1.4.2")
	if o.Result != TagDeleted || !slices.Equal(o.Siblings, []string{"latest", "v1.4"}) {
		t.Fatalf("outcome = %+v; the tags on the same image go too, and must be named", o)
	}
	if !slices.Equal(res.Removed, []string{"latest", "v1.4.2", "v1.4"}) {
		t.Errorf("removed = %v", res.Removed)
	}
	if len(reg.deletes) != 1 || reg.tags["v1.3"] != "sha256:b" {
		t.Errorf("deletes = %v, tags left = %v", reg.deletes, reg.tags)
	}
}

func TestDeleteTagsDeletesEachImageOnce(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"a1": "sha256:a", "a2": "sha256:a", "b1": "sha256:b"})
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"a1", "a2", "b1", "a1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.deletes) != 2 {
		t.Fatalf("deletes = %v; two images, two deletes", reg.deletes)
	}
	if len(res.Tags) != 3 {
		t.Fatalf("outcomes = %+v; a repeated tag is reported once", res.Tags)
	}
	if s := outcomeOf(res, "a1").Siblings; len(s) != 0 {
		t.Errorf("siblings = %v; a2 was named too, so it is no surprise", s)
	}
}

func TestDeleteTagsLeavesAnInUseImageAndDeletesTheRest(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"live": "sha256:a", "old": "sha256:b", "older": "sha256:c"})
	svc.SetCatalog(stubCatalog{digests: map[string]bool{"sha256:a": true}})
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"live", "old", "older", "ghost"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"live": TagInUse, "old": TagDeleted, "older": TagDeleted, "ghost": TagNotFound}
	for tag, r := range want {
		if got := outcomeOf(res, tag).Result; got != r {
			t.Errorf("%s = %s, want %s", tag, got, r)
		}
	}
	if _, ok := reg.tags["live"]; !ok {
		t.Error("the in-use image was deleted")
	}
}

func TestDeleteTagsDryRunDeletesNothing(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1": "sha256:a", "latest": "sha256:a"})
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomeOf(res, "v1"); o.Result != TagWouldDelete || !slices.Equal(o.Siblings, []string{"latest"}) {
		t.Fatalf("outcome = %+v", o)
	}
	if !res.DryRun || len(reg.deletes) != 0 || len(reg.tags) != 2 {
		t.Fatalf("dry run deleted: %v", reg.deletes)
	}
}

func TestDeleteTagsReportsALookupErrorAsFailedNotMissing(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1": "sha256:a"})
	reg.broken["v1"] = true
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomeOf(res, "v1"); o.Result != TagFailed || o.Error == "" {
		t.Fatalf("outcome = %+v; a registry error is not a missing tag", o)
	}
}

func TestDeleteTagsRefusesWhenDeletionIsDisabled(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1": "sha256:a"})
	reg.disabled = true
	if _, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1"}, false); !errors.Is(err, ErrDeleteDisabled) {
		t.Fatalf("err = %v, want ErrDeleteDisabled", err)
	}
}

func TestDeleteTagsValidatesTheRequest(t *testing.T) {
	svc, _ := memSvc(t, map[string]string{})
	ctx := context.Background()
	if _, err := svc.DeleteTags(ctx, 7, "web", nil, false); !errors.Is(err, ErrNoTags) {
		t.Errorf("no tags: %v", err)
	}
	if _, err := svc.DeleteTags(ctx, 7, "web", []string{"bad tag"}, false); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("invalid tag: %v", err)
	}
	many := make([]string, maxDeleteTags+1)
	for i := range many {
		many[i] = fmt.Sprintf("t%d", i)
	}
	if _, err := svc.DeleteTags(ctx, 7, "web", many, false); !errors.Is(err, ErrTooManyTags) {
		t.Errorf("too many: %v", err)
	}
}

// A release deployed by tag records no digest, so its tag must still protect every tag on that image.
func TestDeleteTagsProtectsAnImageDeployedByTag(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1.4.2": "sha256:a", "v1.4": "sha256:a", "v1.3": "sha256:b"})
	svc.SetCatalog(stubCatalog{
		digests: map[string]bool{},
		refs:    map[string]bool{"registry.example.com/ws_7/web:v1.4": true, "registry.example.com/ws_8/web:v1.3": true},
	})
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1.4.2", "v1.3"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomeOf(res, "v1.4.2").Result; got != TagInUse {
		t.Errorf("v1.4.2 = %s; it is the same image a release runs as v1.4", got)
	}
	if got := outcomeOf(res, "v1.3").Result; got != TagDeleted {
		t.Errorf("v1.3 = %s; another workspace's release must not protect this one", got)
	}
	if _, ok := reg.tags["v1.4"]; !ok {
		t.Error("the running image was deleted")
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, path, tag, digest string }{
		{"registry.example.com/ws_7/web:v1.4", "ws_7/web", "v1.4", ""},
		{"mb-registry:5000/ws_7/team/api:1", "ws_7/team/api", "1", ""},
		{"ws_7/web", "ws_7/web", "latest", ""},
		{"localhost/ws_7/web@sha256:abc", "ws_7/web", "", "sha256:abc"},
		{"registry.example.com/ws_7/web:v1@sha256:abc", "ws_7/web", "v1", "sha256:abc"},
	}
	for _, c := range cases {
		path, tag, digest := splitImageRef(c.ref)
		if path != c.path || tag != c.tag || digest != c.digest {
			t.Errorf("%s = (%q, %q, %q), want (%q, %q, %q)", c.ref, path, tag, digest, c.path, c.tag, c.digest)
		}
	}
}

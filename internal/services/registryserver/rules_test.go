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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

var ruleNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// agedRegistry serves ws_7/web, whose images carry a creation time in their config; a zero time means none.
func agedRegistry(t *testing.T, tags map[string]string, created map[string]time.Time) *Service {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/v2/ws_7/web/"
		path := r.URL.Path
		mu.Lock()
		defer mu.Unlock()
		switch {
		case path == "/v2/_catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"ws_7/web"}})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, prefix+"manifests/sha256:"):
			img := strings.TrimPrefix(path, prefix+"manifests/sha256:")
			for tag, i := range tags {
				if i == img {
					delete(tags, tag)
				}
			}
			w.WriteHeader(http.StatusAccepted)
		case path == prefix+"tags/list":
			list := []string{}
			for tag := range tags {
				list = append(list, tag)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": list})
		case strings.HasPrefix(path, prefix+"manifests/"):
			ref := strings.TrimPrefix(path, prefix+"manifests/")
			img := tags[ref]
			if img == "" {
				img = strings.TrimPrefix(ref, "sha256:")
			}
			if _, ok := created[img]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:"+img)
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"digest": "sha256:cfg-" + img, "size": 10}})
		case strings.HasPrefix(path, prefix+"blobs/sha256:cfg-"):
			img := strings.TrimPrefix(path, prefix+"blobs/sha256:cfg-")
			cfg := map[string]any{}
			if c := created[img]; !c.IsZero() {
				cfg["created"] = c
			}
			_ = json.NewEncoder(w).Encode(cfg)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &Service{reg: NewClient(srv.URL, nil)}
}

func days(n int) time.Time { return ruleNow.AddDate(0, 0, -n) }

func agedSvc(t *testing.T) *Service {
	return agedRegistry(t,
		map[string]string{"latest": "a", "v4": "a", "v3": "b", "v2": "c", "v1": "d", "mystery": "e"},
		map[string]time.Time{"a": days(1), "b": days(10), "c": days(40), "d": days(90), "e": {}},
	)
}

func TestSelectKeepNewestTreatsSiblingTagsAsOneImage(t *testing.T) {
	sel, err := agedSvc(t).SelectTags(context.Background(), 7, "web", SelectRule{KeepNewest: 2}, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tags, []string{"v1", "v2"}) || sel.Images != 2 {
		t.Fatalf("tags = %v (images %d); latest and v4 are one image, so the newest two are a and b", sel.Tags, sel.Images)
	}
	if !slices.Equal(sel.UnknownAge, []string{"mystery"}) {
		t.Errorf("unknown = %v; an image with no age is never picked", sel.UnknownAge)
	}
}

func TestSelectOlderThan(t *testing.T) {
	sel, err := agedSvc(t).SelectTags(context.Background(), 7, "web", SelectRule{OlderThanDays: 30}, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tags, []string{"v1", "v2"}) {
		t.Fatalf("tags = %v", sel.Tags)
	}
}

func TestSelectBothRulesMustHold(t *testing.T) {
	sel, err := agedSvc(t).SelectTags(context.Background(), 7, "web", SelectRule{KeepNewest: 3, OlderThanDays: 5}, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tags, []string{"v1"}) {
		t.Fatalf("tags = %v; only d is outside the newest three and older than five days", sel.Tags)
	}
}

func TestSelectLeavesInUseImagesAndCountsThemAsKept(t *testing.T) {
	svc := agedSvc(t)
	svc.SetCatalog(stubCatalog{digests: map[string]bool{"sha256:a": true, "sha256:c": true}})
	sel, err := svc.SelectTags(context.Background(), 7, "web", SelectRule{KeepNewest: 1}, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tags, []string{"v1", "v3"}) {
		t.Fatalf("tags = %v; a (in use) holds the one kept slot, and the oldest image comes first", sel.Tags)
	}
	if !slices.Equal(sel.InUse, []string{"v2"}) {
		t.Errorf("in use = %v", sel.InUse)
	}
}

func TestSelectPrefersTheBuildRecord(t *testing.T) {
	svc := agedSvc(t)
	built := days(200)
	svc.SetCatalog(stubCatalog{
		digests: map[string]bool{},
		rows:    map[string]models.Image{"sha256:e": {Digest: "sha256:e", BuiltAt: &built}},
	})
	sel, err := svc.SelectTags(context.Background(), 7, "web", SelectRule{OlderThanDays: 100}, ruleNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tags, []string{"mystery"}) {
		t.Fatalf("tags = %v; Miabi built e 200 days ago even though its config records nothing", sel.Tags)
	}
}

func TestSelectNeedsARule(t *testing.T) {
	svc := agedSvc(t)
	if _, err := svc.SelectTags(context.Background(), 7, "web", SelectRule{}, ruleNow); !errors.Is(err, ErrNoRule) {
		t.Errorf("no rule: %v", err)
	}
	if _, err := svc.SelectTags(context.Background(), 7, "web", SelectRule{KeepNewest: -1}, ruleNow); !errors.Is(err, ErrBadRule) {
		t.Errorf("negative: %v", err)
	}
}

func TestDeleteRepositoryIsAllOrNothing(t *testing.T) {
	svc, reg := memSvc(t, map[string]string{"v1": "sha256:a", "v2": "sha256:b", "latest": "sha256:b"})
	svc.SetCatalog(stubCatalog{digests: map[string]bool{"sha256:a": true}})
	_, err := svc.DeleteRepository(context.Background(), 7, "web")
	var inUse *InUseError
	if !errors.As(err, &inUse) || !slices.Equal(inUse.Tags, []string{"v1"}) || !errors.Is(err, ErrTagInUse) {
		t.Fatalf("err = %v; one image in use keeps the whole repository", err)
	}
	if len(reg.deletes) != 0 {
		t.Fatalf("deletes = %v", reg.deletes)
	}

	svc.SetCatalog(stubCatalog{digests: map[string]bool{}})
	res, err := svc.DeleteRepository(context.Background(), 7, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.tags) != 0 || len(res.Removed) != 3 {
		t.Fatalf("left = %v, removed = %v", reg.tags, res.Removed)
	}
}

func TestChunk(t *testing.T) {
	got := chunk([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 || len(got[2]) != 1 {
		t.Fatalf("chunks = %v", got)
	}
}

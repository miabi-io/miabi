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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// image is a manifest made of blobs; tags point at images by name.
type image map[string]int64

// nsRegistry serves ws_7 with images shared across repositories, as base layers are in practice.
func nsRegistry(t *testing.T, repos map[string]map[string]string, images map[string]image) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v2/")
		if path == "_catalog" {
			names := []string{}
			for repo := range repos {
				names = append(names, "ws_7/"+repo)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": names})
			return
		}
		for repo, tags := range repos {
			base := "ws_7/" + repo + "/"
			if !strings.HasPrefix(path, base) {
				continue
			}
			rest := strings.TrimPrefix(path, base)
			if rest == "tags/list" {
				list := []string{}
				for tag := range tags {
					list = append(list, tag)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"tags": list})
				return
			}
			ref := strings.TrimPrefix(rest, "manifests/")
			name := tags[ref]
			if name == "" {
				name = strings.TrimPrefix(ref, "sha256:")
			}
			img, ok := images[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			layers := []map[string]any{}
			for d, sz := range img {
				layers = append(layers, map[string]any{"digest": d, "size": sz})
			}
			w.Header().Set("Docker-Content-Digest", "sha256:"+name)
			_ = json.NewEncoder(w).Encode(map[string]any{"layers": layers})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func usageSvc(t *testing.T) *Service {
	t.Helper()
	srv := nsRegistry(t,
		map[string]map[string]string{
			"web": {"latest": "web2", "v2": "web2", "v1": "web1"},
			"api": {"v1": "api1"},
		},
		map[string]image{
			"web1": {"base": 1000, "web-v1": 200},
			"web2": {"base": 1000, "web-v2": 300},
			"api1": {"base": 1000, "api": 50},
		})
	return &Service{reg: NewClient(srv.URL, nil), usage: newUsageCache()}
}

func TestUsageDedupesAcrossRepositories(t *testing.T) {
	u, err := usageSvc(t).Usage(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if u.TotalBytes != 1000+200+300+50 {
		t.Fatalf("total = %d; the shared base layer counts once", u.TotalBytes)
	}
	byName := map[string]RepoUsage{}
	for _, r := range u.Repositories {
		byName[r.Name] = r
	}
	web, api := byName["web"], byName["api"]
	if web.Bytes != 1500 || web.ExclusiveBytes != 500 || web.Tags != 3 {
		t.Errorf("web = %+v; base is shared with api, so it is not web's alone", web)
	}
	if api.Bytes != 1050 || api.ExclusiveBytes != 50 {
		t.Errorf("api = %+v", api)
	}
	if u.Repositories[0].Name != "web" {
		t.Errorf("largest first, got %s", u.Repositories[0].Name)
	}
}

func TestFreedBytesCountsOnlyWhatNothingElseUses(t *testing.T) {
	svc := usageSvc(t)
	idx, err := svc.rebuildIndex(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.freedBy("web", map[string]bool{"sha256:web1": true}); got != 200 {
		t.Errorf("freed = %d; base stays for web2 and api1", got)
	}
	if got := idx.freedBy("web", map[string]bool{"sha256:web1": true, "sha256:web2": true}); got != 500 {
		t.Errorf("freed = %d; base stays for api1", got)
	}
}

func TestQuotaReadsTheIndex(t *testing.T) {
	svc := usageSvc(t)
	now := time.Now()
	svc.usage.nowFn = func() time.Time { return now }
	svc.usage.quotaMB, svc.usage.quotaAt = 1, now
	svc.usage.index[7] = &usageIndex{
		tags:  map[string]map[string]string{"web": {"v1": "sha256:big"}},
		blobs: map[string]map[string]int64{"sha256:big": {"layer": 2 << 20}},
		at:    now,
	}
	if !svc.quotaExceeded(7) {
		t.Fatal("2 MiB against a 1 MiB quota must refuse a push")
	}
	svc.usage.index[7].at = now.Add(-usageTTL)
	svc.usage.refresh[7] = true
	if svc.quotaExceeded(7) {
		t.Fatal("a stale index fails open, so a push never waits on a walk")
	}
}

func TestIndexStalenessAndDebounce(t *testing.T) {
	svc := usageSvc(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	svc.usage.nowFn = func() time.Time { return now }
	if _, err := svc.rebuildIndex(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	svc.usage.mu.Lock()
	svc.usage.refresh[7] = true // hold off the background refresh so the test reads the flags only
	svc.usage.mu.Unlock()

	if _, stale := svc.cachedIndex(7); stale {
		t.Fatal("a fresh index read as stale")
	}
	svc.markDirty(7)
	now = now.Add(time.Second)
	svc.markDirty(7)
	if _, stale := svc.cachedIndex(7); stale {
		t.Fatal("a push inside the debounce must not trigger another walk")
	}
	now = now.Add(usageDebounce)
	if _, stale := svc.cachedIndex(7); !stale {
		t.Fatal("after the debounce a pushed-to namespace is stale")
	}
	svc.usage.dirty = map[uint]time.Time{}
	now = now.Add(usageTTL)
	if _, stale := svc.cachedIndex(7); !stale {
		t.Fatal("past the TTL the index is stale")
	}
}

func TestDeleteDryRunReportsFreedBytes(t *testing.T) {
	svc := usageSvc(t)
	res, err := svc.DeleteTags(context.Background(), 7, "web", []string{"v1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedBytes != 200 {
		t.Fatalf("freed = %d, want 200", res.FreedBytes)
	}
}

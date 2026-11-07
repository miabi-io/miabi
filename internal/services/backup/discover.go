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

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dbenvelope"
)

// The bucket is the authority: after losing a workspace, everything ever taken is still in object storage.
type DiscoveredSet struct {
	Ref       string `json:"ref"`
	Instance  string `json:"instance"`
	Engine    string `json:"engine"`
	Version   string `json:"version,omitempty"`
	CreatedAt string `json:"created_at"`
	SizeBytes int64  `json:"size_bytes"`
	Encrypted bool   `json:"encrypted"`
	// Prefix is where the set's objects live, so a restore knows where to look.
	Prefix string `json:"prefix"`

	Artifacts []DiscoveredArtifact `json:"artifacts"`

	// Known sets are still listed so the view covers the whole bucket.
	Known bool `json:"known"`
	SetID uint `json:"set_id,omitempty"`

	// False when sealed with a different secret, worth knowing before a restore is attempted.
	Openable bool `json:"openable"`
	// Reason says why the set cannot be used, when it cannot.
	Reason string `json:"reason,omitempty"`
}

// DiscoveredArtifact is one database's dump within a discovered set.
type DiscoveredArtifact struct {
	Database  string `json:"database"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Encrypted bool   `json:"encrypted"`
	// Retention and lifecycle rules act on the bucket after the descriptor was written.
	Present bool `json:"present"`
}

// DiscoverSets reads only cleartext descriptors, so no passphrase is needed to see what exists.
func (s *Service) DiscoverSets(ctx context.Context, cfg *S3Config, basePath, passphrase string) ([]DiscoveredSet, error) {
	store, err := blobStoreFor(cfg)
	if err != nil {
		return nil, err
	}
	base := strings.Trim(strings.TrimSpace(basePath), "/")
	objects, err := store.List(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", cfg.Bucket, err)
	}

	// Index once: re-listing per set would cost a round trip per recovery point.
	present := make(map[string]int64, len(objects))
	var infoKeys []string
	for _, o := range objects {
		present[o.Key] = o.Size
		if path.Base(o.Key) == SetInfoObject {
			infoKeys = append(infoKeys, o.Key)
		}
	}

	known, err := s.knownRefs()
	if err != nil {
		return nil, err
	}

	out := make([]DiscoveredSet, 0, len(infoKeys))
	for _, key := range infoKeys {
		set, derr := describeSet(ctx, store.GetBytes, key, present, passphrase)
		if derr != nil {
			// One unreadable descriptor must not hide the rest of the bucket.
			logger.Warn("skipping an unreadable recovery point descriptor", "key", key, "error", derr)
			continue
		}
		if id, ok := known[set.Ref]; ok {
			set.Known, set.SetID = true, id
		}
		out = append(out, set)
	}
	// Newest first, matching how the console lists sets it already knows about.
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// Narrowed from blob.Store so the descriptor judgements are testable without a bucket.
type fetcher func(ctx context.Context, key string) ([]byte, error)

// describeSet reads one descriptor and checks the artifacts it claims.
func describeSet(ctx context.Context, fetch fetcher, key string, present map[string]int64, passphrase string) (DiscoveredSet, error) {
	body, err := fetch(ctx, key)
	if err != nil {
		return DiscoveredSet{}, err
	}
	var info SetInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return DiscoveredSet{}, fmt.Errorf("decode: %w", err)
	}
	if info.Schema != SetInfoSchema {
		return DiscoveredSet{}, fmt.Errorf("descriptor schema %d is not supported by this build (expected %d)", info.Schema, SetInfoSchema)
	}
	if info.Ref == "" {
		return DiscoveredSet{}, fmt.Errorf("descriptor names no recovery point")
	}

	prefix := path.Dir(key)
	out := DiscoveredSet{
		Ref:       info.Ref,
		Instance:  info.Instance,
		Engine:    info.Engine,
		Version:   info.Version,
		CreatedAt: info.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		SizeBytes: info.SizeBytes,
		Encrypted: info.Encrypted,
		Prefix:    prefix,
	}
	if out.Instance == "" {
		// Older descriptors did not record it; the prefix layout still does.
		out.Instance = path.Base(path.Dir(prefix))
	}

	missing := 0
	for _, a := range info.Artifacts {
		size, ok := present[path.Join(prefix, a.Filename)]
		if !ok {
			missing++
		} else if a.SizeBytes == 0 {
			a.SizeBytes = size
		}
		out.Artifacts = append(out.Artifacts, DiscoveredArtifact{
			Database: a.Database, Filename: a.Filename,
			SizeBytes: a.SizeBytes, Encrypted: a.Encrypted, Present: ok,
		})
	}

	switch {
	case len(info.Artifacts) == 0:
		out.Reason = "the descriptor lists no artifacts"
	case missing == len(info.Artifacts):
		out.Reason = "none of this recovery point's artifacts are in the bucket any more"
	case missing > 0:
		out.Reason = fmt.Sprintf("%d of %d artifacts are missing from the bucket", missing, len(info.Artifacts))
	}

	// Answer whether the passphrase opens it here rather than at the point of no return.
	switch {
	case !info.Encrypted || info.Envelope == "":
		out.Openable = out.Reason == ""
	case passphrase == "":
		out.Openable = false
		if out.Reason == "" {
			out.Reason = "encrypted; set the workspace backup passphrase to read it"
		}
	default:
		if _, oerr := dbenvelope.Open(info.Envelope, passphrase); oerr != nil {
			out.Openable = false
			if out.Reason == "" {
				out.Reason = "sealed with a different passphrase than this workspace holds"
			}
		} else {
			out.Openable = out.Reason == ""
		}
	}
	return out, nil
}

// knownRefs maps the refs this workspace already has to their row ids.
func (s *Service) knownRefs() (map[string]uint, error) {
	if s.sets == nil {
		return map[string]uint{}, nil
	}
	rows, err := s.sets.ListAllRefs()
	if err != nil {
		return nil, err
	}
	return rows, nil
}

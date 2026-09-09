// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// DiscoveredSet is a recovery point found in the bucket.
//
// The bucket is the authority, not this workspace's database: after losing a
// workspace — or moving to a new install — the sets Miabi knows about are whatever
// its own rows contain, while everything ever taken is still in object storage.
// Discovery makes those reachable again.
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

	// Known is true when this workspace already has the set in its own database.
	// Those need no adopting; they are listed so the view is the whole bucket
	// rather than only the unfamiliar parts of it.
	Known bool `json:"known"`
	SetID uint `json:"set_id,omitempty"`

	// Openable reports that the workspace's current passphrase opens this set's
	// envelope. False on a set sealed with a different secret — which is worth
	// knowing before a restore is attempted, not during one.
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
	// Present reports whether the object is actually in the bucket. A descriptor
	// records what happened when the backup ran; retention and lifecycle rules act
	// on the bucket afterwards.
	Present bool `json:"present"`
}

// DiscoverSets lists the recovery points under a workspace's database backup path.
// It reads only the cleartext descriptors, so no passphrase is needed to see what
// exists — which is the whole point of keeping them readable.
//
// passphrase is optional: supplied, it is used to report whether each set can
// actually be opened, without which the list says what is there but not what is
// usable.
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

	// Index the objects once: presence is checked per artifact, and re-listing per
	// set would be one round trip per recovery point.
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

// fetcher reads an object's bytes. Narrowed from blob.Store so the descriptor
// logic — which is where the judgements live — is testable without a bucket.
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

	// Whether the current passphrase opens it is the question a restore asks first,
	// so answer it here rather than at the point of no return.
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

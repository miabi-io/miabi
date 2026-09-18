// SPDX-FileCopyrightText: 2026 Tresor Kasenda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/settings"
)

// Sweep is the monthly automatic reclaim, run from a cron task rather than an admin action. On every
// node it prunes dangling images (any age — always safe, since Docker only ever removes images no
// container references) and, separately, any non-dangling image that no container references AND is
// older than the configured retention — except the images an app's guard protects (see
// protectedImageRefs). Best-effort throughout: a failure on one node or app is logged and skipped, so
// it never aborts the rest of the sweep. A no-op if unwired (SetImagePrune never called) or disabled.
func (s *Service) Sweep(ctx context.Context) error {
	if s.servers == nil || s.releases == nil || s.pruneSettings == nil {
		return nil
	}
	if !s.pruneSettings.Bool(settings.KeyImagePruneEnabled, true) {
		return nil
	}
	retentionDays := s.pruneSettings.Int(settings.KeyImagePruneRetentionDays, 30)
	if retentionDays <= 0 {
		retentionDays = 30
	}
	keepLast := s.pruneSettings.Int(settings.KeyImagePruneKeepLast, 3)
	if keepLast < 0 {
		keepLast = 0
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	servers, err := s.servers.List()
	if err != nil {
		logger.Warn("image prune sweep: list nodes failed", "error", err)
		return nil
	}

	var nodesOK, danglingDeleted, agedDeleted int
	var bytesReclaimed int64
	for _, srv := range servers {
		dc, cerr := s.clients.For(srv.ID)
		if cerr != nil {
			logger.Warn("image prune sweep: no client for node", "server_id", srv.ID, "error", cerr)
			continue
		}

		if rep, perr := dc.PruneImages(ctx, docker.PruneImagesOptions{Dangling: true}); perr != nil {
			logger.Warn("image prune sweep: dangling prune failed", "server_id", srv.ID, "error", perr)
		} else {
			danglingDeleted += len(rep.ItemsDeleted)
			bytesReclaimed += rep.SpaceReclaimed
		}

		protected, perr := s.protectedImageRefs(srv.ID, keepLast)
		if perr != nil {
			logger.Warn("image prune sweep: resolve protected images failed", "server_id", srv.ID, "error", perr)
			continue
		}
		imgs, ierr := dc.ListImages(ctx)
		if ierr != nil {
			logger.Warn("image prune sweep: list images failed", "server_id", srv.ID, "error", ierr)
			continue
		}
		for _, im := range imgs {
			// Dangling is already handled above; Containers != 0 means Docker itself
			// still has a container referencing it — never touch that regardless of age.
			if im.Dangling || im.Containers != 0 {
				continue
			}
			if time.Unix(im.Created, 0).After(cutoff) {
				continue // not old enough yet
			}
			if imageIsProtected(im, protected) {
				continue
			}
			ref := im.ID
			if len(im.RepoTags) > 0 {
				ref = im.RepoTags[0]
			}
			if rerr := dc.RemoveImage(ctx, ref, false); rerr != nil {
				logger.Warn("image prune sweep: remove image failed", "server_id", srv.ID, "ref", ref, "error", rerr)
				continue
			}
			agedDeleted++
			bytesReclaimed += im.Size
		}
		nodesOK++
	}
	logger.Info("image prune sweep complete",
		"nodes", nodesOK, "dangling_deleted", danglingDeleted, "aged_deleted", agedDeleted, "bytes_reclaimed", bytesReclaimed)
	return nil
}

// protectedImageRefs is the set of image tags/digests Sweep must never remove for a node's apps: each
// app's keepLast most recent releases, plus its active release and any pinned (rollback-target) release
// regardless of position — so a rollback target always survives no matter how old it is.
func (s *Service) protectedImageRefs(serverID uint, keepLast int) (map[string]bool, error) {
	apps, err := s.apps.ListByServer(serverID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, a := range apps {
		releases, rerr := s.releases.ListByApp(a.ID)
		if rerr != nil {
			logger.Warn("image prune sweep: list releases failed", "app_id", a.ID, "error", rerr)
			continue
		}
		for i, rel := range releases {
			if i >= keepLast && !rel.Active && !rel.Pinned {
				continue
			}
			if rel.Image != "" {
				out[rel.Image] = true
			}
			if rel.Digest != "" {
				out[rel.Digest] = true
			}
		}
	}
	return out, nil
}

// imageIsProtected reports whether any tag or digest of im is in the protected set.
func imageIsProtected(im docker.Image, protected map[string]bool) bool {
	for _, t := range im.RepoTags {
		if protected[t] {
			return true
		}
	}
	for _, d := range im.RepoDigests {
		if protected[d] {
			return true
		}
	}
	return false
}

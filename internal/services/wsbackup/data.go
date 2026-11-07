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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

const (
	volumeMount     = "/data"
	defaultVolImage = platformimage.DefaultVolumeBkup
)

var volArtifactRe = backup.VolumeArtifactRe

func (s *Service) volImage() string {
	if s.Images != nil {
		if r := s.Images.Ref(platformimage.KeyBackupVolume); r != "" {
			return r
		}
	}
	return defaultVolImage
}

// Encryption is read off the name: a helper too old to support it writes plaintext.
func (s *Service) archiveVolume(ctx context.Context, v *models.Volume, cfg *backup.S3Config, path, passphrase string) (string, bool, error) {
	dc, err := s.Clients.For(v.ServerID)
	if err != nil {
		return "", false, err
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return "", false, fmt.Errorf("pull %s: %w", image, err)
	}
	env := append(backup.S3Env(cfg), backup.IndexEnv(v.Name))
	if passphrase != "" {
		env = append(env, "GPG_PASSPHRASE="+passphrase)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   oneShotName("mb-wsb-volbkup", v.ID),
		Image:  image,
		Env:    env,
		Cmd:    []string{"backup", "--storage", "s3", "--remote-path", path, "--name", v.Name},
		Mounts: map[string]string{v.DockerName: volumeMount},
		Labels: map[string]string{
			docker.LabelWorkspace: fmt.Sprintf("%d", v.WorkspaceID),
			docker.LabelVolume:    fmt.Sprintf("%d", v.ID),
		},
	})
	if err != nil {
		return "", false, err
	}
	if exit != 0 {
		return "", false, fmt.Errorf("volume archive exited %d: %s", exit, tail(out))
	}
	name, encrypted, err := backup.ArtifactName(out, volArtifactRe)
	if err != nil {
		return "", false, err
	}
	if passphrase != "" && !encrypted {
		return name, false, fmt.Errorf("volume %s was archived UNENCRYPTED: this %s image does not support encryption", v.Name, image)
	}
	return name, encrypted, nil
}

// The volume already exists: the restore created it from the state file before fetching data.
func (s *Service) restoreVolumeArchive(ctx context.Context, v *models.Volume, cfg *backup.S3Config, path, file, passphrase string) error {
	dc, err := s.Clients.For(v.ServerID)
	if err != nil {
		return err
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	env := backup.S3Env(cfg)
	if passphrase != "" {
		env = append(env, "GPG_PASSPHRASE="+passphrase)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   oneShotName("mb-wsb-volrestore", v.ID),
		Image:  image,
		Env:    env,
		Cmd:    []string{"restore", "--storage", "s3", "--remote-path", path, "--file", file},
		Mounts: map[string]string{v.DockerName: volumeMount},
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("volume restore exited %d: %s", exit, tail(out))
	}
	return nil
}

// A timestamp isn't enough: two runs in one clock tick collide, and Docker refuses the second name.
func oneShotName(prefix string, id uint) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Randomness fails only in far worse situations; fall back to the clock rather than fail the backup.
		return fmt.Sprintf("%s-%d-%d", prefix, id, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d-%s", prefix, id, hex.EncodeToString(b))
}

func tail(out string) string {
	const max = 2000
	if len(out) <= max {
		return out
	}
	return "…" + out[len(out)-max:]
}

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

const (
	volumeMount     = "/data"
	defaultVolImage = "jkaninda/volume-bkup:latest"
)

// volume-bkup names archives "<name>_YYYYMMDD_HHMMSS.tar.gz", plus ".gpg" when a
// passphrase is supplied. Without matching the encrypted form, a run would
// complete having recorded a filename that was never written.
var volArtifactRe = regexp.MustCompile(`[\w.\-]+\.tar\.gz(?:\.gpg)?`)

// volImage is the volume archive helper (deployment-config catalog, else the
// ecosystem default).
func (s *Service) volImage() string {
	if s.Images != nil {
		if r := s.Images.Ref(platformimage.KeyBackupVolume); r != "" {
			return r
		}
	}
	return defaultVolImage
}

// archiveVolume writes one volume's contents into the bundle's branch and returns the artifact name the helper
// actually uploaded, and whether it is encrypted. Encryption is read back off the name rather than assumed: a
// helper too old to support it writes plaintext, and a bundle claiming otherwise would fail to restore.
func (s *Service) archiveVolume(ctx context.Context, v *models.Volume, cfg *backup.S3Config, path, passphrase string) (string, bool, error) {
	dc, err := s.Clients.For(v.ServerID)
	if err != nil {
		return "", false, err
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return "", false, fmt.Errorf("pull %s: %w", image, err)
	}
	env := backup.S3Env(cfg)
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

// restoreVolumeArchive extracts a bundle's archive back into a volume,
// overwriting what is there. The volume already exists — the restore created it
// from the state file before any data was fetched.
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

// oneShotName builds a unique container name for a helper run. The random suffix is what keeps a retry from
// colliding with the container its predecessor left behind. A timestamp is not enough: two runs started in the
// same clock tick would produce the same name, and Docker refuses the second.
func oneShotName(prefix string, id uint) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Randomness is unavailable only in situations far worse than a name
		// collision; fall back to the clock rather than failing the backup.
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

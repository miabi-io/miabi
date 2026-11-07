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

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

// SetImageResolver wires the deployment-config resolver for the volume helper image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

func (s *Service) helperImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyHelper); r != "" {
			return r
		}
	}
	return "busybox:1.36"
}

// fileMount is where the helper container mounts the target volume.
const fileMount = "/vol"

// Config volumes are small, but a stray data volume could hold thousands of files.
const maxListedFiles = 5000

// ErrFileNotFound is returned when a requested path is absent from the volume.
var ErrFileNotFound = errors.New("file not found in volume")

// VolumeFile is one entry in a volume's filesystem listing.
type VolumeFile struct {
	Path    string `json:"path"`     // path relative to the volume root, e.g. "config/app.yaml"
	Size    int64  `json:"size"`     // bytes (0 for directories)
	ModTime int64  `json:"mod_time"` // unix seconds
	IsDir   bool   `json:"is_dir"`
}

func (s *Service) ListFiles(ctx context.Context, v *models.Volume) ([]VolumeFile, error) {
	dc, err := s.clients.For(v.ServerID)
	if err != nil {
		return nil, err
	}
	image := s.helperImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return nil, fmt.Errorf("pull helper image: %w", err)
	}
	// Names are relative to the volume root because find runs with cwd = /vol.
	script := "cd " + fileMount + " 2>/dev/null && find . -mindepth 1 -exec stat -c '%F|%s|%Y|%n' {} + 2>/dev/null"
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   fmt.Sprintf("mb-vls-%d", v.ID),
		Image:  image,
		Cmd:    []string{"sh", "-c", script},
		Mounts: map[string]string{v.DockerName: fileMount},
	})
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("list volume files exited %d: %s", exit, strings.TrimSpace(out))
	}
	return parseFileListing(out), nil
}

// parseFileListing turns the helper's "type|size|mtime|name" lines into entries.
func parseFileListing(out string) []VolumeFile {
	files := make([]VolumeFile, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		mtime, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		name := strings.TrimPrefix(parts[3], "./")
		if name == "" {
			continue
		}
		isDir := strings.Contains(parts[0], "directory")
		if isDir {
			size = 0
		}
		files = append(files, VolumeFile{Path: name, Size: size, ModTime: mtime, IsDir: isDir})
		if len(files) >= maxListedFiles {
			logger.Warn("volume listing truncated", "volume", name, "limit", maxListedFiles)
			break
		}
	}
	return files
}

func (s *Service) UploadFile(ctx context.Context, v *models.Volume, subdir, filename string, content io.Reader, size int64) (string, error) {
	name := path.Base(strings.TrimSpace(filename))
	if name == "" || name == "." || name == "/" {
		return "", errors.New("a file name is required")
	}
	dir, err := cleanRelPath(subdir)
	if err != nil {
		return "", err
	}
	dest := name
	if dir != "" {
		dest = path.Join(dir, name)
	}
	dc, err := s.clients.For(v.ServerID)
	if err != nil {
		return "", err
	}
	image := s.helperImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return "", fmt.Errorf("pull helper image: %w", err)
	}
	// Ensure the target sub-directory exists before landing the file.
	if dir != "" {
		exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
			Name:   fmt.Sprintf("mb-vmk-%d", v.ID),
			Image:  image,
			Cmd:    []string{"sh", "-c", "mkdir -p " + fileMount + "/" + dir},
			Mounts: map[string]string{v.DockerName: fileMount},
		})
		if err != nil || exit != 0 {
			return "", fmt.Errorf("create directory exited %d: %s", exit, strings.TrimSpace(out))
		}
	}
	if err := dc.CopyToVolume(ctx, v.DockerName, image, dest, content, size); err != nil {
		return "", err
	}
	return dest, nil
}

// DownloadFile streams a single file out of a volume.
func (s *Service) DownloadFile(ctx context.Context, v *models.Volume, rel string) (io.ReadCloser, int64, error) {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return nil, 0, err
	}
	if clean == "" {
		return nil, 0, errors.New("a file path is required")
	}
	dc, err := s.clients.For(v.ServerID)
	if err != nil {
		return nil, 0, err
	}
	rc, size, err := dc.CopyFileFromVolume(ctx, v.DockerName, s.helperImage(), clean)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return nil, 0, ErrFileNotFound
		}
		return nil, 0, err
	}
	return rc, size, nil
}

// DeleteFile removes a file or directory (recursively) from a volume.
func (s *Service) DeleteFile(ctx context.Context, v *models.Volume, rel string) error {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return err
	}
	if clean == "" {
		return errors.New("a file path is required")
	}
	dc, err := s.clients.For(v.ServerID)
	if err != nil {
		return err
	}
	image := s.helperImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull helper image: %w", err)
	}
	target := fileMount + "/" + clean
	exit, _, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   fmt.Sprintf("mb-vrm-%d", v.ID),
		Image:  image,
		Cmd:    []string{"sh", "-c", fmt.Sprintf("test -e %q && rm -rf %q", target, target)},
		Mounts: map[string]string{v.DockerName: fileMount},
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		return ErrFileNotFound
	}
	return nil
}

func cleanRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", nil
	}
	clean := path.Clean(p)
	if clean == ".." || clean == "." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid path")
	}
	return clean, nil
}

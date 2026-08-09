// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config owns workspace configuration files: named text payloads,
// encrypted at rest, projected into containers as read-only files.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrInvalidName = errors.New("invalid config name")
	ErrNameTaken   = errors.New("config name already exists")
	ErrNotFound    = errors.New("config not found")
	ErrNoData      = errors.New("config must declare at least one file")
	ErrInUse       = errors.New("config is mounted by an application")
	ErrKeyNotFound = errors.New("config has no such file")
)

// Limits mirror internal/declarative so a config rejected by a manifest is also
// rejected over the API. The per-file cap is what matters against Docker's
// 500 KB config-object limit, since projection is always per file.
const (
	MaxFileBytes    = 256 * 1024
	MaxTotalBytes   = 512 * 1024
	DefaultFileMode = "0644"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	keyRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*)?(/[A-Za-z0-9._-]+)*$`)
)

// Consumers resolves which apps mount a config and redeploys them. Implemented by
// the application service; nil in processes that only read configs.
type Consumers interface {
	AppsMountingConfig(workspaceID, configID uint) ([]models.Application, error)
	AutoRedeploy(app *models.Application) (*models.Deployment, error)
}

// ProjectedFile is one file as it lands in a container. Every projection reduces
// to this, whether the mount named a single key or a directory prefix.
type ProjectedFile struct {
	Path    string
	Content string
	Mode    string
}

// FileDigest fingerprints a projected file over content *and* mode: two mounts of
// the same key at different modes are different files, and a content-only digest
// would collide on one materialized path.
func (f ProjectedFile) FileDigest() string {
	sum := sha256.Sum256([]byte(f.Content + "\x00" + f.Mode))
	return hex.EncodeToString(sum[:])
}

type Service struct {
	repo      *repositories.ConfigRepository
	consumers Consumers
}

func NewService(repo *repositories.ConfigRepository) *Service { return &Service{repo: repo} }

func (s *Service) SetConsumers(c Consumers) { s.consumers = c }

func (s *Service) List(workspaceID uint) ([]models.Config, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) ListPaged(workspaceID uint, search string, managed *bool, limit, offset int) ([]models.Config, int64, error) {
	return s.repo.ListByWorkspacePaged(workspaceID, search, managed, limit, offset)
}

func (s *Service) Get(workspaceID, id uint) (*models.Config, error) {
	c, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

func (s *Service) GetByName(workspaceID uint, name string) (*models.Config, error) {
	c, err := s.repo.FindByName(workspaceID, name)
	if err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// Input carries the mutable fields of a config.
type Input struct {
	Name        string
	DisplayName string
	Description string
	Data        map[string]string
	Mode        string
	Sensitive   bool
	Delimiters  []string
}

func (s *Service) Create(workspaceID uint, in Input, userID *uint) (*models.Config, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		return nil, ErrInvalidName
	}
	if err := ValidateData(in.Data); err != nil {
		return nil, err
	}
	if in.Mode == "" {
		in.Mode = DefaultFileMode
	}
	if err := ValidateMode(in.Mode); err != nil {
		return nil, err
	}
	if exists, err := s.repo.ExistsByName(workspaceID, in.Name); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrNameTaken
	}
	enc, digest, err := encode(workspaceID, in.Data)
	if err != nil {
		return nil, err
	}
	cfg := &models.Config{
		WorkspaceID: workspaceID, Name: in.Name, DisplayName: in.DisplayName,
		Description: in.Description, DataEnc: enc, Digest: digest, Mode: in.Mode,
		Sensitive: in.Sensitive, Delimiters: in.Delimiters, Version: 1, UpdatedByID: userID,
	}
	if err := s.repo.Create(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Update replaces the config's content and bumps its version, then redeploys the
// apps mounting it. That redeploy is the imperative half of change propagation:
// an edit over the API never runs an apply, so nothing else would restart them.
func (s *Service) Update(workspaceID, id uint, in Input, userID *uint) (*models.Config, error) {
	cfg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	changed := false
	if in.Data != nil {
		if err := ValidateData(in.Data); err != nil {
			return nil, err
		}
		enc, digest, eerr := encode(workspaceID, in.Data)
		if eerr != nil {
			return nil, eerr
		}
		if digest != cfg.Digest {
			cfg.DataEnc, cfg.Digest, changed = enc, digest, true
			cfg.Version++
		}
	}
	if in.Mode != "" && in.Mode != cfg.Mode {
		if err := ValidateMode(in.Mode); err != nil {
			return nil, err
		}
		cfg.Mode, changed = in.Mode, true
	}
	if in.DisplayName != "" {
		cfg.DisplayName = in.DisplayName
	}
	if in.Description != "" {
		cfg.Description = in.Description
	}
	if in.Delimiters != nil {
		cfg.Delimiters = in.Delimiters
	}
	cfg.UpdatedByID = userID
	if err := s.repo.Update(cfg); err != nil {
		return nil, err
	}
	if changed {
		s.fanOut(cfg)
	}
	return cfg, nil
}

// UpsertOwned creates or updates a config owned by another resource (a template
// install, for example), so its lifecycle follows the owner.
func (s *Service) UpsertOwned(workspaceID uint, ownerKind string, ownerID uint, in Input) (*models.Config, error) {
	existing, err := s.repo.FindByName(workspaceID, in.Name)
	if err == nil {
		existing.Managed, existing.OwnerKind, existing.OwnerID = true, ownerKind, ownerID
		return s.Update(workspaceID, existing.ID, in, nil)
	}
	cfg, err := s.Create(workspaceID, in, nil)
	if err != nil {
		return nil, err
	}
	cfg.Managed, cfg.OwnerKind, cfg.OwnerID = true, ownerKind, ownerID
	if err := s.repo.Update(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Delete refuses while an application still mounts the config, so a deploy can
// never lose a file out from under a running app.
func (s *Service) Delete(workspaceID, id uint) error {
	cfg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if s.consumers != nil {
		apps, aerr := s.consumers.AppsMountingConfig(workspaceID, cfg.ID)
		if aerr != nil {
			return aerr
		}
		if len(apps) > 0 {
			return fmt.Errorf("%w: %s", ErrInUse, apps[0].Name)
		}
	}
	return s.repo.Delete(cfg.ID)
}

// DeleteOwned removes every config owned by a resource being torn down.
func (s *Service) DeleteOwned(workspaceID uint, ownerKind string, ownerID uint) error {
	rows, err := s.repo.ListByOwner(workspaceID, ownerKind, ownerID)
	if err != nil {
		return err
	}
	for i := range rows {
		if derr := s.repo.Delete(rows[i].ID); derr != nil {
			return derr
		}
	}
	return nil
}

// Usage lists the applications mounting a config.
func (s *Service) Usage(workspaceID, id uint) ([]models.Application, error) {
	if s.consumers == nil {
		return nil, nil
	}
	cfg, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.consumers.AppsMountingConfig(workspaceID, cfg.ID)
}

// Data decrypts a config's files. Callers handling a sensitive config must not
// return this to an unprivileged reader.
func (s *Service) Data(cfg *models.Config) (map[string]string, error) {
	if cfg.DataEnc == "" {
		return map[string]string{}, nil
	}
	raw, err := crypto.Decrypt(cfg.DataEnc)
	if err != nil {
		return nil, err
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}
	return data, nil
}

// Project expands a mount into the files it lands in the container. A mount
// naming a key projects that one file at Path; without a key every file is
// projected under Path as a directory prefix. This is the only place path
// expansion happens.
func (s *Service) Project(cfg *models.Config, mount models.AppMount) ([]ProjectedFile, error) {
	data, err := s.Data(cfg)
	if err != nil {
		return nil, err
	}
	return s.project(cfg, data, mount)
}

// project is Project over already-decrypted data, so callers holding the files
// (and tests) do not decrypt twice.
func (s *Service) project(cfg *models.Config, data map[string]string, mount models.AppMount) ([]ProjectedFile, error) {
	mode := mount.Mode
	if mode == "" {
		mode = cfg.Mode
	}
	if mode == "" {
		mode = DefaultFileMode
	}
	if mount.ConfigKey != "" {
		content, ok := data[mount.ConfigKey]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, mount.ConfigKey)
		}
		return []ProjectedFile{{Path: mount.Path, Content: content, Mode: mode}}, nil
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ProjectedFile, 0, len(keys))
	for _, k := range keys {
		out = append(out, ProjectedFile{
			Path:    path.Join(strings.TrimSuffix(mount.Path, "/"), k),
			Content: data[k],
			Mode:    mode,
		})
	}
	return out, nil
}

// fanOut redeploys every app mounting the config, skipping those that watch their
// own config file (reloadPolicy: none).
func (s *Service) fanOut(cfg *models.Config) {
	if s.consumers == nil {
		return
	}
	apps, err := s.consumers.AppsMountingConfig(cfg.WorkspaceID, cfg.ID)
	if err != nil {
		return
	}
	for i := range apps {
		if apps[i].ReloadPolicy == models.ReloadNone {
			continue
		}
		_, _ = s.consumers.AutoRedeploy(&apps[i])
	}
}

func (s *Service) ExistsByName(workspaceID uint, name string) bool {
	ok, err := s.repo.ExistsByName(workspaceID, name)
	return err == nil && ok
}

func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }

// Digest is sha256 over the canonical sorted-key serialization, so the same files
// in any order produce the same value.
func Digest(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(data[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateData enforces the key and size rules the declarative layer applies, so
// the API cannot store a config a manifest would reject.
func ValidateData(data map[string]string) error {
	if len(data) == 0 {
		return ErrNoData
	}
	total := 0
	for k, v := range data {
		if !keyRe.MatchString(k) {
			return fmt.Errorf("file key %q must be a relative path matching %s", k, keyRe)
		}
		for _, seg := range strings.Split(k, "/") {
			if seg == "." || seg == ".." {
				return fmt.Errorf("file key %q must not contain path traversal", k)
			}
		}
		if len(v) > MaxFileBytes {
			return fmt.Errorf("file %q is %d bytes, over the %d-byte limit", k, len(v), MaxFileBytes)
		}
		total += len(v)
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("total content is %d bytes, over the %d-byte limit", total, MaxTotalBytes)
	}
	return nil
}

// ValidateMode accepts a 3- or 4-digit octal mode and rejects setuid, setgid and
// the sticky bit.
func ValidateMode(mode string) error {
	if mode == "" {
		return nil
	}
	if len(mode) < 3 || len(mode) > 4 {
		return fmt.Errorf("mode %q must be 3 or 4 octal digits", mode)
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("mode %q is not octal", mode)
	}
	if v&0o7000 != 0 {
		return fmt.Errorf("mode %q must not set the setuid, setgid or sticky bit", mode)
	}
	return nil
}

func encode(workspaceID uint, data map[string]string) (enc, digest string, err error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", "", err
	}
	enc, err = crypto.EncryptWS(workspaceID, string(raw))
	if err != nil {
		return "", "", err
	}
	return enc, Digest(data), nil
}

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

// Package secret is the Vault: encrypted workspace secrets referenced from env and resolved at deploy or job time.
package secret

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound    = errors.New("secret not found")
	ErrNameTaken   = errors.New("a secret with this name already exists")
	ErrInvalidName = errors.New("invalid secret name (use letters, digits, _ or -)")
	ErrNoValue     = errors.New("a value is required")
	ErrInUse       = errors.New("secret is referenced by one or more apps; remove the references first")
	ErrManaged     = errors.New("secret is managed by a platform resource; it is removed with its owner, not by hand")
)

// Optional: nil means no rotation fan-out or delete guard, as in workers that only resolve references.
type Consumers interface {
	AppsReferencingSecret(workspaceID uint, name string) ([]models.Application, error)
	AutoRedeploy(app *models.Application) (*models.Deployment, error)
}

// nameRe validates secret names; it also bounds what a reference can name.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)

// refRe matches a `${{ secrets.NAME }}` reference (whitespace-tolerant).
var refRe = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})\s*\}\}`)

// A stored credential is opaque, so only an all-or-nothing reference counts, unlike env interpolation.
var pureRefRe = regexp.MustCompile(`^\s*\$\{\{\s*secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})\s*\}\}\s*$`)

func RefName(value string) string {
	m := pureRefRe.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return m[1]
}

func Ref(name string) string { return "${{ secrets." + name + " }}" }

type Service struct {
	repo      *repositories.SecretRepository
	consumers Consumers
}

func NewService(repo *repositories.SecretRepository) *Service { return &Service{repo: repo} }

// SetConsumers wires the resolver used for rotation fan-out and the delete guard.
func (s *Service) SetConsumers(c Consumers) { s.consumers = c }

func ReferencesSecret(value, name string) bool {
	for _, m := range refRe.FindAllStringSubmatch(value, -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

func (s *Service) List(workspaceID uint) ([]models.Secret, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) ListPaged(workspaceID uint, search string, managed *bool, limit, offset int) ([]models.Secret, int64, error) {
	return s.repo.ListByWorkspacePaged(workspaceID, search, managed, limit, offset)
}

func (s *Service) Get(workspaceID, id uint) (*models.Secret, error) {
	sec, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return sec, nil
}

// Create stores a new encrypted secret.
func (s *Service) Create(workspaceID uint, name, value, description string, userID *uint) (*models.Secret, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return nil, ErrInvalidName
	}
	if value == "" {
		return nil, ErrNoValue
	}
	if exists, err := s.repo.ExistsByName(workspaceID, name); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrNameTaken
	}
	enc, err := crypto.EncryptWS(workspaceID, value)
	if err != nil {
		return nil, err
	}
	sec := &models.Secret{
		WorkspaceID: workspaceID, Name: name, ValueEnc: enc,
		Description: description, Version: 1, UpdatedByID: userID,
	}
	if err := s.repo.Create(sec); err != nil {
		return nil, err
	}
	return sec, nil
}

func (s *Service) Update(workspaceID, id uint, value, description string, userID *uint) (*models.Secret, error) {
	sec, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	sec.Description = description
	sec.UpdatedByID = userID
	rotated := false
	if strings.TrimSpace(value) != "" {
		enc, err := crypto.EncryptWS(workspaceID, value)
		if err != nil {
			return nil, err
		}
		sec.ValueEnc = enc
		sec.Version++
		sec.SealedFP, sec.SealedKeyVersion = "", 0
		rotated = true
	}
	if err := s.repo.Update(sec); err != nil {
		return nil, err
	}
	if rotated {
		s.fanOut(sec)
	}
	return sec, nil
}

// A re-seal of an unchanged value only moves the fingerprint: no new version, no redeploy.
func (s *Service) ApplySealed(workspaceID uint, name, value, fp string, keyVersion int) (*models.Secret, error) {
	sec, err := s.repo.FindByName(workspaceID, name)
	if err != nil {
		created, cerr := s.Create(workspaceID, name, value, "", nil)
		if cerr != nil {
			return nil, cerr
		}
		created.SealedFP, created.SealedKeyVersion = fp, keyVersion
		return created, s.repo.Update(created)
	}
	if value == "" {
		return nil, ErrNoValue
	}
	if sec.Managed {
		return nil, ErrManaged
	}
	current, derr := crypto.Decrypt(sec.ValueEnc)
	changed := derr != nil || current != value
	if changed {
		enc, eerr := crypto.EncryptWS(workspaceID, value)
		if eerr != nil {
			return nil, eerr
		}
		sec.ValueEnc = enc
		sec.Version++
		sec.UpdatedByID = nil
	}
	sec.SealedFP, sec.SealedKeyVersion = fp, keyVersion
	if err := s.repo.Update(sec); err != nil {
		return nil, err
	}
	if changed {
		s.fanOut(sec)
	}
	return sec, nil
}

// UpsertOwned bypasses name validation for system-generated names; a changed value fans out to consumers.
func (s *Service) UpsertOwned(workspaceID uint, ownerKind string, ownerID uint, name, value, description string) (*models.Secret, error) {
	existing, err := s.repo.FindByName(workspaceID, name)
	if err != nil {
		enc, eerr := crypto.EncryptWS(workspaceID, value)
		if eerr != nil {
			return nil, eerr
		}
		sec := &models.Secret{
			WorkspaceID: workspaceID, Name: name, ValueEnc: enc, Description: description,
			Version: 1, Managed: true, OwnerKind: ownerKind, OwnerID: ownerID,
		}
		if err := s.repo.Create(sec); err != nil {
			return nil, err
		}
		return sec, nil // new: nothing references it yet, no fan-out
	}
	existing.Description = description
	existing.Managed = true
	existing.OwnerKind = ownerKind
	existing.OwnerID = ownerID
	rotated := false
	if strings.TrimSpace(value) != "" {
		enc, eerr := crypto.EncryptWS(workspaceID, value)
		if eerr != nil {
			return nil, eerr
		}
		existing.ValueEnc = enc
		existing.Version++
		existing.SealedFP, existing.SealedKeyVersion = "", 0
		rotated = true
	}
	if err := s.repo.Update(existing); err != nil {
		return nil, err
	}
	if rotated {
		s.fanOut(existing)
	}
	return existing, nil
}

// DeleteOwned bypasses the referenced-secret guard since a managed secret's lifecycle follows its owner.
func (s *Service) DeleteOwned(workspaceID uint, ownerKind string, ownerID uint) ([]models.Application, error) {
	owned, err := s.repo.ListByOwner(workspaceID, ownerKind, ownerID)
	if err != nil {
		return nil, err
	}
	seen := map[uint]bool{}
	var stillReferenced []models.Application
	for i := range owned {
		if s.consumers != nil {
			if apps, aerr := s.consumers.AppsReferencingSecret(workspaceID, owned[i].Name); aerr == nil {
				for j := range apps {
					if !seen[apps[j].ID] {
						seen[apps[j].ID] = true
						stillReferenced = append(stillReferenced, apps[j])
					}
				}
			}
		}
		if derr := s.repo.Delete(owned[i].ID); derr != nil {
			return stillReferenced, derr
		}
	}
	return stillReferenced, nil
}

func (s *Service) Usage(workspaceID, id uint) ([]models.Application, error) {
	sec, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if s.consumers == nil {
		return nil, nil
	}
	return s.consumers.AppsReferencingSecret(workspaceID, sec.Name)
}

func (s *Service) fanOut(sec *models.Secret) {
	if s.consumers == nil {
		return
	}
	apps, err := s.consumers.AppsReferencingSecret(sec.WorkspaceID, sec.Name)
	if err != nil {
		return
	}
	for i := range apps {
		_, _ = s.consumers.AutoRedeploy(&apps[i])
	}
}

// Reveal returns a secret's decrypted value (sensitive — admin only, audited).
func (s *Service) Reveal(workspaceID, id uint) (string, error) {
	sec, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return "", ErrNotFound
	}
	return crypto.Decrypt(sec.ValueEnc)
}

func (s *Service) Delete(workspaceID, id uint) error {
	sec, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	// A managed secret is only removed via its owner's cascade, never by a hand delete.
	if sec.Managed {
		return ErrManaged
	}
	if s.consumers != nil {
		if apps, err := s.consumers.AppsReferencingSecret(workspaceID, sec.Name); err == nil && len(apps) > 0 {
			return ErrInUse
		}
	}
	return s.repo.Delete(sec.ID)
}

// ResolveAll fails on an unknown reference: a deploy must fail loudly rather than inject a blank.
func (s *Service) ResolveAll(workspaceID uint, env []string) ([]string, error) {
	hasRef := false
	for _, v := range env {
		if refRe.MatchString(v) {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return env, nil
	}

	secrets, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(secrets))
	for i := range secrets {
		dec, derr := crypto.Decrypt(secrets[i].ValueEnc)
		if derr != nil {
			return nil, fmt.Errorf("decrypt secret %q: %w", secrets[i].Name, derr)
		}
		values[secrets[i].Name] = dec
	}

	var missing error
	out := make([]string, len(env))
	for i, v := range env {
		out[i] = refRe.ReplaceAllStringFunc(v, func(match string) string {
			name := refRe.FindStringSubmatch(match)[1]
			val, ok := values[name]
			if !ok {
				if missing == nil {
					missing = fmt.Errorf("secret %q referenced in env is not defined in this workspace", name)
				}
				return match
			}
			return val
		})
	}
	if missing != nil {
		return nil, missing
	}
	return out, nil
}

// CredentialSecret reads a referenced vault secret's current value, so rotating it rotates every credential.
func (s *Service) CredentialSecret(workspaceID uint, enc, ref string) (string, error) {
	if ref != "" {
		sec, err := s.repo.FindByName(workspaceID, ref)
		if err != nil {
			return "", fmt.Errorf("%w: credential references secret %q, which is not defined in this workspace", ErrNotFound, ref)
		}
		return crypto.Decrypt(sec.ValueEnc)
	}
	if enc == "" {
		return "", nil
	}
	return crypto.Decrypt(enc)
}

func (s *Service) ValueByName(workspaceID uint, name string) (string, bool) {
	sec, err := s.repo.FindByName(workspaceID, name)
	if err != nil {
		return "", false
	}
	v, err := crypto.Decrypt(sec.ValueEnc)
	if err != nil {
		return "", false
	}
	return v, true
}

// Validates credential references at save so a typo surfaces in the form, not at the next deploy.
func (s *Service) ExistsByName(workspaceID uint, name string) bool {
	_, err := s.repo.FindByName(workspaceID, name)
	return err == nil
}

// IDByUID resolves a secret's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secret is the Vault: workspace-scoped named secrets that env var values reference
// (`${{ secrets.NAME }}`) and that are resolved into a container's environment at deploy or job time. Values
// are encrypted at rest and never returned except via an explicit, audited reveal.
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

// Consumers resolves which apps reference a secret and triggers their redeploy.
// Implemented by the application service; optional (nil = no rotation fan-out /
// no delete guard, e.g. in worker processes that only resolve references).
type Consumers interface {
	AppsReferencingSecret(workspaceID uint, name string) ([]models.Application, error)
	AutoRedeploy(app *models.Application) (*models.Deployment, error)
}

// nameRe validates secret names; it also bounds what a reference can name.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)

// refRe matches a `${{ secrets.NAME }}` reference (whitespace-tolerant).
var refRe = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})\s*\}\}`)

// pureRefRe matches a value that is *only* a reference, with nothing around it. Env values interpolate
// references inside larger strings, but a stored credential is opaque and can contain any bytes, so there it
// is all-or-nothing — otherwise a token containing "${{ secrets.x }}" would be silently unusable.
var pureRefRe = regexp.MustCompile(`^\s*\$\{\{\s*secrets\.([A-Za-z][A-Za-z0-9_-]{0,62})\s*\}\}\s*$`)

// RefName returns the secret a value references when the value is exactly one
// `${{ secrets.NAME }}` reference, or "" when it is a literal.
func RefName(value string) string {
	m := pureRefRe.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return m[1]
}

// Ref renders the reference form of a secret name — the value a client stores in
// a credential field to point at the vault instead of pasting the secret.
func Ref(name string) string { return "${{ secrets." + name + " }}" }

type Service struct {
	repo      *repositories.SecretRepository
	consumers Consumers
}

func NewService(repo *repositories.SecretRepository) *Service { return &Service{repo: repo} }

// SetConsumers wires the resolver used for rotation fan-out and the delete guard.
func (s *Service) SetConsumers(c Consumers) { s.consumers = c }

// ReferencesSecret reports whether an env value contains a `${{ secrets.name }}`
// reference to the named secret.
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

// ListPaged returns a searchable, paginated page of a workspace's secrets and
// the total count of matching rows. managed narrows to platform-owned secrets
// (true) or hand-created ones (false); nil returns both.
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

// Update replaces a secret's value (rotation) and/or description. A blank value
// leaves the stored value unchanged.
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

// UpsertOwned creates or rotates a managed secret owned by a platform resource, such as a managed database.
// The name is system-generated, so it bypasses the user-facing name validation, and a changed value fans out
// to consumers. Marks the secret Managed with the given owner.
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

// DeleteOwned removes every managed secret owned by a resource, cascading from the owner's deletion. This is
// privileged: it bypasses the referenced-secret delete guard, since a managed secret's lifecycle follows its
// owner. Returns the apps that still referenced any deleted secret, for the caller to warn.
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

// Usage returns the apps that reference a secret (for the "used by" view and the
// delete guard).
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

// fanOut redeploys every app that references the secret (best-effort) so a
// rotated value takes effect immediately.
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
	// A managed secret's lifecycle follows its owning resource (e.g. a managed
	// database) — it is only removed via the owner's cascade (DeleteOwned), never
	// by a hand delete.
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

// ResolveAll substitutes `${{ secrets.NAME }}` references across all env values for a workspace, loading its
// secrets once. Returns an error naming the first unknown reference — a deploy must fail loudly rather than
// inject a blank. When no value contains a reference it is a no-op, with no DB load.
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

// CredentialSecret resolves a stored credential's secret — the one resolution point shared by registry and
// Git credentials. When ref names a vault secret its *current* value is read, so rotating that secret rotates
// every credential pointing at it. Both empty means no secret, which is not an error.
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

// ExistsByName reports whether a secret of that name exists in the workspace. It
// backs the validation of a credential's secret reference at create/update time,
// so a typo surfaces in the form rather than at the next deploy.
func (s *Service) ExistsByName(workspaceID uint, name string) bool {
	_, err := s.repo.FindByName(workspaceID, name)
	return err == nil
}

// IDByUID resolves a secret's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }

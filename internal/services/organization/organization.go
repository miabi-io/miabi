// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package organization manages tenant realms: the workspaces they own, the workspace cap they carry,
// and the cluster they are dedicated to. One organization is always the default, and every nullable
// organization_id in the schema resolves to it.
package organization

import (
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

var (
	ErrNotFound      = errors.New("organization not found")
	ErrNameInvalid   = errors.New("an organization handle is lowercase letters, digits and hyphens, e.g. acme")
	ErrNameReserved  = errors.New("that handle is reserved")
	ErrNameTaken     = errors.New("an organization with that handle already exists")
	ErrNameImmutable = errors.New("an organization's handle cannot be changed; edit its display name instead")
	// ErrDefaultProtected guards the fallback every nullable organization_id resolves to.
	ErrDefaultProtected = errors.New("the default organization cannot be deleted")
	ErrNotEmpty         = errors.New("the organization still holds workspaces or users")
	ErrInvalidMax       = errors.New("max_workspaces is -1 (unlimited) or a count of 0 or more")
	// ErrClusterNotOurs guards a default location the organization cannot place in.
	ErrClusterNotOurs = errors.New("that location is not one this organization can place in")
)

// Clusters is the slice of the cluster store this service needs: resolving a dedicated cluster and
// releasing the ones an organization owns when it is deleted.
type Clusters interface {
	FindByID(id uint) (*models.Cluster, error)
	ListByOrganization(orgID uint) ([]models.Cluster, error)
	CountByOrganization(orgID uint) (int64, error)
	ReleaseOrganization(orgID uint) error
}

// Workspaces resolves a workspace to the organization that owns it. Satisfied by
// repositories.WorkspaceRepository.
type Workspaces interface {
	FindByID(id uint) (*models.Workspace, error)
}

// Service manages organizations.
type Service struct {
	repo       *repositories.OrganizationRepository
	clusters   Clusters
	workspaces Workspaces
}

func NewService(repo *repositories.OrganizationRepository) *Service {
	return &Service{repo: repo}
}

// SetClusters wires the cluster store (nil-safe; without it an organization cannot be given a
// dedicated cluster and deleting one leaves its clusters to the caller).
func (s *Service) SetClusters(c Clusters) { s.clusters = c }

// SetWorkspaces wires the workspace store (nil-safe; without it a workspace cannot be resolved to
// its organization, which leaves placement on the shared clusters).
func (s *Service) SetWorkspaces(w Workspaces) { s.workspaces = w }

func (s *Service) List() ([]models.Organization, error) { return s.repo.List() }

func (s *Service) Get(id uint) (*models.Organization, error) {
	org, err := s.repo.FindByID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	return org, nil
}

// Default is the organization every nullable organization_id resolves to.
func (s *Service) Default() (*models.Organization, error) { return s.repo.FindDefault() }

// DefaultID is the default organization's id, or 0 when it cannot be read. Callers use it to resolve
// a nil organization_id without having to handle the error at every site.
func (s *Service) DefaultID() uint {
	org, err := s.repo.FindDefault()
	if err != nil {
		return 0
	}
	return org.ID
}

// Resolve turns a nullable organization_id into a concrete one, mapping nil to the default org.
func (s *Service) Resolve(id *uint) uint {
	if id != nil && *id != 0 {
		return *id
	}
	return s.DefaultID()
}

// CreateInput is a new organization. Handle is derived from DisplayName when blank, and a nil
// MaxWorkspaces means unlimited — the zero value would otherwise read as "no workspaces allowed".
type CreateInput struct {
	Handle        string
	DisplayName   string
	OwnerUserID   uint
	MaxWorkspaces *int
}

// Create makes an organization. Unlike a workspace handle it is not auto-suffixed on collision: an
// admin naming an org means that name, and silently creating "acme-1" would be worse than an error.
func (s *Service) Create(in CreateInput) (*models.Organization, error) {
	name, err := s.validName(in.Handle, in.DisplayName)
	if err != nil {
		return nil, err
	}
	max := models.Unlimited
	if in.MaxWorkspaces != nil {
		max = *in.MaxWorkspaces
	}
	if err := validMax(max); err != nil {
		return nil, err
	}
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = name
	}
	org := &models.Organization{
		Name: name, DisplayName: display,
		OwnerUserID: in.OwnerUserID, MaxWorkspaces: max,
	}
	if err := s.repo.Create(org); err != nil {
		return nil, err
	}
	return org, nil
}

func (s *Service) validName(handle, fallback string) (string, error) {
	raw := strings.TrimSpace(handle)
	if raw == "" {
		raw = fallback
	}
	name := slug.Make(raw, "")
	if name == "" {
		return "", ErrNameInvalid
	}
	if slug.IsReserved(name) {
		return "", ErrNameReserved
	}
	taken, err := s.repo.ExistsByName(name)
	if err != nil {
		return "", err
	}
	if taken {
		return "", ErrNameTaken
	}
	return name, nil
}

func validMax(n int) error {
	if n < models.Unlimited {
		return ErrInvalidMax
	}
	return nil
}

// UpdateInput carries the editable fields. A nil field is left as it is; the handle is not editable
// (ErrNameImmutable) because it is what admins and the API address the org by.
type UpdateInput struct {
	DisplayName      *string
	OwnerUserID      *uint
	MaxWorkspaces    *int
	DefaultClusterID *uint
	// ClearDefaultCluster releases the org's default location, which a nil DefaultClusterID cannot
	// express on its own.
	ClearDefaultCluster bool
}

func (s *Service) Update(id uint, in UpdateInput) (*models.Organization, error) {
	org, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if in.DisplayName != nil {
		if d := strings.TrimSpace(*in.DisplayName); d != "" {
			org.DisplayName = d
		}
	}
	if in.OwnerUserID != nil {
		org.OwnerUserID = *in.OwnerUserID
	}
	if in.MaxWorkspaces != nil {
		if err := validMax(*in.MaxWorkspaces); err != nil {
			return nil, err
		}
		org.MaxWorkspaces = *in.MaxWorkspaces
	}
	switch {
	case in.ClearDefaultCluster:
		org.DefaultClusterID = nil
	case in.DefaultClusterID != nil:
		if err := s.usableCluster(org.ID, *in.DefaultClusterID); err != nil {
			return nil, err
		}
		org.DefaultClusterID = in.DefaultClusterID
	}
	if err := s.repo.Update(org); err != nil {
		return nil, err
	}
	return org, nil
}

// usableCluster refuses a default location the organization could never place in: one dedicated to
// somebody else, or a shared one while this organization runs clusters of its own. Either would seed
// every new workspace with a default that placement silently skips.
func (s *Service) usableCluster(orgID, clusterID uint) error {
	if s.clusters == nil {
		return nil
	}
	c, err := s.clusters.FindByID(clusterID)
	if err != nil {
		return err
	}
	if c.OrganizationID != nil {
		if *c.OrganizationID != orgID {
			return ErrClusterNotOurs
		}
		return nil
	}
	if s.OwnsClusters(orgID) {
		return ErrClusterNotOurs
	}
	return nil
}

// SetDefault promotes an organization to the default one. Demoting the previous default and
// promoting the new one happen together, so the fallback is never ambiguous or absent.
func (s *Service) SetDefault(id uint) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return s.repo.SetDefault(id)
}

// Delete removes an organization. The default one is never removable, and an org still holding
// workspaces or users is refused rather than silently orphaning them — reassign first, deliberately.
// Clusters dedicated to it are released back to shared, since a dangling owner would hide a cluster
// from everyone.
func (s *Service) Delete(id uint) error {
	org, err := s.Get(id)
	if err != nil {
		return err
	}
	if org.IsDefault {
		return ErrDefaultProtected
	}
	workspaces, err := s.repo.CountWorkspaces(id)
	if err != nil {
		return err
	}
	users, err := s.repo.CountUsers(id)
	if err != nil {
		return err
	}
	if workspaces > 0 || users > 0 {
		return ErrNotEmpty
	}
	if s.clusters != nil {
		if err := s.clusters.ReleaseOrganization(id); err != nil {
			return err
		}
	}
	return s.repo.Delete(id)
}

// CapReached reports whether the organization already holds its maximum workspaces. It fails open on
// a read error: a transient database fault must not stop a tenant creating a workspace.
func (s *Service) CapReached(orgID uint) bool {
	if orgID == 0 {
		return false
	}
	org, err := s.repo.FindByID(orgID)
	if err != nil || org.WorkspacesUnlimited() {
		return false
	}
	n, err := s.repo.CountWorkspaces(orgID)
	if err != nil {
		return false
	}
	return n >= int64(org.MaxWorkspaces)
}

// HomeOrganization is the organization a user belongs to, resolving nil to the default. 0 when it
// cannot be read at all.
func (s *Service) HomeOrganization(u *models.User) uint {
	if u == nil {
		return s.DefaultID()
	}
	return s.Resolve(u.OrganizationID)
}

// UserCount is how many users call the organization home. 0 when it cannot be read.
func (s *Service) UserCount(orgID uint) int64 {
	n, err := s.repo.CountUsers(orgID)
	if err != nil {
		return 0
	}
	return n
}

// The three methods below answer the placement engine's organization questions. They satisfy
// placement.Orgs structurally, so neither package imports the other.

// OrganizationOfWorkspace resolves the realm a workspace belongs to, mapping an unassigned
// workspace to the default organization. 0 when it cannot be read, which leaves the caller on the
// shared clusters.
func (s *Service) OrganizationOfWorkspace(workspaceID uint) uint {
	if s.workspaces == nil {
		return 0
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return 0
	}
	// The platform's own workspace belongs to no tenant. It is already pinned to the Unlimited plan
	// so platform infrastructure is never constrained by tenant quotas, and confining it to whichever
	// organization happens to be the default would constrain it by tenant geography instead.
	if ws.System {
		return 0
	}
	return s.Resolve(ws.OrganizationID)
}

// OwnsClusters reports whether the organization runs clusters of its own, which confines it to
// them. It fails open — a read error must not strand every create — because the per-cluster
// ownership check still refuses another tenant's cluster on its own.
func (s *Service) OwnsClusters(orgID uint) bool {
	if orgID == 0 || s.clusters == nil {
		return false
	}
	n, err := s.clusters.CountByOrganization(orgID)
	return err == nil && n > 0
}

// OrganizationLabel names the organization for the message shown where a workspace's own placement
// choice used to be.
func (s *Service) OrganizationLabel(orgID uint) string {
	org, err := s.Get(orgID)
	if err != nil {
		return ""
	}
	if org.DisplayName != "" {
		return org.DisplayName
	}
	return org.Name
}

// Labels names several organizations at once, for a list annotated with the organization each row
// belongs to. A read error yields an empty map: a missing label costs a name, not the list.
func (s *Service) Labels(ids []uint) map[uint]string {
	if s.repo == nil || len(ids) == 0 {
		return map[uint]string{}
	}
	labels, err := s.repo.Labels(ids)
	if err != nil {
		return map[uint]string{}
	}
	return labels
}

// IsNotFound reports a missing row, so callers can map a repository error without importing gorm.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

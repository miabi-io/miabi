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

// Package organization manages tenant realms; every nullable organization_id resolves to the default one.
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
	// ErrInvalidWorkspaceCreation rejects an unknown workspace creation mode.
	ErrInvalidWorkspaceCreation = errors.New(`workspace_creation must be "anyone", "except_invited" or "admins_only"`)
	// ErrClusterNotOurs guards a default location the organization cannot place in.
	ErrClusterNotOurs = errors.New("that location is not one this organization can place in")
)

type Clusters interface {
	FindByID(id uint) (*models.Cluster, error)
	ListByOrganization(orgID uint) ([]models.Cluster, error)
	CountByOrganization(orgID uint) (int64, error)
	ReleaseOrganization(orgID uint) error
}

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

// SetClusters wires the cluster store; without it orgs cannot get dedicated clusters and Delete leaves them to the caller.
func (s *Service) SetClusters(c Clusters) { s.clusters = c }

// SetWorkspaces wires the workspace store; without it placement stays on the shared clusters.
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

// DefaultID returns 0 when unreadable, so callers can resolve a nil organization_id without error handling.
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

// A nil MaxWorkspaces means unlimited; the zero value would read as "no workspaces allowed".
type CreateInput struct {
	Handle        string
	DisplayName   string
	OwnerUserID   uint
	MaxWorkspaces *int
	// nil inherits the platform default.
	MaxWorkspacesPerUser           *int
	MaxWorkspaceMembershipsPerUser *int
	// nil applies the per-user cap to the owner too.
	MaxWorkspacesPerOwner *int
	// WorkspaceCreation is who may create workspaces in the org; nil inherits the platform setting.
	WorkspaceCreation *string
}

// Create never auto-suffixes on collision: an admin naming an org means that name.
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
	perUser, err := validPerUser(in.MaxWorkspacesPerUser)
	if err != nil {
		return nil, err
	}
	perUserJoined, err := validPerUser(in.MaxWorkspaceMembershipsPerUser)
	if err != nil {
		return nil, err
	}
	perOwner, err := validPerUser(in.MaxWorkspacesPerOwner)
	if err != nil {
		return nil, err
	}
	creation, err := validCreation(in.WorkspaceCreation)
	if err != nil {
		return nil, err
	}
	org := &models.Organization{
		Name: name, DisplayName: display,
		OwnerUserID: in.OwnerUserID, MaxWorkspaces: max,
		MaxWorkspacesPerUser:           perUser,
		MaxWorkspaceMembershipsPerUser: perUserJoined,
		MaxWorkspacesPerOwner:          perOwner,
		WorkspaceCreation:              creation,
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

// Only -1 (unlimited) may reach the database as a negative.
func validPerUser(n *int) (*int, error) {
	if n == nil {
		return nil, nil
	}
	if err := validMax(*n); err != nil {
		return nil, err
	}
	v := *n
	return &v, nil
}

// The handle is not editable (ErrNameImmutable): admins and the API address the org by it.
type UpdateInput struct {
	DisplayName      *string
	OwnerUserID      *uint
	MaxWorkspaces    *int
	DefaultClusterID *uint
	// Inherit*PerUser resets a cap to the platform default, which a nil pointer cannot express.
	MaxWorkspacesPerUser               *int
	MaxWorkspaceMembershipsPerUser     *int
	InheritWorkspacesPerUser           bool
	InheritWorkspaceMembershipsPerUser bool
	// SameAsUsersForOwner clears the owner cap so the per-user cap applies again.
	MaxWorkspacesPerOwner *int
	SameAsUsersForOwner   bool
	// InheritWorkspaceCreation clears it back to the platform setting.
	WorkspaceCreation        *string
	InheritWorkspaceCreation bool
	// Needed because a nil DefaultClusterID cannot express "clear".
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
	case in.InheritWorkspacesPerUser:
		org.MaxWorkspacesPerUser = nil
	case in.MaxWorkspacesPerUser != nil:
		v, err := validPerUser(in.MaxWorkspacesPerUser)
		if err != nil {
			return nil, err
		}
		org.MaxWorkspacesPerUser = v
	}
	switch {
	case in.InheritWorkspaceMembershipsPerUser:
		org.MaxWorkspaceMembershipsPerUser = nil
	case in.MaxWorkspaceMembershipsPerUser != nil:
		v, err := validPerUser(in.MaxWorkspaceMembershipsPerUser)
		if err != nil {
			return nil, err
		}
		org.MaxWorkspaceMembershipsPerUser = v
	}
	switch {
	case in.SameAsUsersForOwner:
		org.MaxWorkspacesPerOwner = nil
	case in.MaxWorkspacesPerOwner != nil:
		v, err := validPerUser(in.MaxWorkspacesPerOwner)
		if err != nil {
			return nil, err
		}
		org.MaxWorkspacesPerOwner = v
	}
	switch {
	case in.InheritWorkspaceCreation:
		org.WorkspaceCreation = nil
	case in.WorkspaceCreation != nil:
		v, err := validCreation(in.WorkspaceCreation)
		if err != nil {
			return nil, err
		}
		org.WorkspaceCreation = v
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

// Refuses another tenant's cluster, or a shared one while the org owns clusters; placement would silently skip either.
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

// AlignDefaultCluster fixes defaults invalidated by dedicating a cluster; prefer (the cluster that changed hands) wins if owned.
func (s *Service) AlignDefaultCluster(orgID, prefer uint) error {
	if s.clusters == nil || orgID == 0 {
		return nil
	}
	org, err := s.Get(orgID)
	if err != nil {
		return err
	}
	if org.DefaultClusterID != nil && s.usableCluster(orgID, *org.DefaultClusterID) == nil {
		return nil
	}
	owned, err := s.clusters.ListByOrganization(orgID)
	if err != nil {
		return err
	}
	next := pickDefault(owned, prefer)
	if equalClusterID(org.DefaultClusterID, next) {
		return nil
	}
	org.DefaultClusterID = next
	return s.repo.Update(org)
}

// An org that owns nothing falls back to the platform default, which it may use again.
func pickDefault(owned []models.Cluster, prefer uint) *uint {
	for i := range owned {
		if owned[i].ID == prefer {
			return &owned[i].ID
		}
	}
	if len(owned) > 0 {
		return &owned[0].ID
	}
	return nil
}

func equalClusterID(a *uint, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// SetDefault demotes and promotes together, so the fallback is never ambiguous or absent.
func (s *Service) SetDefault(id uint) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return s.repo.SetDefault(id)
}

// Delete refuses the default org or one with workspaces/users; dedicated clusters are released back to shared.
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

// CapReached fails open: a transient DB fault must not stop a tenant creating a workspace.
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

// HomeOrganization resolves nil to the default; 0 when it cannot be read.
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

// These satisfy placement.Orgs structurally, so neither package imports the other.

// OrganizationOfWorkspace maps unassigned workspaces to the default org; 0 on read failure keeps shared clusters.
func (s *Service) OrganizationOfWorkspace(workspaceID uint) uint {
	if s.workspaces == nil {
		return 0
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return 0
	}
	// The platform workspace belongs to no tenant; confining it to the default org would bind infra to tenant geography.
	if ws.System {
		return 0
	}
	return s.Resolve(ws.OrganizationID)
}

// OwnsClusters fails open; the per-cluster ownership check still refuses another tenant's cluster.
func (s *Service) OwnsClusters(orgID uint) bool {
	if orgID == 0 || s.clusters == nil {
		return false
	}
	n, err := s.clusters.CountByOrganization(orgID)
	return err == nil && n > 0
}

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

// Labels returns an empty map on read error: a missing label costs a name, not the list.
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

// validCreation checks an optional workspace creation mode; nil stays nil (inherit).
func validCreation(mode *string) (*string, error) {
	if mode == nil {
		return nil, nil
	}
	m := strings.TrimSpace(*mode)
	// Console mode is platform-wide and set only by MIABI_WORKSPACE_CREATION.
	if !models.ValidWorkspaceCreation(m) || m == models.WorkspaceCreationConsole {
		return nil, ErrInvalidWorkspaceCreation
	}
	return &m, nil
}

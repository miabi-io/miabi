// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package organization

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// orgRow is the organizations stand-in: the real model's uid column defaults to the Postgres-only
// gen_random_uuid(), which sqlite cannot parse.
type orgRow struct {
	ID               uint `gorm:"primaryKey"`
	UID              string
	Name             string
	DisplayName      string
	IsDefault        bool
	OwnerUserID      uint
	MaxWorkspaces    int
	DefaultClusterID *uint
	EnforceSSO       bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (orgRow) TableName() string { return "organizations" }

type wsRow struct {
	ID             uint `gorm:"primaryKey"`
	Name           string
	OrganizationID *uint
}

func (wsRow) TableName() string { return "workspaces" }

type userRow struct {
	ID             uint `gorm:"primaryKey"`
	Name           string
	OrganizationID *uint
}

func (userRow) TableName() string { return "users" }

func newOrgService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&orgRow{}, &wsRow{}, &userRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orgRow{ID: 1, Name: "default", DisplayName: "Default", IsDefault: true, MaxWorkspaces: models.Unlimited}).Error; err != nil {
		t.Fatal(err)
	}
	return NewService(repositories.NewOrganizationRepository(db)), db
}

// A new organization takes a slugged handle, defaults to unlimited workspaces, and refuses a handle
// that is taken or reserved — unlike a workspace it is never auto-suffixed, because an admin naming
// an org means that name.
func TestCreateOrganization(t *testing.T) {
	s, _ := newOrgService(t)

	org, err := s.Create(CreateInput{DisplayName: "Acme Corp"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if org.Name != "acme-corp" {
		t.Errorf("handle = %q, want acme-corp", org.Name)
	}
	if !org.WorkspacesUnlimited() {
		t.Errorf("max workspaces = %d, want unlimited", org.MaxWorkspaces)
	}
	if org.IsDefault {
		t.Error("a new organization must not claim the default flag")
	}

	if _, err := s.Create(CreateInput{Handle: "acme-corp", DisplayName: "Other"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate handle err = %v, want ErrNameTaken", err)
	}
	if _, err := s.Create(CreateInput{Handle: "orgs", DisplayName: "Reserved"}); !errors.Is(err, ErrNameReserved) {
		t.Errorf("reserved handle err = %v, want ErrNameReserved", err)
	}
	zero := 0
	capped, err := s.Create(CreateInput{DisplayName: "Capped", MaxWorkspaces: &zero})
	if err != nil {
		t.Fatalf("create capped: %v", err)
	}
	if capped.MaxWorkspaces != 0 {
		t.Errorf("an explicit 0 should mean none allowed, got %d", capped.MaxWorkspaces)
	}
}

// The workspace cap counts the org's workspaces, treats -1 as unlimited, and fails open so a read
// error never blocks a create.
func TestCapReached(t *testing.T) {
	s, db := newOrgService(t)
	two := 2
	org, err := s.Create(CreateInput{DisplayName: "Acme", MaxWorkspaces: &two})
	if err != nil {
		t.Fatal(err)
	}

	if s.CapReached(org.ID) {
		t.Error("an empty organization is not at its cap")
	}
	for i := 1; i <= 2; i++ {
		if err := db.Create(&wsRow{Name: "ws", OrganizationID: &org.ID}).Error; err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	if !s.CapReached(org.ID) {
		t.Error("two of two workspaces should reach the cap")
	}
	if s.CapReached(1) {
		t.Error("the unlimited default organization is never at its cap")
	}
	if s.CapReached(0) {
		t.Error("an unknown organization must not block a create")
	}
}

// The default organization is the fallback every nullable organization_id resolves to, so it cannot
// be deleted; promoting another one moves the flag rather than duplicating it.
func TestDefaultOrganizationIsProtected(t *testing.T) {
	s, _ := newOrgService(t)
	org, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(1); !errors.Is(err, ErrDefaultProtected) {
		t.Errorf("deleting the default org err = %v, want ErrDefaultProtected", err)
	}
	if err := s.SetDefault(org.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	def, err := s.Default()
	if err != nil || def.ID != org.ID {
		t.Fatalf("default = %+v (%v), want org %d", def, err, org.ID)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, o := range all {
		if o.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d organizations carry the default flag, want exactly 1", defaults)
	}
}

// An organization still holding workspaces or users is refused rather than orphaning them.
func TestDeleteRefusesANonEmptyOrganization(t *testing.T) {
	s, db := newOrgService(t)
	org, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&wsRow{Name: "ws", OrganizationID: &org.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(org.ID); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("delete with a workspace err = %v, want ErrNotEmpty", err)
	}

	if err := db.Where("organization_id = ?", org.ID).Delete(&wsRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&userRow{Name: "u", OrganizationID: &org.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(org.ID); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("delete with a user err = %v, want ErrNotEmpty", err)
	}
}

// A nil organization_id resolves to the default org, which is what every unassigned row means.
func TestResolveAndHomeOrganization(t *testing.T) {
	s, _ := newOrgService(t)
	if got := s.Resolve(nil); got != 1 {
		t.Errorf("Resolve(nil) = %d, want the default org 1", got)
	}
	id := uint(7)
	if got := s.Resolve(&id); got != 7 {
		t.Errorf("Resolve(7) = %d, want 7", got)
	}
	if got := s.HomeOrganization(&models.User{}); got != 1 {
		t.Errorf("a user with no organization = %d, want the default org 1", got)
	}
	if got := s.HomeOrganization(nil); got != 1 {
		t.Errorf("no user = %d, want the default org 1", got)
	}
}

// clusterStub answers the placement questions from a fixed count.
type clusterStub struct{ owned map[uint]int64 }

func (c clusterStub) FindByID(uint) (*models.Cluster, error)            { return &models.Cluster{}, nil }
func (c clusterStub) ListByOrganization(uint) ([]models.Cluster, error) { return nil, nil }
func (c clusterStub) CountByOrganization(id uint) (int64, error)        { return c.owned[id], nil }
func (c clusterStub) ReleaseOrganization(uint) error                    { return nil }

type wsStub struct{ orgs map[uint]*uint }

func (w wsStub) FindByID(id uint) (*models.Workspace, error) {
	org, ok := w.orgs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &models.Workspace{ID: id, OrganizationID: org}, nil
}

// The placement engine asks the organization service three questions. Unwired dependencies answer
// "no organization" rather than failing, so placement keeps working before organizations are set up.
func TestPlacementQuestions(t *testing.T) {
	s, _ := newOrgService(t)
	acme, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}

	// Unwired: no workspace resolves, and no organization owns clusters.
	if got := s.OrganizationOfWorkspace(1); got != 0 {
		t.Errorf("unwired workspace lookup = %d, want 0", got)
	}
	if s.OwnsClusters(acme.ID) {
		t.Error("unwired cluster lookup must not report ownership")
	}

	s.SetWorkspaces(wsStub{orgs: map[uint]*uint{1: &acme.ID, 2: nil}})
	s.SetClusters(clusterStub{owned: map[uint]int64{acme.ID: 2}})

	if got := s.OrganizationOfWorkspace(1); got != acme.ID {
		t.Errorf("workspace 1 = org %d, want %d", got, acme.ID)
	}
	// An unassigned workspace belongs to the default organization, not to none.
	if got := s.OrganizationOfWorkspace(2); got != 1 {
		t.Errorf("unassigned workspace = org %d, want the default org 1", got)
	}
	if got := s.OrganizationOfWorkspace(99); got != 0 {
		t.Errorf("unknown workspace = org %d, want 0", got)
	}

	if !s.OwnsClusters(acme.ID) {
		t.Error("an organization with clusters should report ownership")
	}
	if s.OwnsClusters(1) || s.OwnsClusters(0) {
		t.Error("an organization without clusters must not report ownership")
	}

	if got := s.OrganizationLabel(acme.ID); got != "Acme" {
		t.Errorf("label = %q, want Acme", got)
	}
	if got := s.OrganizationLabel(999); got != "" {
		t.Errorf("unknown organization label = %q, want empty", got)
	}
}

// The platform's own workspace belongs to no tenant, so a dedicated default organization must not
// confine it — platform infrastructure has to be placeable anywhere.
func TestSystemWorkspaceIsNeverConfined(t *testing.T) {
	s, _ := newOrgService(t)
	acme, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetClusters(clusterStub{owned: map[uint]int64{acme.ID: 1}})
	s.SetWorkspaces(systemWS{orgID: acme.ID})

	if got := s.OrganizationOfWorkspace(1); got != acme.ID {
		t.Errorf("ordinary workspace = org %d, want %d", got, acme.ID)
	}
	if got := s.OrganizationOfWorkspace(2); got != 0 {
		t.Errorf("system workspace = org %d, want 0 (no tenant)", got)
	}
}

// systemWS serves workspace 1 as an ordinary member of orgID and workspace 2 as the platform one.
type systemWS struct{ orgID uint }

func (w systemWS) FindByID(id uint) (*models.Workspace, error) {
	if id == 2 {
		return &models.Workspace{ID: 2, System: true, OrganizationID: &w.orgID}, nil
	}
	return &models.Workspace{ID: id, OrganizationID: &w.orgID}, nil
}

// An organization's default location has to be one it can actually place in, or every workspace it
// creates starts with a default that placement silently skips.
func TestDefaultClusterMustBeUsable(t *testing.T) {
	s, _ := newOrgService(t)
	acme, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	other := uint(99)
	shared := models.Cluster{ID: 1}
	theirs := models.Cluster{ID: 2, OrganizationID: &other}
	ours := models.Cluster{ID: 3, OrganizationID: &acme.ID}

	// No clusters of its own: a shared location is fine, somebody else's is not.
	s.SetClusters(lookupStub{byID: map[uint]models.Cluster{1: shared, 2: theirs, 3: ours}})
	if _, err := s.Update(acme.ID, UpdateInput{DefaultClusterID: &shared.ID}); err != nil {
		t.Errorf("shared default for an unconfined organization: %v", err)
	}
	if _, err := s.Update(acme.ID, UpdateInput{DefaultClusterID: &theirs.ID}); !errors.Is(err, ErrClusterNotOurs) {
		t.Errorf("another organization's location: err = %v, want ErrClusterNotOurs", err)
	}

	// Once it runs its own, a shared location is no longer reachable either.
	s.SetClusters(lookupStub{byID: map[uint]models.Cluster{1: shared, 3: ours}, owned: map[uint]int64{acme.ID: 1}})
	if _, err := s.Update(acme.ID, UpdateInput{DefaultClusterID: &shared.ID}); !errors.Is(err, ErrClusterNotOurs) {
		t.Errorf("shared default for a confined organization: err = %v, want ErrClusterNotOurs", err)
	}
	if _, err := s.Update(acme.ID, UpdateInput{DefaultClusterID: &ours.ID}); err != nil {
		t.Errorf("its own location: %v", err)
	}
}

type lookupStub struct {
	byID  map[uint]models.Cluster
	owned map[uint]int64
}

func (l lookupStub) FindByID(id uint) (*models.Cluster, error) {
	c, ok := l.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &c, nil
}
func (l lookupStub) ListByOrganization(uint) ([]models.Cluster, error) { return nil, nil }
func (l lookupStub) CountByOrganization(id uint) (int64, error)        { return l.owned[id], nil }
func (l lookupStub) ReleaseOrganization(uint) error                    { return nil }

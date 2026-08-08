// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- fakes ---

type fakeAuth struct {
	ident enterprise.LDAPIdentity
	err   error
}

func (f *fakeAuth) Authenticate(context.Context, string, string) (enterprise.LDAPIdentity, error) {
	return f.ident, f.err
}
func (f *fakeAuth) TestConnection(context.Context, uint) (enterprise.LDAPTestResult, error) {
	return enterprise.LDAPTestResult{}, nil
}

// fakeEE satisfies enterprise.EE; only LDAP() is meaningful for these tests.
type fakeEE struct{ auth enterprise.LDAPAuthenticator }

func (f fakeEE) Entitlements() enterprise.Entitlements { return enterprise.Entitlements{} }
func (f fakeEE) Has(string) bool                       { return f.auth != nil }
func (f fakeEE) Mutable(string) bool                   { return f.auth != nil }
func (f fakeEE) Require(string) error                  { return nil }
func (f fakeEE) RequireMutable(string) error           { return nil }
func (f fakeEE) Install(context.Context, string, string) (enterprise.Entitlements, error) {
	return enterprise.Entitlements{}, nil
}
func (f fakeEE) Remove(context.Context) error       { return nil }
func (f fakeEE) InitSSO(enterprise.SSODeps)         {}
func (f fakeEE) SAML() enterprise.SAMLProvider      { return nil }
func (f fakeEE) SCIM() enterprise.SCIMProvider      { return nil }
func (f fakeEE) LDAP() enterprise.LDAPAuthenticator { return f.auth }

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// NB: models.Workspace embeds a Postgres-only gen_random_uuid() default that
	// sqlite rejects, and reconciliation only touches workspace_members, so the
	// workspaces table isn't migrated here — tests use a fixed workspace id.
	if err := db.AutoMigrate(&models.User{}, &models.WorkspaceMember{},
		&models.LDAPConfig{}, &models.LDAPGroupMapping{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newService(t *testing.T, db *gorm.DB, auth enterprise.LDAPAuthenticator) *Service {
	return NewService(fakeEE{auth: auth}, repositories.NewUserRepository(db),
		repositories.NewWorkspaceRepository(db), repositories.NewLDAPRepository(db))
}

// --- Login / provisioning ---

func TestLogin_NoDirectory_FallsThrough(t *testing.T) {
	db := newTestDB(t)
	s := newService(t, db, nil) // ee.LDAP() == nil (Community)
	user, err := s.Login(context.Background(), "alice", "pw")
	if err != nil || user != nil {
		t.Fatalf("want (nil,nil) fall-through, got (%v,%v)", user, err)
	}
}

func TestLogin_NoMatch_FallsThrough(t *testing.T) {
	db := newTestDB(t)
	s := newService(t, db, &fakeAuth{err: enterprise.ErrLDAPNoMatch})
	user, err := s.Login(context.Background(), "alice", "pw")
	if err != nil || user != nil {
		t.Fatalf("want (nil,nil), got (%v,%v)", user, err)
	}
}

func TestLogin_ProvisionsFirstUserAsAdmin(t *testing.T) {
	db := newTestDB(t)
	s := newService(t, db, &fakeAuth{ident: enterprise.LDAPIdentity{
		Email: "Alice@Corp.com", Name: "Alice", Username: "alice",
	}})
	user, err := s.Login(context.Background(), "alice", "pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user.Role != models.SystemRoleAdmin {
		t.Errorf("first user role = %q, want admin", user.Role)
	}
	if user.AuthSource != AuthSourceLDAP {
		t.Errorf("auth_source = %q, want ldap", user.AuthSource)
	}
	if user.Email != "alice@corp.com" {
		t.Errorf("email = %q, want lowercased", user.Email)
	}
	if user.EmailVerifiedAt == nil {
		t.Error("email should be marked verified")
	}
	if user.PasswordHash == "" {
		t.Error("expected an unusable password hash, not empty")
	}
}

func TestLogin_MatchesExistingByUsername(t *testing.T) {
	db := newTestDB(t)
	users := repositories.NewUserRepository(db)
	if err := users.Create(&models.User{Name: "Bob", Email: "bob@corp.com", Username: "bob", Role: models.SystemRoleUser, Active: true}); err != nil {
		t.Fatal(err)
	}
	s := newService(t, db, &fakeAuth{ident: enterprise.LDAPIdentity{Email: "different@corp.com", Username: "bob"}})
	user, err := s.Login(context.Background(), "bob", "pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user.Email != "bob@corp.com" {
		t.Errorf("matched wrong user: %q", user.Email)
	}
	var count int64
	db.Model(&models.User{}).Count(&count)
	if count != 1 {
		t.Errorf("expected no new user created, have %d", count)
	}
}

func TestLogin_DisabledAccount(t *testing.T) {
	db := newTestDB(t)
	users := repositories.NewUserRepository(db)
	if err := users.Create(&models.User{Name: "C", Email: "c@corp.com", Username: "carol", Role: models.SystemRoleUser, Active: true}); err != nil {
		t.Fatal(err)
	}
	// Active has a default:true tag, so a zero-value false is ignored on insert;
	// force it disabled explicitly.
	db.Model(&models.User{}).Where("username = ?", "carol").Update("active", false)
	s := newService(t, db, &fakeAuth{ident: enterprise.LDAPIdentity{Email: "c@corp.com", Username: "carol"}})
	_, err := s.Login(context.Background(), "carol", "pw")
	if !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("want ErrAccountDisabled, got %v", err)
	}
}

func TestLogin_NoEmail(t *testing.T) {
	db := newTestDB(t)
	s := newService(t, db, &fakeAuth{ident: enterprise.LDAPIdentity{Username: "noemail"}})
	_, err := s.Login(context.Background(), "noemail", "pw")
	if !errors.Is(err, ErrNoEmail) {
		t.Fatalf("want ErrNoEmail, got %v", err)
	}
}

func TestLogin_BindError_Propagates(t *testing.T) {
	db := newTestDB(t)
	bindErr := errors.New("bind failed")
	s := newService(t, db, &fakeAuth{err: bindErr})
	_, err := s.Login(context.Background(), "x", "bad")
	if !errors.Is(err, bindErr) {
		t.Fatalf("want bind error, got %v", err)
	}
}

// --- Group reconciliation ---

func TestReconcile_GrantsAdminAndWorkspace(t *testing.T) {
	db := newTestDB(t)
	// A seeded admin so the LDAP user isn't the first (and to allow later demotion).
	users := repositories.NewUserRepository(db)
	_ = users.Create(&models.User{Name: "root", Email: "root@corp.com", Username: "root", Role: models.SystemRoleAdmin, Active: true})
	// LDAP config with two mappings (workspace id is fixed; no workspaces table).
	ws := repositories.NewWorkspaceRepository(db)
	wsID := uint(1)
	ldapRepo := repositories.NewLDAPRepository(db)
	cfg := &models.LDAPConfig{Name: "corp", Host: "h", Enabled: true}
	_ = ldapRepo.Create(cfg)
	_ = ldapRepo.CreateMapping(&models.LDAPGroupMapping{LDAPConfigID: cfg.ID, GroupDN: "cn=platform-admins,ou=g,dc=c", SystemAdmin: true})
	_ = ldapRepo.CreateMapping(&models.LDAPGroupMapping{LDAPConfigID: cfg.ID, GroupDN: "eng-team", WorkspaceID: &wsID, WorkspaceRole: models.WorkspaceRoleDeveloper})

	s := newService(t, db, &fakeAuth{ident: enterprise.LDAPIdentity{
		Email: "dana@corp.com", Username: "dana", Provider: "corp",
		Groups: []string{"CN=Platform-Admins,OU=g,DC=c", "cn=eng-team,ou=g,dc=c"},
	}})
	user, err := s.Login(context.Background(), "dana", "pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user.Role != models.SystemRoleAdmin {
		t.Errorf("expected admin via group, got %q", user.Role)
	}
	m, err := ws.FindMember(wsID, user.ID)
	if err != nil {
		t.Fatalf("expected workspace membership: %v", err)
	}
	if m.Role != models.WorkspaceRoleDeveloper {
		t.Errorf("member role = %q, want developer", m.Role)
	}
}

func TestReconcile_RemovesManagedMembershipWhenNoLongerMatched(t *testing.T) {
	db := newTestDB(t)
	users := repositories.NewUserRepository(db)
	_ = users.Create(&models.User{Name: "root", Email: "root@corp.com", Username: "root", Role: models.SystemRoleAdmin, Active: true})
	ws := repositories.NewWorkspaceRepository(db)
	wsID := uint(1)
	ldapRepo := repositories.NewLDAPRepository(db)
	cfg := &models.LDAPConfig{Name: "corp", Host: "h", Enabled: true}
	_ = ldapRepo.Create(cfg)
	_ = ldapRepo.CreateMapping(&models.LDAPGroupMapping{LDAPConfigID: cfg.ID, GroupDN: "eng-team", WorkspaceID: &wsID, WorkspaceRole: models.WorkspaceRoleDeveloper})

	// First login: user is in the group → gets membership.
	auth := &fakeAuth{ident: enterprise.LDAPIdentity{Email: "e@corp.com", Username: "erin", Provider: "corp", Groups: []string{"cn=eng-team,ou=g,dc=c"}}}
	s := newService(t, db, auth)
	user, _ := s.Login(context.Background(), "erin", "pw")
	if _, err := ws.FindMember(wsID, user.ID); err != nil {
		t.Fatalf("expected initial membership: %v", err)
	}
	// Second login: no longer in the group → membership revoked (managed workspace).
	auth.ident.Groups = nil
	if _, err := s.Login(context.Background(), "erin", "pw"); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, err := ws.FindMember(wsID, user.ID); err == nil {
		t.Error("expected managed membership to be revoked when group no longer matches")
	}
}

// --- pure helpers ---

func TestGroupMatches(t *testing.T) {
	groups := normalizeGroups([]string{"CN=Admins,OU=Groups,DC=corp,DC=com", "cn=devs,ou=g,dc=c"})
	cases := []struct {
		mapping string
		want    bool
	}{
		{"cn=admins,ou=groups,dc=corp,dc=com", true}, // full DN, case-insensitive
		{"CN=Admins,OU=Groups,DC=corp,DC=com", true},
		{"admins", true}, // bare CN
		{"devs", true},   // bare CN
		{"unknown", false},
		{"cn=unknown,ou=g,dc=c", false},
		{"", false},
	}
	for _, c := range cases {
		if got := groupMatches(groups, c.mapping); got != c.want {
			t.Errorf("groupMatches(%q) = %v, want %v", c.mapping, got, c.want)
		}
	}
}

func TestRoleRank(t *testing.T) {
	if !(roleRank(models.WorkspaceRoleOwner) > roleRank(models.WorkspaceRoleAdmin) &&
		roleRank(models.WorkspaceRoleAdmin) > roleRank(models.WorkspaceRoleDeveloper) &&
		roleRank(models.WorkspaceRoleDeveloper) > roleRank(models.WorkspaceRoleViewer)) {
		t.Error("role ranks are not strictly ordered owner>admin>developer>viewer")
	}
}

// A directory handle is stored through slug.Make in BeforeCreate, but the "have we seen this user?"
// lookup was a plain lowercase — so a handle that slugifies to something else missed the existing
// account and then collided with it on insert, and the sign-in failed on the unique index.
func TestProvisionHandleNormalisation(t *testing.T) {
	db := newTestDB(t)
	users := repositories.NewUserRepository(db)

	// Someone already holds the slugified form of the directory handle.
	if err := users.Create(&models.User{
		Name: "Jane Doe", Email: "jane@example.com", Username: "j.doe",
		PasswordHash: "x", Role: models.SystemRoleUser, Active: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded, err := users.FindByEmail("jane@example.com")
	if err != nil {
		t.Fatalf("seed lookup: %v", err)
	}
	if seeded.Username != "j-doe" {
		t.Fatalf("stored handle = %q, want the slugified %q", seeded.Username, "j-doe")
	}

	svc := newService(t, db, &fakeAuth{})
	// The same person signing in through the directory, same handle, same email.
	u, err := svc.provision(enterprise.LDAPIdentity{Username: "J.Doe", Email: "jane@example.com", Name: "Jane Doe"})
	if err != nil {
		t.Fatalf("provision matched no one and failed: %v", err)
	}
	if u.ID != seeded.ID {
		t.Fatalf("provisioned a second account (id %d) for the existing user (id %d)", u.ID, seeded.ID)
	}
}

// A directory handle that is reserved must not be stored verbatim: BeforeCreate
// trusts a supplied username, so nothing else would stop "admin" from being
// claimed — the derived path deliberately avoids the reserved set.
func TestProvisionReservedHandle(t *testing.T) {
	db := newTestDB(t)
	svc := newService(t, db, &fakeAuth{})

	u, err := svc.provision(enterprise.LDAPIdentity{Username: "admin", Email: "ops@example.com", Name: "Ops"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if u.Username == "admin" {
		t.Fatal("claimed the reserved handle \"admin\"")
	}
	if u.Username != "admin-1" {
		t.Fatalf("handle = %q, want the suffixed %q", u.Username, "admin-1")
	}
}

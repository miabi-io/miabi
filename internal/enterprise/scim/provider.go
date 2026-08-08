//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

// Package scim implements a minimal SCIM 2.0 Users provisioning endpoint for identity providers.
// It is compiled only into the Enterprise build. Create/replace provisions a Miabi user; setting
// active=false (or DELETE) deprovisions by disabling the account.
package scim

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const userSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
const listSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
const errSchema = "urn:ietf:params:scim:api:messages:2.0:Error"

// Deps are the core dependencies for the SCIM handler.
type Deps struct {
	DB       *gorm.DB
	Entitled func() bool // re-checks the scim license per request
}

// Provider is the SCIM 2.0 handler set. Users/Groups satisfy enterprise.SCIMProvider.
type Provider struct {
	db       *gorm.DB
	tokens   *repositories.SCIMTokenRepository
	entitled func() bool
}

func New(d Deps) *Provider {
	entitled := d.Entitled
	if entitled == nil {
		entitled = func() bool { return true }
	}
	return &Provider{db: d.DB, tokens: repositories.NewSCIMTokenRepository(d.DB), entitled: entitled}
}

// authenticate validates the Bearer SCIM token. Returns false (and writes a 401)
// when missing/unknown.
func (p *Provider) authenticate(c *okapi.Context) bool {
	h := c.Header("Authorization")
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		_ = scimError(c, http.StatusUnauthorized, "missing bearer token")
		return false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(h, pfx))
	tok, err := p.tokens.FindByHash(repositories.HashSCIMToken(raw))
	if err != nil {
		_ = scimError(c, http.StatusUnauthorized, "invalid SCIM token")
		return false
	}
	p.tokens.TouchLastUsed(tok.ID)
	return true
}

// Users dispatches the SCIM /Users collection and item operations by method.
func (p *Provider) Users(c *okapi.Context) error {
	if !p.entitled() {
		return scimError(c, http.StatusPaymentRequired, "SCIM requires an active Enterprise license")
	}
	if !p.authenticate(c) {
		return nil
	}
	id := strings.TrimSpace(c.Param("id"))
	switch c.Request().Method {
	case http.MethodGet:
		if id == "" {
			return p.listUsers(c)
		}
		return p.getUser(c, id)
	case http.MethodPost:
		return p.createUser(c)
	case http.MethodPut:
		return p.replaceUser(c, id)
	case http.MethodPatch:
		return p.patchUser(c, id)
	case http.MethodDelete:
		return p.deleteUser(c, id)
	default:
		return scimError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Groups returns an empty list — group provisioning is not modeled yet, but the
// endpoint exists so IdPs that probe it succeed.
func (p *Provider) Groups(c *okapi.Context) error {
	if !p.entitled() {
		return scimError(c, http.StatusPaymentRequired, "SCIM requires an active Enterprise license")
	}
	if !p.authenticate(c) {
		return nil
	}
	return c.JSON(http.StatusOK, listResponse(nil))
}

type scimName struct {
	Formatted string `json:"formatted,omitempty"`
}
type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}
type scimUser struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id,omitempty"`
	UserName    string      `json:"userName"`
	Name        scimName    `json:"name"`
	DisplayName string      `json:"displayName,omitempty"`
	Active      bool        `json:"active"`
	Emails      []scimEmail `json:"emails,omitempty"`
	Meta        *scimMeta   `json:"meta,omitempty"`
}
type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

func toSCIM(u *models.User) scimUser {
	return scimUser{
		Schemas: []string{userSchema}, ID: strconv.FormatUint(uint64(u.ID), 10),
		UserName: u.Email, Name: scimName{Formatted: u.Name}, DisplayName: u.Name,
		Active: u.Active, Emails: []scimEmail{{Value: u.Email, Primary: true}},
		Meta: &scimMeta{ResourceType: "User", Created: u.CreatedAt.Format(time.RFC3339), LastModified: u.UpdatedAt.Format(time.RFC3339)},
	}
}

func emailOf(in scimUser) string {
	if e := strings.TrimSpace(in.UserName); e != "" {
		return strings.ToLower(e)
	}
	for _, e := range in.Emails {
		if v := strings.TrimSpace(e.Value); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

func nameOf(in scimUser, email string) string {
	if n := strings.TrimSpace(in.Name.Formatted); n != "" {
		return n
	}
	if n := strings.TrimSpace(in.DisplayName); n != "" {
		return n
	}
	return email
}

func (p *Provider) listUsers(c *okapi.Context) error {
	// IdPs query by userName before creating. Support the common
	// filter=userName eq "x"; otherwise return all (bounded).
	var users []models.User
	if f := c.Query("filter"); f != "" {
		if email := parseUserNameFilter(f); email != "" {
			p.db.Where("email = ?", email).Find(&users)
		}
	} else {
		p.db.Limit(200).Find(&users)
	}
	res := make([]scimUser, 0, len(users))
	for i := range users {
		res = append(res, toSCIM(&users[i]))
	}
	return c.JSON(http.StatusOK, listResponse(res))
}

func (p *Provider) getUser(c *okapi.Context, id string) error {
	u, err := p.find(id)
	if err != nil {
		return scimError(c, http.StatusNotFound, "user not found")
	}
	return c.JSON(http.StatusOK, toSCIM(u))
}

func (p *Provider) createUser(c *okapi.Context) error {
	var in scimUser
	if err := json.NewDecoder(c.Request().Body).Decode(&in); err != nil {
		return scimError(c, http.StatusBadRequest, "invalid SCIM payload")
	}
	email := emailOf(in)
	if email == "" {
		return scimError(c, http.StatusBadRequest, "userName/email is required")
	}
	// Idempotent: if the user already exists, return it (200) rather than erroring.
	var existing models.User
	if err := p.db.Where("email = ?", email).First(&existing).Error; err == nil {
		return c.JSON(http.StatusOK, toSCIM(&existing))
	}
	now := time.Now()
	u := models.User{
		Name: nameOf(in, email), Email: email, PasswordHash: randomPassword(),
		Role: models.SystemRoleUser, Active: in.Active, EmailVerifiedAt: &now,
	}
	if err := p.db.Create(&u).Error; err != nil {
		return scimError(c, http.StatusConflict, "could not create user")
	}
	return c.JSON(http.StatusCreated, toSCIM(&u))
}

func (p *Provider) replaceUser(c *okapi.Context, id string) error {
	u, err := p.find(id)
	if err != nil {
		return scimError(c, http.StatusNotFound, "user not found")
	}
	var in scimUser
	if err := json.NewDecoder(c.Request().Body).Decode(&in); err != nil {
		return scimError(c, http.StatusBadRequest, "invalid SCIM payload")
	}
	if n := nameOf(in, u.Email); n != "" {
		u.Name = n
	}
	u.Active = in.Active // PUT replaces — deprovision when active=false
	if err := p.db.Save(u).Error; err != nil {
		return scimError(c, http.StatusInternalServerError, "could not update user")
	}
	return c.JSON(http.StatusOK, toSCIM(u))
}

// patchUser handles the common SCIM PATCH operations, chiefly replacing the
// active flag (deprovisioning).
func (p *Provider) patchUser(c *okapi.Context, id string) error {
	u, err := p.find(id)
	if err != nil {
		return scimError(c, http.StatusNotFound, "user not found")
	}
	var body struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return scimError(c, http.StatusBadRequest, "invalid SCIM payload")
	}
	for _, op := range body.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(op.Path)) {
		case "active":
			var active bool
			if json.Unmarshal(op.Value, &active) == nil {
				u.Active = active
			}
		case "": // value is an object of attributes
			var obj struct {
				Active *bool `json:"active"`
				Name   *struct {
					Formatted string `json:"formatted"`
				} `json:"name"`
			}
			if json.Unmarshal(op.Value, &obj) == nil {
				if obj.Active != nil {
					u.Active = *obj.Active
				}
				if obj.Name != nil && strings.TrimSpace(obj.Name.Formatted) != "" {
					u.Name = obj.Name.Formatted
				}
			}
		}
	}
	if err := p.db.Save(u).Error; err != nil {
		return scimError(c, http.StatusInternalServerError, "could not update user")
	}
	return c.JSON(http.StatusOK, toSCIM(u))
}

// deleteUser deprovisions by disabling the account (we never hard-delete a user
// and their owned resources via SCIM).
func (p *Provider) deleteUser(c *okapi.Context, id string) error {
	u, err := p.find(id)
	if err != nil {
		return scimError(c, http.StatusNotFound, "user not found")
	}
	u.Active = false
	if err := p.db.Save(u).Error; err != nil {
		return scimError(c, http.StatusInternalServerError, "could not deprovision user")
	}
	return c.Data(http.StatusNoContent, "application/scim+json", nil)
}

func (p *Provider) find(id string) (*models.User, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return nil, err
	}
	var u models.User
	if err := p.db.First(&u, uint(n)).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func listResponse(res []scimUser) map[string]any {
	if res == nil {
		res = []scimUser{}
	}
	return map[string]any{
		"schemas": []string{listSchema}, "totalResults": len(res),
		"startIndex": 1, "itemsPerPage": len(res), "Resources": res,
	}
}

func scimError(c *okapi.Context, status int, detail string) error {
	return c.JSON(status, map[string]any{
		"schemas": []string{errSchema}, "status": strconv.Itoa(status), "detail": detail,
	})
}

func parseUserNameFilter(f string) string {
	// Minimal parser for: userName eq "value"
	parts := strings.Fields(f)
	if len(parts) >= 3 && strings.EqualFold(parts[0], "userName") && strings.EqualFold(parts[1], "eq") {
		return strings.ToLower(strings.Trim(strings.Join(parts[2:], " "), `"`))
	}
	return ""
}

func randomPassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	h, _ := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	return string(h)
}

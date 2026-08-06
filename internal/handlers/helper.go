// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ok writes a 200 response wrapping data in the standard envelope.
func ok[T any](c *okapi.Context, data T) error {
	return c.JSON(http.StatusOK, dto.Response[T]{Success: true, Data: data})
}

// created writes a 201 response wrapping data in the standard envelope.
func created[T any](c *okapi.Context, data T) error {
	return c.JSON(http.StatusCreated, dto.Response[T]{Success: true, Data: data})
}

// message writes a 200 response carrying a single message.
func message(c *okapi.Context, msg string) error {
	return ok(c, dto.MessageData{Message: msg})
}

// userIDPtr returns the request's actor as a pointer, or nil when there is no
// authenticated user (an API-key call). Services take *uint so a run or record
// can be left unattributed rather than attributed to user 0.
func userIDPtr(c *okapi.Context) *uint {
	id := middlewares.UserID(c)
	if id == 0 {
		return nil
	}
	return &id
}

// quotaAbort maps a plan quota / capability error to a 403 response. Returns nil
// when err is not a quota error, so callers fall through to their own mapping.
func quotaAbort(c *okapi.Context, err error) error {
	if errors.Is(err, quota.ErrQuotaExceeded) || errors.Is(err, quota.ErrCapabilityDenied) {
		return c.AbortForbidden(err.Error(), err)
	}
	return nil
}

// entitlementAbort maps an enterprise license/entitlement error to its HTTP
// status (402 license_required / license_expired, 403 entitlement_denied),
// preserving the stable error code via the envelope. Returns nil when err is not
// a gate error, so callers fall through to their own mapping.
func entitlementAbort(c *okapi.Context, err error) error {
	if err == nil {
		return nil
	}
	var ge interface{ Status() int }
	if errors.As(err, &ge) {
		return c.AbortWithError(ge.Status(), err)
	}
	return nil
}

// appIDParam parses the {appID} path parameter.
func appIDParam(c *okapi.Context) (uint, error) {
	id, err := strconv.Atoi(c.Param("appID"))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid app id")
	}
	return uint(id), nil
}

// resolveID resolves a route param that is either a numeric primary key or a
// resource uid (UUID) to the numeric id, letting every public resource route
// accept both forms (numeric for the web UI, uid for Terraform/clients).
// Ownership is still enforced by the caller's workspace-scoped lookup, so a uid
// from another workspace resolves but then 404s.
func resolveID(ref string, resolveUID func(string) (uint, error)) (uint, error) {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil && n > 0 {
		return uint(n), nil
	}
	if _, err := uuid.Parse(ref); err == nil {
		return resolveUID(ref)
	}
	return 0, errors.New("invalid id")
}

// uintParam parses a named positive-integer path parameter.
func uintParam(c *okapi.Context, name string) (uint, error) {
	id, err := strconv.Atoi(c.Param(name))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid " + name)
	}
	return uint(id), nil
}

// optionalUintRef decodes a JSON field that may be absent, null, or a positive
// integer — the tri-state a partial update needs to distinguish "leave unchanged"
// from "clear" from "set". `present` is false when the field was omitted (leave
// unchanged); when present, `value` is nil for JSON null (clear the reference) or
// the parsed id. A zero or non-integer value is an error.
func optionalUintRef(raw json.RawMessage) (present bool, value *uint, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false, nil, nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return true, nil, nil
	}
	var n uint
	if err := json.Unmarshal(raw, &n); err != nil || n == 0 {
		return true, nil, errors.New("invalid reference")
	}
	return true, &n, nil
}

// selfOwnerMeta records the authenticated user as the owner of a resource they
// create by hand (owner-kind=user, id+resolved name). The display name is
// best-effort; a lookup miss still records the id. Higher-level callers
// (marketplace/stack/apply) pass an app/database/stack owner instead, which wins
// over this default via models.DefaultOwner.
func selfOwnerMeta(users *repositories.UserRepository, c *okapi.Context) models.Metadata {
	uid := middlewares.UserID(c)
	if uid == 0 {
		return nil
	}
	name := ""
	if users != nil {
		if u, err := users.FindByID(uid); err == nil && u != nil {
			if name = u.Name; name == "" {
				name = u.Email
			}
		}
	}
	return models.SetOwner(nil, models.OwnerUser, uid, name)
}

// Shared error values for admin handlers.
var (
	errEmailTaken         = errors.New("email already registered")
	errInvalidID          = errors.New("invalid id")
	errSlugTaken          = errors.New("slug already in use")
	errAccountUnavailable = errors.New("account service unavailable")
	errUsernameTaken      = errors.New("username already taken")
	errUsernameInvalid    = errors.New("username must be lowercase letters, digits and hyphens, and not a reserved word")
)

// validateUsername normalizes and validates a user-chosen username handle,
// returning the canonical form. A blank desired value returns ("", nil) so a
// caller can treat an omitted username as "leave unchanged". excludeUserID is
// skipped in the uniqueness check, so re-submitting an unchanged username is a
// no-op (pass 0 when creating). Errors: errUsernameInvalid (malformed or
// reserved), errUsernameTaken (already held by another user).
func validateUsername(users *repositories.UserRepository, desired string, excludeUserID uint) (string, error) {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		return "", nil
	}
	handle := slug.Make(desired, "")
	if !slug.IsAvailableHandle(handle) {
		return "", errUsernameInvalid
	}
	if existing, err := users.FindByUsername(handle); err == nil && existing.ID != excludeUserID {
		return "", errUsernameTaken
	}
	return handle, nil
}

// pageParams parses the standard ?limit / ?offset query parameters, clamping
// limit to a sane default and maximum.
func pageParams(c *okapi.Context) (limit, offset int) {
	limit, _ = strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ = strconv.Atoi(c.Query("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// queryInt reads an integer query parameter, returning def when absent/invalid.
func queryInt(c *okapi.Context, key string, def int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return def
}

// queryBool reads an optional tri-state boolean query parameter: "true"/"false"
// select a value, anything else (including absent) means "no filter" and yields
// nil. Use it for filters where "either" is a meaningful third choice, so a
// caller can't accidentally narrow the result by omitting the parameter.
func queryBool(c *okapi.Context, key string) *bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	default:
		return nil
	}
}

// timeRange parses the ?from / ?to query params into a created_at window.
// Each accepts RFC3339 or a YYYY-MM-DD date; absent/invalid values yield a zero
// time (unbounded). A date-only `to` is advanced to the next midnight so the
// upper bound — applied as `created_at < to` — includes the whole day.
func timeRange(c *okapi.Context) (from, to time.Time) {
	return parseTimeParam(c.Query("from"), false), parseTimeParam(c.Query("to"), true)
}

func parseTimeParam(s string, endExclusive bool) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endExclusive {
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	return time.Time{}
}

// normalizePageParams clamps a (page, size) pair and computes the offset. Page
// is 0-indexed; size defaults to 20 and is capped at 100 (Posta convention).
func normalizePageParams(page, size int) (normPage, normSize, offset int) {
	if size <= 0 || size > 100 {
		size = 20
	}
	if page < 0 {
		page = 0
	}
	return page, size, page * size
}

// paginated writes a PageableResponse with full pagination metadata, computing
// total_pages from total and size.
func paginated[T any](c *okapi.Context, items []T, total int64, page, size int) error {
	if items == nil {
		items = []T{}
	}
	totalPages := 0
	if size > 0 {
		totalPages = int((total + int64(size) - 1) / int64(size))
	}
	return c.JSON(http.StatusOK, dto.PageableResponse[T]{
		Success: true,
		Data:    items,
		Pageable: dto.Pageable{
			CurrentPage:   page,
			Size:          size,
			TotalPages:    totalPages,
			TotalElements: total,
			Empty:         len(items) == 0,
		},
	})
}

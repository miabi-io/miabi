// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// AssetSlot names an uploadable brand image.
type AssetSlot string

const (
	AssetLogo     AssetSlot = "logo"
	AssetLogoDark AssetSlot = "logo_dark"
	AssetFavicon  AssetSlot = "favicon"
)

// MaxAssetBytes caps an upload. Logos render at 28–48 px and every sign-in page
// load serves them, so anything larger is an unoptimised export.
const MaxAssetBytes = 512 << 10

// assetPath is where auth_routes serves uploads. Under /auth because that group is
// public: the sign-in page shows them before anyone has a session.
const assetPath = "/api/v1/auth/branding/"

const assetVersionLen = 12

var (
	ErrUnknownAsset     = errors.New("image slot must be one of: logo, logo_dark, favicon")
	ErrAssetTooLarge    = fmt.Errorf("an image may be at most %d KiB", MaxAssetBytes>>10)
	ErrUnsupportedImage = errors.New("an image must be PNG, JPEG, WebP, SVG or ICO")
	ErrAssetNotFound    = errors.New("no image has been uploaded for this slot")
)

// Asset describes an uploaded image without its bytes.
type Asset struct {
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ParseAssetSlot validates a slot name from a request path.
func ParseAssetSlot(raw string) (AssetSlot, error) {
	switch s := AssetSlot(raw); s {
	case AssetLogo, AssetLogoDark, AssetFavicon:
		return s, nil
	}
	return "", ErrUnknownAsset
}

// AssetURL is the versioned address of an upload. The version is the content hash,
// so a replaced image gets a new URL and the old one can be cached for good.
func AssetURL(slot AssetSlot, sha string) string {
	v := sha
	if len(v) > assetVersionLen {
		v = v[:assetVersionLen]
	}
	return assetPath + string(slot) + "?v=" + v
}

// AssetCacheControl caches a request for good only when it names the current
// version; a bare or stale URL revalidates, so a replaced image is never pinned.
func AssetCacheControl(version, sha string) string {
	if len(version) == assetVersionLen && strings.HasPrefix(sha, version) {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

// DetectImage returns the content type of an accepted image. It is read from the
// bytes, never from the filename or the upload's header, which the client chooses.
func DetectImage(data []byte) (string, error) {
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/webp", "image/x-icon":
		return ct, nil
	}
	if isSVG(data) {
		return "image/svg+xml", nil
	}
	return "", ErrUnsupportedImage
}

// isSVG reports whether data is an XML document whose root element is <svg>. A
// prolog, comments and a doctype may come first; an HTML page carrying an inline
// <svg> is refused, because its root is not one.
func isSVG(data []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t.Name.Local == "svg"
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return false
			}
		}
	}
}

// Assets lists the uploaded images by slot. Unreadable means none: the sign-in page
// falls back to URLs and Miabi's own marks rather than failing.
func (s *Service) Assets() map[AssetSlot]Asset {
	out := map[AssetSlot]Asset{}
	rows, err := s.assets.List()
	if err != nil {
		return out
	}
	for _, r := range rows {
		slot, err := ParseAssetSlot(r.Slot)
		if err != nil {
			continue
		}
		out[slot] = Asset{URL: AssetURL(slot, r.SHA256), ContentType: r.ContentType, Size: r.Size, UpdatedAt: r.UpdatedAt}
	}
	return out
}

// PutAsset validates and stores an image for slot, replacing any earlier upload.
func (s *Service) PutAsset(slot AssetSlot, data []byte) error {
	if len(data) > MaxAssetBytes {
		return ErrAssetTooLarge
	}
	ct, err := DetectImage(data)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	return s.assets.Put(&models.BrandAsset{
		Slot:        string(slot),
		ContentType: ct,
		SHA256:      hex.EncodeToString(sum[:]),
		Size:        len(data),
		Data:        data,
	})
}

// DeleteAsset removes a slot's upload; the matching URL, if any, applies again.
func (s *Service) DeleteAsset(slot AssetSlot) error { return s.assets.Delete(string(slot)) }

// OpenAsset returns a slot's image with its bytes.
func (s *Service) OpenAsset(slot AssetSlot) (*models.BrandAsset, error) {
	a, err := s.assets.Get(string(slot))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssetNotFound
	}
	return a, err
}

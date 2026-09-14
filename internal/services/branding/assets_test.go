// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectImage(t *testing.T) {
	accepted := map[string]struct{ data, want string }{
		"png":             {"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", "image/png"},
		"jpeg":            {"\xff\xd8\xff\xe0\x00\x10JFIF", "image/jpeg"},
		"webp":            {"RIFF\x00\x00\x00\x00WEBPVP8 ", "image/webp"},
		"ico":             {"\x00\x00\x01\x00\x01\x00\x10\x10", "image/x-icon"},
		"bare svg":        {`<svg xmlns="http://www.w3.org/2000/svg"/>`, "image/svg+xml"},
		"svg with prolog": {"\n<?xml version=\"1.0\"?>\n<!-- mark -->\n<!DOCTYPE svg PUBLIC \"-//W3C//DTD SVG 1.1//EN\" \"x\">\n<svg></svg>", "image/svg+xml"},
	}
	for name, tc := range accepted {
		t.Run(name, func(t *testing.T) {
			got, err := DetectImage([]byte(tc.data))
			if err != nil || got != tc.want {
				t.Fatalf("DetectImage = %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	// Served from this origin, a file that is really a page would be stored XSS the
	// moment someone opens its URL.
	refused := map[string]string{
		"empty":            "",
		"html":             "<!DOCTYPE html><html><body><script>alert(1)</script></body></html>",
		"html with an svg": "<html><body><svg></svg></body></html>",
		"other xml root":   `<?xml version="1.0"?><feed><svg/></feed>`,
		"text before svg":  "hello <svg/>",
		"plain text":       "not an image",
		"gif":              "GIF89a\x01\x00\x01\x00",
	}
	for name, data := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := DetectImage([]byte(data)); !errors.Is(err, ErrUnsupportedImage) {
				t.Fatalf("DetectImage = %v, want ErrUnsupportedImage", err)
			}
		})
	}
}

func TestParseAssetSlot(t *testing.T) {
	for _, ok := range []string{"logo", "logo_dark", "favicon"} {
		if _, err := ParseAssetSlot(ok); err != nil {
			t.Errorf("ParseAssetSlot(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Logo", "../logo", "hero"} {
		if _, err := ParseAssetSlot(bad); !errors.Is(err, ErrUnknownAsset) {
			t.Errorf("ParseAssetSlot(%q) = %v, want ErrUnknownAsset", bad, err)
		}
	}
}

// A versioned URL may be cached forever only while it names the bytes actually
// served; otherwise a replaced logo would stay stuck in browsers.
func TestAssetCaching(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	url := AssetURL(AssetLogo, sha)
	if url != "/api/v1/auth/branding/logo?v="+sha[:assetVersionLen] {
		t.Fatalf("AssetURL = %q", url)
	}
	version := url[strings.Index(url, "=")+1:]
	if got := AssetCacheControl(version, sha); !strings.Contains(got, "immutable") {
		t.Errorf("current version: Cache-Control = %q, want immutable", got)
	}
	for name, v := range map[string]string{"bare": "", "stale": "cdcdcdcdcdcd", "short prefix": "ab"} {
		if got := AssetCacheControl(v, sha); got != "no-cache" {
			t.Errorf("%s version: Cache-Control = %q, want no-cache", name, got)
		}
	}
}

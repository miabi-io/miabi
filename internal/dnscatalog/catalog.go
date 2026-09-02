// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dnscatalog is the single source of truth for the DNS hosts Miabi can connect to. Each
// type has one Descriptor driving validation, secret handling, the API enum and the UI form.
package dnscatalog

import (
	"sort"
	"strings"
	"time"
)

const (
	FieldString   = "string"
	FieldPassword = "password"
	FieldTextarea = "textarea"
	FieldEnum     = "enum"
)

// Field is one credential input.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
}

// Descriptor declares one DNS host.
type Descriptor struct {
	Type    string  `json:"type"`
	Label   string  `json:"label"`
	Fields  []Field `json:"fields"`
	DocsURL string  `json:"docs_url,omitempty"`
	// ChallengeOnly marks a host that solves DNS-01 but cannot own A/AAAA records.
	ChallengeOnly bool `json:"challenge_only,omitempty"`
	// PropagationTimeout raises the DNS-01 propagation cap for slow hosts. Zero uses the ACME
	// client's default; it is a ceiling, not a wait.
	PropagationTimeout time.Duration `json:"-"`
}

var registry = []Descriptor{
	{
		Type:    "cloudflare",
		Label:   "Cloudflare",
		DocsURL: "https://dash.cloudflare.com/profile/api-tokens",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true,
				Help: "A token with Zone:DNS:Edit on the zones you want Miabi to manage."},
		},
	},
	{
		Type:    "digitalocean",
		Label:   "DigitalOcean",
		DocsURL: "https://cloud.digitalocean.com/account/api/tokens",
		Fields: []Field{
			{Key: "api_token", Label: "API token", Type: FieldPassword, Required: true, Secret: true,
				Help: "A personal access token with write scope."},
		},
	},
	{
		Type:    "route53",
		Label:   "AWS Route 53",
		DocsURL: "https://console.aws.amazon.com/iam/home#/security_credentials",
		Fields: []Field{
			{Key: "access_key_id", Label: "Access key ID", Type: FieldString, Required: true},
			{Key: "secret_access_key", Label: "Secret access key", Type: FieldPassword, Required: true, Secret: true},
			{Key: "region", Label: "Region", Type: FieldString, Placeholder: "us-east-1",
				Help: "Optional. Route 53 is global; this only selects the API endpoint."},
		},
	},
}

var byType = func() map[string]Descriptor {
	m := make(map[string]Descriptor, len(registry))
	for _, d := range registry {
		m[d.Type] = d
	}
	return m
}()

func Get(t string) (Descriptor, bool) {
	d, ok := byType[strings.ToLower(strings.TrimSpace(t))]
	return d, ok
}

// All returns every descriptor in display order.
func All() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}

// Types returns every catalogued type, sorted, for OpenAPI descriptions and error messages.
func Types() []string {
	out := make([]string, 0, len(registry))
	for _, d := range registry {
		out = append(out, d.Type)
	}
	sort.Strings(out)
	return out
}

// Validate reports the first required field the credentials leave empty.
func (d Descriptor) Validate(creds map[string]string) error {
	for _, f := range d.Fields {
		if f.Required && strings.TrimSpace(creds[f.Key]) == "" {
			return &MissingFieldError{Type: d.Type, Field: f.Key, Label: f.Label}
		}
	}
	return nil
}

// Public filters credentials to the fields safe to return.
func (d Descriptor) Public(creds map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range d.Fields {
		if f.Secret {
			continue
		}
		if v, ok := creds[f.Key]; ok && v != "" {
			out[f.Key] = v
		}
	}
	return out
}

type MissingFieldError struct {
	Type  string
	Field string
	Label string
}

func (e *MissingFieldError) Error() string {
	return e.Type + ": " + e.Label + " (" + e.Field + ") is required"
}

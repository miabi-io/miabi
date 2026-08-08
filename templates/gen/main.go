// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command gen vendors the official templates from the marketplace registry's published export.json
// into miabi/templates/, the embedded offline floor. It writes each version's template.yaml
// byte-for-byte, regenerates index.yaml, and rewrites the //go:embed list in embed.go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type bundle struct {
	Templates []struct {
		Name     string `json:"name"`
		Source   string `json:"source"`
		Versions []struct {
			Version  string `json:"version"`
			Digest   string `json:"digest"`
			Manifest string `json:"manifest"`
		} `json:"versions"`
	} `json:"templates"`
}

type tmplVersion struct {
	Version  string
	Digest   string
	Manifest string
}

type tmpl struct {
	Name     string
	Versions []tmplVersion
}

func main() {
	source := flag.String("source", "../../marketplace/export.json", "marketplace export.json (file path or http(s) URL)")
	out := flag.String("out", ".", "output directory (miabi/templates)")
	flag.Parse()
	if err := run(*source, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(source, out string) error {
	data, err := load(source)
	if err != nil {
		return err
	}
	var b bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("decode bundle: %w", err)
	}

	// Keep only official templates, sorted by name for a stable index.
	var officials []tmpl
	keep := map[string]bool{}
	for _, t := range b.Templates {
		if t.Source != "official" {
			continue
		}
		keep[t.Name] = true
		ot := tmpl{Name: t.Name}
		for _, v := range t.Versions {
			ot.Versions = append(ot.Versions, tmplVersion{Version: v.Version, Digest: v.Digest, Manifest: v.Manifest})
		}
		officials = append(officials, ot)
	}
	if len(officials) == 0 {
		return fmt.Errorf("no official templates found in %s", source)
	}
	sort.Slice(officials, func(i, j int) bool { return officials[i].Name < officials[j].Name })

	// Remove stale template-slug directories no longer in the official set, so the
	// snapshot is exact (a removed template leaves no orphan).
	if err := removeStale(out, keep); err != nil {
		return err
	}

	// Write each version's manifest byte-for-byte (the digest must keep matching).
	for _, t := range officials {
		for _, v := range t.Versions {
			dir := filepath.Join(out, t.Name, v.Version)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "template.yaml"), []byte(v.Manifest), 0o644); err != nil {
				return err
			}
		}
	}

	if err := writeIndex(out, officials); err != nil {
		return err
	}
	slugs := make([]string, len(officials))
	for i, t := range officials {
		slugs[i] = t.Name
	}
	if err := rewriteEmbed(filepath.Join(out, "embed.go"), slugs); err != nil {
		return err
	}
	fmt.Printf("vendored %d official templates into %s\n", len(officials), out)
	return nil
}

func load(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(source) //nolint:noctx // short-lived build tool
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: status %d", source, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(source)
}

// removeStale deletes template-slug directories under out whose slug is not in
// keep. A directory is a template dir if it contains a <version>/template.yaml.
func removeStale(out string, keep map[string]bool) error {
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		if isTemplateDir(filepath.Join(out, e.Name())) {
			if err := os.RemoveAll(filepath.Join(out, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func isTemplateDir(dir string) bool {
	vers, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, v := range vers {
		if v.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, v.Name(), "template.yaml")); err == nil {
				return true
			}
		}
	}
	return false
}

func writeIndex(out string, officials []tmpl) error {
	var b strings.Builder
	b.WriteString("# Miabi official template catalog (registry index).\n")
	b.WriteString("# Digests are sha256 of each template.yaml; regenerate via `go generate ./templates`.\n")
	b.WriteString("apiVersion: miabi.io/v1\n")
	b.WriteString("kind: Index\n")
	b.WriteString("name: miabi-official\n")
	b.WriteString("templates:\n")
	for _, t := range officials {
		fmt.Fprintf(&b, "  - name: %s\n", t.Name)
		b.WriteString("    versions:\n")
		for _, v := range t.Versions {
			fmt.Fprintf(&b, "      - version: %s\n", v.Version)
			fmt.Fprintf(&b, "        path: %s/%s/template.yaml\n", t.Name, v.Version)
			fmt.Fprintf(&b, "        digest: %s\n", v.Digest)
		}
	}
	return os.WriteFile(filepath.Join(out, "index.yaml"), []byte(b.String()), 0o644)
}

var embedRe = regexp.MustCompile(`(?m)^//go:embed .*$`)

// rewriteEmbed replaces the //go:embed directive in embed.go with the current
// official slug list, so the embedded set stays in lockstep with the catalog.
func rewriteEmbed(path string, slugs []string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !embedRe.Match(src) {
		return fmt.Errorf("%s: no //go:embed directive found", path)
	}
	directive := "//go:embed index.yaml " + strings.Join(slugs, " ")
	return os.WriteFile(path, embedRe.ReplaceAll(src, []byte(directive)), 0o644)
}

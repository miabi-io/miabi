// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flatManifest is an install that predates the kinded document: same content, older shape.
func flatManifest() *Manifest {
	m := testManifest()
	m.kinded = false
	return m
}

func writeManifest(t *testing.T, m *Manifest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "miabi.yaml")
	if err := Save(path, m); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Both shapes convert into the same internal type, so nothing downstream of Load learns which one
// the host happens to have.
func TestLoadReadsBothShapes(t *testing.T) {
	for name, m := range map[string]*Manifest{"flat": flatManifest(), "kinded": testManifest()} {
		t.Run(name, func(t *testing.T) {
			path := writeManifest(t, m)
			got, err := Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got.Domain != m.Domain || got.Secrets.DBPassword != m.Secrets.DBPassword {
				t.Errorf("load lost content: domain=%q dbPassword set=%v", got.Domain, got.Secrets.DBPassword != "")
			}
			if got.kinded != m.kinded {
				t.Errorf("kinded = %v, want %v — Save writes back in the shape Load found", got.kinded, m.kinded)
			}
		})
	}
}

// Converting is an explicit act. A flat file rewritten by `setup` must come back flat, or an
// operator's older CLI stops reading the install without anyone saying so.
func TestSaveKeepsTheShapeItRead(t *testing.T) {
	flat := writeManifest(t, flatManifest())
	if strings.Contains(readFile(t, flat), "apiVersion:") {
		t.Error("a flat manifest was rewritten as a kinded document")
	}

	kinded := writeManifest(t, testManifest())
	if !strings.Contains(readFile(t, kinded), "apiVersion: "+APIVersion) {
		t.Error("a kinded manifest lost its apiVersion on save")
	}
}

func TestDefaultsProduceTheKindedDocument(t *testing.T) {
	path := writeManifest(t, Defaults("miabi/miabi:1.0.0"))
	if !strings.Contains(readFile(t, path), "kind: "+KindControlPlane) {
		t.Error("a fresh install did not land on the kinded document")
	}
}

// The copy is the way back: a converted file cannot be read by an older CLI, and it is the only
// record of the install's secrets.
func TestConversionBacksUpTheOriginal(t *testing.T) {
	path := writeManifest(t, flatManifest())
	original := readFile(t, path)

	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := ConvertFile(path, m)
	if err != nil {
		t.Fatal(err)
	}
	if !converted {
		t.Fatal("a flat manifest reported nothing to convert")
	}

	bak := path + BackupSuffix
	fi, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("stat %s: %v", bak, err)
	}
	if perm := fi.Mode().Perm(); perm != manifestMode {
		t.Errorf("%s mode = %04o, want %04o — the copy holds every secret the original did", bak, perm, manifestMode)
	}
	if readFile(t, bak) != original {
		t.Error("the copy is not the file it replaced")
	}
	if back, lerr := Load(bak); lerr != nil || back.kinded {
		t.Errorf("the copy must still load as the flat shape (err=%v)", lerr)
	}

	// Conversion writes only the copy; the new shape lands when the caller saves.
	if err := Save(path, m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), "apiVersion: "+APIVersion) {
		t.Error("the converted manifest did not save as a document")
	}
}

func TestConvertingAnAlreadyKindedManifestIsANoOp(t *testing.T) {
	path := writeManifest(t, testManifest())
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := ConvertFile(path, m)
	if err != nil {
		t.Fatal(err)
	}
	if converted {
		t.Error("reported a conversion for a manifest that was already kinded")
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) {
		t.Error("wrote a backup with nothing to convert")
	}
}

// The asymmetry is deliberate: files already on thousands of hosts keep loading, and get the check
// the moment they are converted.
func TestUnknownFieldIsRefusedInTheKindedDocumentButNotTheFlatOne(t *testing.T) {
	dir := t.TempDir()

	flat := filepath.Join(dir, "flat.yaml")
	if err := os.WriteFile(flat, []byte("version: 1\ndomain: x.example.com\nbogus: 1\n"), manifestMode); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(flat); err != nil {
		t.Errorf("a flat manifest with an unknown key stopped loading: %v", err)
	}

	kinded := filepath.Join(dir, "kinded.yaml")
	doc := "apiVersion: " + APIVersion + "\nkind: " + KindControlPlane + "\nspec:\n  domain: x.example.com\n  bogus: 1\n"
	if err := os.WriteFile(kinded, []byte(doc), manifestMode); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(kinded); err == nil {
		t.Error("the kinded document accepted an unknown field")
	}
}

// Minting one here would switch config encryption on for a host whose imported gateways will never
// receive the key, and their routes would fail silently.
func TestConversionNeverMintsAGatewayKey(t *testing.T) {
	m := flatManifest()
	delete(m.Gateway.Env, gomaConfigEncryptionKey)
	path := writeManifest(t, m)

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertFile(path, loaded); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	doc, perr := ParseDocument([]byte(readFile(t, path)))
	if perr != nil {
		t.Fatal(perr)
	}
	if got := doc.Spec.Secrets.GomaConfigEncryptionKey; got != "" {
		t.Errorf("conversion invented a gateway config encryption key (%q)", got)
	}
}

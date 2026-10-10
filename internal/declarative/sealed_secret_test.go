// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/pkg/sealed"
)

func sealedFor(t *testing.T, name string) string {
	t.Helper()
	_, rcpt, err := sealed.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	v, err := sealed.Seal(rcpt, 1, name, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sealedDoc(name, value string) string {
	return "apiVersion: miabi.io/v1\nkind: SealedSecret\nmetadata: { name: " + name + " }\nspec: { value: \"" + value + "\" }"
}

func TestSealedSecretRequiresASealedValue(t *testing.T) {
	for name, src := range map[string]string{
		"missing":       "apiVersion: miabi.io/v1\nkind: SealedSecret\nmetadata: { name: tok }\nspec: {}",
		"plaintext":     sealedDoc("tok", "ghp_plain_token"),
		"unknown field": "apiVersion: miabi.io/v1\nkind: SealedSecret\nmetadata: { name: tok }\nspec: { sealedValue: x }",
	} {
		if _, err := d.Parse([]byte(src)); err == nil {
			t.Errorf("%s: want a parse error", name)
		}
	}
	if _, err := d.Parse([]byte(sealedDoc("tok", sealedFor(t, "tok")))); err != nil {
		t.Fatalf("a sealed value must parse: %v", err)
	}
}

func TestSecretAndSealedSecretCannotShareAName(t *testing.T) {
	src := sealedDoc("tok", sealedFor(t, "tok")) + "\n---\napiVersion: miabi.io/v1\nkind: Secret\nmetadata: { name: tok }\nspec: { generate: true }"
	if _, err := d.Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("want a conflict error, got %v", err)
	}
}

func desiredSealed(t *testing.T, name string) (*d.ResourceSet, string) {
	t.Helper()
	sv := sealedFor(t, name)
	set, err := d.Parse([]byte(sealedDoc(name, sv)))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := set.Get("SealedSecret/" + name)
	res.SealedSecret.SealedFP = sealed.Fingerprint(sv)
	set.Add(res)
	return set, sv
}

func actionFor(plan *d.Plan, kind d.Kind, name string) d.Action {
	for _, ch := range plan.Changes {
		if ch.Kind == kind && ch.Name == name {
			return ch.Action
		}
	}
	return d.ActionNoop
}

func TestSealedSecretPlansAnUpdateOnlyWhenTheFingerprintMoves(t *testing.T) {
	desired, sv := desiredSealed(t, "tok")
	live := func(fp string) *d.ResourceSet {
		a := d.NewResourceSet()
		a.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindSealedSecret, Metadata: d.Meta{Name: "tok"}, SealedSecret: &d.SealedSecretSpec{SealedFP: fp}})
		return a
	}
	if got := actionFor(d.BuildPlan(desired, live(sealed.Fingerprint(sv)), d.PlanOptions{}), d.KindSealedSecret, "tok"); got != d.ActionNoop {
		t.Errorf("same sealed value planned %s", got)
	}
	if got := actionFor(d.BuildPlan(desired, live("0000"), d.PlanOptions{}), d.KindSealedSecret, "tok"); got != d.ActionUpdate {
		t.Errorf("edited sealed value planned %s", got)
	}
	if got := actionFor(d.BuildPlan(desired, d.NewResourceSet(), d.PlanOptions{}), d.KindSealedSecret, "tok"); got != d.ActionCreate {
		t.Errorf("new sealed secret planned %s", got)
	}
}

// Moving a name from Secret to SealedSecret in git adopts the vault entry; it must not plan a delete of
// the old kind next to a create the vault's unique name would refuse.
func TestSwitchingKindAdoptsTheVaultEntry(t *testing.T) {
	desired, _ := desiredSealed(t, "tok")
	actual := d.NewResourceSet()
	actual.Add(d.Resource{
		APIVersion: d.APIVersion, Kind: d.KindSecret, Secret: &d.SecretSpec{},
		Metadata: d.Meta{Name: "tok", Labels: map[string]string{d.LabelManagedBy: "gitops"}},
	})
	plan := d.BuildPlan(desired, actual, d.PlanOptions{Prune: true, PruneManagedBy: "gitops"})
	if got := actionFor(plan, d.KindSealedSecret, "tok"); got != d.ActionUpdate {
		t.Errorf("SealedSecret planned %s, want update", got)
	}
	if got := actionFor(plan, d.KindSecret, "tok"); got != d.ActionNoop {
		t.Errorf("the old Secret must not be pruned, planned %s", got)
	}

	back, err := d.Parse([]byte("apiVersion: miabi.io/v1\nkind: Secret\nmetadata: { name: tok }\nspec: { value: x }"))
	if err != nil {
		t.Fatal(err)
	}
	live := d.NewResourceSet()
	live.Add(d.Resource{APIVersion: d.APIVersion, Kind: d.KindSealedSecret, Metadata: d.Meta{Name: "tok"}, SealedSecret: &d.SealedSecretSpec{SealedFP: "ab"}})
	if plan := d.BuildPlan(back, live, d.PlanOptions{Prune: true}); len(plan.Changes) != 0 {
		t.Errorf("going back to a plain Secret must leave the entry alone, got %+v", plan.Changes)
	}
}

func TestAppReferenceDrawsAnEdgeToASealedSecret(t *testing.T) {
	src := sealedDoc("tok", sealedFor(t, "tok")) + `
---
apiVersion: miabi.io/v1
kind: Application
metadata: { name: api }
spec:
  image: nginx
  env:
    TOKEN: "{{ .secrets.tok }}"`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range d.Edges(set) {
		if e.To == "SealedSecret/tok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no edge from the app to its sealed secret: %+v", d.Edges(set))
	}
}

func TestPlainSecretsListsOnlyPlaintextValues(t *testing.T) {
	src := sealedDoc("sealed", sealedFor(t, "sealed")) + `
---
apiVersion: miabi.io/v1
kind: Secret
metadata: { name: zeta }
spec: { value: hunter2 }
---
apiVersion: miabi.io/v1
kind: Secret
metadata: { name: generated }
spec: { generate: true }
---
apiVersion: miabi.io/v1
kind: Secret
metadata: { name: alpha }
spec: { value: x }`
	set, err := d.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.PlainSecrets(set), ","); got != "alpha,zeta" {
		t.Fatalf("PlainSecrets = %q, want alpha,zeta", got)
	}
}

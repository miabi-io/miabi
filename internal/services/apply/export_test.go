package apply

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

func TestGitAppProjectsItsSourceNotItsBuiltImage(t *testing.T) {
	app := &models.Application{
		Name: "web", SourceType: models.AppSourceGit,
		Image: "registry.example.com/ws1/web", Tag: "sha-9f2c1ab",
		GitRepo: "https://github.com/acme/web", GitRef: "main",
		BuildMethod: models.BuildBuildpack, Builder: "paketobuildpacks/builder-jammy-base",
	}
	r := appResource(app, nil, nil, nil, nil, nil)
	spec := r.Application
	if spec.Image != "" || spec.Tag != "" {
		t.Errorf("the built image leaked into the manifest: %q:%q", spec.Image, spec.Tag)
	}
	if spec.Source == nil || spec.Source.Git != "https://github.com/acme/web" || spec.Source.Ref != "main" {
		t.Fatalf("source = %+v", spec.Source)
	}
	if spec.Source.BuildMethod != "buildpack" || spec.Source.Builder == "" {
		t.Errorf("build config lost: %+v", spec.Source)
	}
	b, err := declarative.Marshal(setOf(r))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "source:") || strings.Contains(string(b), "sha-9f2c1ab") {
		t.Errorf("rendered bundle is wrong:\n%s", b)
	}
}

func TestImageAppKeepsItsImage(t *testing.T) {
	app := &models.Application{Name: "web", SourceType: models.AppSourceImage, Image: "nginx", Tag: "1.27"}
	spec := appResource(app, nil, nil, nil, nil, nil).Application
	if spec.Source != nil {
		t.Errorf("an image app grew a source block: %+v", spec.Source)
	}
	if spec.Image != "nginx" || spec.Tag != "1.27" {
		t.Errorf("image = %q:%q", spec.Image, spec.Tag)
	}
}

func setOf(r declarative.Resource) *declarative.ResourceSet {
	s := declarative.NewResourceSet()
	s.Add(r)
	return s
}

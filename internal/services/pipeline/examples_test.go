package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExampleSpecsParse(t *testing.T) {
	matches, err := filepath.Glob("../../../examples/pipeline/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Skip("examples not present in this tree")
	}
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := ParseSpec(data)
			if err != nil {
				t.Fatalf("%s does not parse: %v", filepath.Base(path), err)
			}
			if len(spec.Steps) == 0 {
				t.Errorf("%s declares no steps", filepath.Base(path))
			}
		})
	}
}

func TestRepositoryExampleIsValidForARepositoryBinding(t *testing.T) {
	data, err := os.ReadFile("../../../examples/pipeline/pipeline-repository.yaml")
	if err != nil {
		t.Skip("example not present in this tree")
	}
	spec, err := ParseSpec(data)
	if err != nil {
		t.Fatalf("example does not parse: %v", err)
	}

	repoID := uint(1)
	if err := validateStepsAgainstBinding(spec, nil, &repoID); err != nil {
		t.Errorf("the repository example is rejected for a repository-bound pipeline: %v", err)
	}
	if err := validateStepsAgainstBinding(spec, nil, nil); !errors.Is(err, ErrBuildNeedsSource) {
		t.Errorf("unbound: got %v, want ErrBuildNeedsSource", err)
	}

	for _, st := range spec.Steps {
		if st.Uses == UsesDeploy {
			t.Errorf("step %q: the repository example must not declare a deploy step", st.Name)
		}
	}
}

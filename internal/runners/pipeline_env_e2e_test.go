package runners

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// The whole path: what an author writes, through the run records, to the wire.
func TestPipelineEnvReachesTheRunner(t *testing.T) {
	spec, err := pipeline.ParseSpec([]byte(`
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: ci }
env:
  NODE_ENV: production
  NPM_TOKEN: ${{ secrets.NPM_TOKEN }}
steps:
  - name: test
    image: node:22
    run: npm ci && npm test
    env:
      CI: "true"
`))
	if err != nil {
		t.Fatal(err)
	}
	// What CreateRun persists.
	steps := make([]models.PipelineStepRun, 0, len(spec.Steps))
	for i, st := range spec.Steps {
		steps = append(steps, models.PipelineStepRun{Ordinal: i, Name: st.Name, Image: st.Image, Run: st.Run, Env: st.Env})
	}
	job, _ := BuildJobSpec(JobInputs{
		Run: &models.PipelineRun{ID: 9, WorkspaceID: 3, Env: spec.Env}, Pipeline: "ci",
		Env: spec.Env, Steps: steps,
	})
	// What the dispatcher does next.
	vault := &fakeVault{values: map[string]string{"NPM_TOKEN": "npm_live_TOKEN"}}
	mask, err := resolveJobSecrets(vault, 3, &job)
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(job.Env, " ")
	if !strings.Contains(joined, "NODE_ENV=production") || !strings.Contains(joined, "NPM_TOKEN=npm_live_TOKEN") {
		t.Errorf("job env = %v", job.Env)
	}
	if strings.Contains(joined, "secrets.NPM_TOKEN") {
		t.Error("the reference reached the runner instead of the value")
	}
	if got := strings.Join(job.Steps[0].Env, " "); !strings.Contains(got, "CI=true") {
		t.Errorf("step env = %v", job.Steps[0].Env)
	}
	if len(mask) != 1 || mask[0] != "npm_live_TOKEN" {
		t.Errorf("mask = %v — the resolved value must be redacted from live logs", mask)
	}
	t.Logf("job env: %v", job.Env)
	t.Logf("step env: %v  mask: %v", job.Steps[0].Env, mask)
}

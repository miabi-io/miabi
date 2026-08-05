// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/pipeline"
	"github.com/miabi-io/miabi/internal/worker"
)

// PipelineHandler exposes pipeline definition CRUD, run triggering, run history,
// and live run-log streaming.
type PipelineHandler struct {
	svc   *pipeline.Service
	bus   *eventbus.Bus
	audit *audit.Logger
	logs  *logstore.Store
}

func NewPipelineHandler(svc *pipeline.Service, bus *eventbus.Bus, auditLog *audit.Logger) *PipelineHandler {
	return &PipelineHandler{svc: svc, bus: bus, audit: auditLog}
}

// SetLogStore wires the shared execution-log store so a finished run's step
// history is replayed from the store and full step logs can be downloaded. nil
// keeps DB-tail-only behavior.
func (h *PipelineHandler) SetLogStore(s *logstore.Store) { h.logs = s }

type CreatePipelineRequest struct {
	Body struct {
		Name          string `json:"name" required:"true"` // desired unique slug handle
		DisplayName   string `json:"display_name"`         // free-text label (defaults to name)
		ApplicationID *uint  `json:"application_id"`
		Spec          string `json:"spec" required:"true"`
		Enabled       bool   `json:"enabled"`
	} `json:"body"`
}

// UpdatePipelineRequest is a partial update: every field is optional and an
// omitted field is left unchanged. Enabled is a pointer (nil = unchanged), and
// application_id is tri-state — omitted keeps the binding, null unbinds, a number
// rebinds — so a spec-only PATCH can't silently disable or unbind the pipeline.
type UpdatePipelineRequest struct {
	Body struct {
		Name          string          `json:"name"`
		ApplicationID json.RawMessage `json:"application_id"` // omitted=keep, null=unbind, N=bind
		Spec          string          `json:"spec"`
		Enabled       *bool           `json:"enabled"`
	} `json:"body"`
}

type TriggerPipelineRequest struct {
	Body struct {
		// Branch selects the ref to build. Blank uses the pipeline's tracked ref
		// (a repo-owned pipeline's source_ref); it also selects the ref a repo-owned
		// pipeline re-reads its spec from.
		Branch        string `json:"branch"`
		Commit        string `json:"commit"`
		CommitMessage string `json:"commit_message"`
	} `json:"body"`
}

func (h *PipelineHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	out, total, err := h.svc.ListPaged(middlewares.WorkspaceID(c), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list pipelines", err)
	}
	return paginated(c, out, total, page, size)
}

func (h *PipelineHandler) Create(c *okapi.Context, req *CreatePipelineRequest) error {
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Create(wsID, pipeline.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, ApplicationID: req.Body.ApplicationID,
		Spec: req.Body.Spec, Enabled: &req.Body.Enabled,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "pipeline.create", p.ID)
	return created(c, p)
}

func (h *PipelineHandler) Get(c *okapi.Context) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	p, err := h.svc.GetWithLastRun(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("pipeline not found")
	}
	return ok(c, p)
}

func (h *PipelineHandler) Update(c *okapi.Context, req *UpdatePipelineRequest) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	wsID := middlewares.WorkspaceID(c)
	in := pipeline.Input{Name: req.Body.Name, Spec: req.Body.Spec, Enabled: req.Body.Enabled}
	// Tri-state application_id: present ⇒ write the binding (null clears it),
	// absent ⇒ leave it unchanged.
	present, appID, perr := optionalUintRef(req.Body.ApplicationID)
	if perr != nil {
		return c.AbortBadRequest("invalid application_id")
	}
	in.SetApplicationID = present
	in.ApplicationID = appID
	p, err := h.svc.Update(wsID, id, in)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "pipeline.update", p.ID)
	return ok(c, p)
}

func (h *PipelineHandler) Delete(c *okapi.Context) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "pipeline.delete", id)
	return message(c, "pipeline deleted")
}

// Trigger starts a manual run.
func (h *PipelineHandler) Trigger(c *okapi.Context, req *TriggerPipelineRequest) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	run, err := h.svc.Trigger(wsID, id, pipeline.TriggerInput{
		Trigger: "manual", Branch: req.Body.Branch, Commit: req.Body.Commit,
		CommitMessage: req.Body.CommitMessage, UserID: &actor,
	})
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "pipeline.trigger", id)
	return created(c, run)
}

// WebhookInfo reveals the push-webhook path and secret so a user can configure
// their Git provider. Role-gated (Developer+) since the secret is sensitive.
func (h *PipelineHandler) WebhookInfo(c *okapi.Context) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	wsID := middlewares.WorkspaceID(c)
	p, err := h.svc.Get(wsID, id)
	if err != nil {
		return c.AbortNotFound("pipeline not found")
	}
	return ok(c, map[string]string{
		"path":             fmt.Sprintf("/api/v1/workspaces/%d/pipelines/%d/webhook", wsID, p.ID),
		"secret":           p.WebhookSecret,
		"signature_header": "X-Hub-Signature-256", // GitHub HMAC; GitLab uses X-Gitlab-Token = secret
	})
}

// Webhook fires a pipeline from a provider push. It is unauthenticated; the
// request is verified against the pipeline's webhook secret, and the run starts
// only when the pushed branch matches the pipeline's `on.push` trigger.
func (h *PipelineHandler) Webhook(c *okapi.Context) error {
	wsID, err := uintParam(c, "workspace")
	if err != nil {
		return c.AbortBadRequest("invalid workspace id")
	}
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	body, _ := io.ReadAll(c.Request().Body)
	sig := c.Header("X-Hub-Signature-256")
	if sig == "" {
		sig = c.Header("X-Gitlab-Token")
	}
	run, fired, err := h.svc.TriggerPush(wsID, id, sig, body)
	switch {
	case errors.Is(err, pipeline.ErrUnauthorized):
		return c.AbortUnauthorized("invalid webhook signature")
	case errors.Is(err, pipeline.ErrNotFound):
		return c.AbortNotFound("pipeline not found")
	case errors.Is(err, pipeline.ErrDisabled):
		return c.AbortBadRequest("pipeline is disabled")
	case err != nil:
		return c.AbortInternalServerError("pipeline webhook failed", err)
	}
	if !fired {
		return message(c, "ignored: push does not match this pipeline's trigger")
	}
	h.record(c, wsID, "pipeline.trigger", id)
	return created(c, run)
}

// ListRuns returns a page of a pipeline's runs.
// StreamWorkspaceRuns pushes run transitions so the lists need no polling.
func (h *PipelineHandler) StreamWorkspaceRuns(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	ch, unsubscribe := h.bus.Subscribe(worker.PipelineWorkspaceTopic(wsID))
	defer unsubscribe()

	ctx := c.Request().Context()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := c.SSESendJSON(ev); err != nil {
				return nil
			}
		case <-ping.C:
			if err := c.SSESendJSON(eventbus.Event{Type: "ping"}); err != nil {
				return nil
			}
		}
	}
}

func (h *PipelineHandler) ListRuns(c *okapi.Context) error {
	id, err := resolveID(c.Param("pipelineID"), h.svc.IDByUID)
	if err != nil {
		return c.AbortBadRequest("invalid pipeline id")
	}
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	runs, total, err := h.svc.ListRunsPaged(middlewares.WorkspaceID(c), id, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list runs", err)
	}
	return paginated(c, runs, total, page, size)
}

// GetRun returns a single run with its steps.
func (h *PipelineHandler) GetRun(c *okapi.Context) error {
	id, err := uintParam(c, "runID")
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	run, err := h.svc.GetRun(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("pipeline run not found")
	}
	return ok(c, run)
}

// RunLogs streams a run's step logs over SSE: for a finished run it replays each
// step's full history from the store (falling back to the DB tail), then the
// terminal status; for a running one it streams live step logs from the bus.
func (h *PipelineHandler) RunLogs(c *okapi.Context) error {
	id, err := uintParam(c, "runID")
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	run, err := h.svc.GetRun(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("pipeline run not found")
	}

	// Subscribe before replaying to avoid missing events in between.
	ch, unsubscribe := h.bus.Subscribe(worker.PipelineTopic(id))
	defer unsubscribe()

	if run.Status.IsTerminal() {
		for _, step := range run.Steps {
			for _, line := range replayLogHistory(h.logs, step.LogRef, step.Logs) {
				_ = c.SSESendJSON(eventbus.Event{Type: "log", Data: line})
			}
		}
		_ = c.SSESendJSON(eventbus.Event{Type: "status", Data: string(run.Status)})
		return nil
	}

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			_ = c.SSESendJSON(e)
		}
	}
}

// StepLogsDownload streams one pipeline step's full log as a file download.
func (h *PipelineHandler) StepLogsDownload(c *okapi.Context) error {
	id, err := uintParam(c, "runID")
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil {
		return c.AbortBadRequest("invalid step ordinal")
	}
	run, err := h.svc.GetRun(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("pipeline run not found")
	}
	for i := range run.Steps {
		step := run.Steps[i]
		if step.Ordinal == ordinal {
			name := fmt.Sprintf("run-%d-step-%d.log", run.ID, step.Ordinal)
			return streamLogDownload(c, h.logs, step.LogRef, step.Logs, name)
		}
	}
	return c.AbortNotFound("step not found")
}

// StepLogHistory is one step's stored output for the non-streaming run-logs view.
type StepLogHistory struct {
	Ordinal   int      `json:"ordinal"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// PipelineRunLogHistory is a (usually finished) run's full per-step logs.
type PipelineRunLogHistory struct {
	Status string           `json:"status"`
	Steps  []StepLogHistory `json:"steps"`
}

// RunLogsHistory returns a pipeline run's full per-step logs (from the store, else
// each step's bounded DB tail) as JSON — the load-once counterpart to the SSE
// stream, for viewing a finished run.
func (h *PipelineHandler) RunLogsHistory(c *okapi.Context) error {
	id, err := uintParam(c, "runID")
	if err != nil {
		return c.AbortBadRequest("invalid run id")
	}
	run, err := h.svc.GetRun(middlewares.WorkspaceID(c), id)
	if err != nil {
		return c.AbortNotFound("pipeline run not found")
	}
	steps := make([]StepLogHistory, 0, len(run.Steps))
	for i := range run.Steps {
		s := &run.Steps[i]
		steps = append(steps, StepLogHistory{
			Ordinal:   s.Ordinal,
			Name:      s.Name,
			Status:    string(s.Status),
			Lines:     replayLogHistory(h.logs, s.LogRef, s.Logs),
			Truncated: s.LogTruncated,
		})
	}
	return ok(c, PipelineRunLogHistory{Status: string(run.Status), Steps: steps})
}

func (h *PipelineHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action,
		TargetType: "pipeline", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}

func (h *PipelineHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, pipeline.ErrNotFound), errors.Is(err, pipeline.ErrRunNotFound):
		return c.AbortNotFound("pipeline not found")
	case errors.Is(err, pipeline.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, pipeline.ErrRepoOwned):
		// 409: the request is well-formed, but this pipeline's spec lives in git.
		return c.AbortWithError(409, err)
	case errors.Is(err, pipeline.ErrNoPipelineInRepo), errors.Is(err, pipeline.ErrNotGitApp),
		errors.Is(err, pipeline.ErrAdoptionUnavailable):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, pipeline.ErrInvalidSpec), errors.Is(err, pipeline.ErrNameRequired), errors.Is(err, pipeline.ErrDisabled):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("pipeline operation failed", err)
	}
}

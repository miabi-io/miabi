// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package worker holds the asynq producer, task types, and handlers for background work.
package worker

import (
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"
	"github.com/miabi-io/miabi/internal/services/database"
)

// Queue names, ordered by priority weight in the worker config.
const (
	QueueDeploy  = "deploy"
	QueueDefault = "default"
	QueueLow     = "low"
	// Agent tunnels live only in the control-plane server, so only its embedded worker consumes this queue.
	QueueNode = "node"
	// A standalone worker builds only a subset of the control-plane services, so it doesn't consume this queue.
	QueueControl = "control"
)

// Task types.
const (
	TypeDeploy         = "deploy:run"
	TypeProvisionDB    = "database:provision"
	TypeUpgradeDB      = "database:upgrade"
	TypeCanaryStep     = "deploy:canary-step"
	TypeNotifyFanout   = "notify:fanout"
	TypeWebhookDeliver = "webhook:deliver"
	TypeNotifyChannel  = "notify:channel-send"
	TypeRunJob         = "job:run"
	TypeVolumeBackup   = "volume:backup"
	TypeRunPipeline    = "pipeline:run"
	TypePlatformBackup = "platform:backup"
	TypeWSBundle       = "workspace:bundle"
	TypeMigration      = "location:migration"
)

// DeployPayload identifies the deployment to process.
type DeployPayload struct {
	DeploymentID uint `json:"deployment_id"`
}

// CanaryStepPayload identifies the canary deployment whose rollout to advance.
type CanaryStepPayload struct {
	DeploymentID uint `json:"deployment_id"`
}

// ProvisionDBPayload identifies the database to provision.
type ProvisionDBPayload struct {
	DatabaseID uint `json:"database_id"`
	// The previous limits are what a failed resize restores.
	Resize          bool   `json:"resize,omitempty"`
	PrevMemoryBytes int64  `json:"prev_memory_bytes,omitempty"`
	PrevNanoCPUs    int64  `json:"prev_nano_cpus,omitempty"`
	PrevSize        string `json:"prev_size,omitempty"`
	Redeploy        bool   `json:"redeploy,omitempty"`
}

// UpgradeDBPayload describes a queued database version upgrade.
type UpgradeDBPayload struct {
	DatabaseID uint   `json:"database_id"`
	Target     string `json:"target"`    // target engine version
	Path       string `json:"path"`      // in-place | dump-restore
	StopApps   bool   `json:"stop_apps"` // quiesce apps using the database during the upgrade
}

type NotifyFanoutPayload struct {
	AppEventID uint `json:"app_event_id"`
}

// EventID == 0 signals a synthetic test delivery.
type WebhookDeliverPayload struct {
	WebhookID uint `json:"webhook_id"`
	EventID   uint `json:"event_id"`
}

// NotifyChannelPayload identifies a single notification-channel delivery.
type NotifyChannelPayload struct {
	ChannelID uint `json:"channel_id"`
	EventID   uint `json:"event_id"`
}

// RunJobPayload identifies the one-off Job to run.
type RunJobPayload struct {
	JobID uint `json:"job_id"`
}

// VolumeBackupPayload identifies the volume backup record to execute.
type VolumeBackupPayload struct {
	VolumeBackupID uint `json:"volume_backup_id"`
}

// RunPipelinePayload identifies the pipeline run to execute.
type RunPipelinePayload struct {
	PipelineRunID uint `json:"pipeline_run_id"`
}

// Only the row id travels so no secret is ever written to the queue.
type WorkspaceBundlePayload struct {
	WorkspaceBundleID uint `json:"workspace_bundle_id"`
}

// The run reads everything from its row, so a resumed run and a first run are the same task.
type LocationMigrationPayload struct {
	MigrationID uint `json:"migration_id"`
}

// PlatformBackupPayload identifies the platform backup record to execute.
type PlatformBackupPayload struct {
	PlatformBackupID uint `json:"platform_backup_id"`
}

// Producer enqueues tasks into Redis via asynq.
type Producer struct {
	client     *asynq.Client
	maxRetries int
	localID    uint
}

// NewProducer creates a task producer backed by Redis on the given DB index.
func NewProducer(redisAddr, redisPassword string, redisDB, maxRetries int) *Producer {
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})
	return &Producer{client: client, maxRetries: maxRetries}
}

func (p *Producer) SetLocalID(id uint) { p.localID = id }

// Remote nodes go to QueueNode so only the worker holding the agent tunnels runs them.
func (p *Producer) nodeQueue(serverID uint, def string) string {
	if serverID != 0 && serverID != p.localID {
		return QueueNode
	}
	return def
}

func (p *Producer) EnqueueDeploy(deploymentID, serverID uint) error {
	payload, err := json.Marshal(DeployPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeDeploy, payload, asynq.Queue(p.nodeQueue(serverID, QueueDeploy)), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// A git-source deploy must build on a runner, so a standalone worker hands it to the tunnel-holding worker.
func (p *Producer) EnqueueDeployToBuilder(deploymentID uint) error {
	payload, err := json.Marshal(DeployPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeDeploy, payload, asynq.Queue(QueueNode), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueueDeployIn(deploymentID, serverID uint, delay time.Duration) error {
	payload, err := json.Marshal(DeployPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeDeploy, payload, asynq.Queue(p.nodeQueue(serverID, QueueDeploy)), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task, asynq.ProcessIn(delay))
	return err
}

// EnqueueCanaryStep schedules the next canary progression step after delaySeconds.
func (p *Producer) EnqueueCanaryStep(deploymentID uint, delaySeconds int, serverID uint) error {
	payload, err := json.Marshal(CanaryStepPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeCanaryStep, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(3))
	_, err = p.client.Enqueue(task, asynq.ProcessIn(time.Duration(delaySeconds)*time.Second))
	return err
}

// EnqueueProvisionDB schedules a database to be provisioned by the worker.
func (p *Producer) EnqueueProvisionDB(databaseID, serverID uint) error {
	payload, err := json.Marshal(ProvisionDBPayload{DatabaseID: databaseID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeProvisionDB, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// MaxRetry is 0: the job restores the previous limits itself on failure.
func (p *Producer) EnqueueResizeDB(databaseID, serverID uint, previous database.Resources) error {
	payload, err := json.Marshal(ProvisionDBPayload{
		DatabaseID: databaseID, Resize: true, PrevMemoryBytes: previous.MemoryBytes, PrevNanoCPUs: previous.NanoCPUs,
		PrevSize: previous.Size,
	})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeProvisionDB, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// MaxRetry is 0: the job marks the instance failed itself, and the user redeploys again.
func (p *Producer) EnqueueRedeployDB(databaseID, serverID uint) error {
	payload, err := json.Marshal(ProvisionDBPayload{DatabaseID: databaseID, Redeploy: true})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeProvisionDB, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// MaxRetry is 0: a half-applied upgrade must not be blindly re-run.
func (p *Producer) EnqueueUpgradeDB(databaseID, serverID uint, target, path string, stopApps bool) error {
	payload, err := json.Marshal(UpgradeDBPayload{DatabaseID: databaseID, Target: target, Path: path, StopApps: stopApps})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeUpgradeDB, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueueNotifyFanout(appEventID uint) error {
	payload, err := json.Marshal(NotifyFanoutPayload{AppEventID: appEventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeNotifyFanout, payload, asynq.Queue(QueueLow), asynq.MaxRetry(3))
	_, err = p.client.Enqueue(task)
	return err
}

// Each endpoint retries independently with asynq's exponential backoff.
func (p *Producer) EnqueueWebhookDeliver(webhookID, eventID uint) error {
	payload, err := json.Marshal(WebhookDeliverPayload{WebhookID: webhookID, EventID: eventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeWebhookDeliver, payload, asynq.Queue(QueueLow), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueueNotifyChannel(channelID, eventID uint) error {
	payload, err := json.Marshal(NotifyChannelPayload{ChannelID: channelID, EventID: eventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeNotifyChannel, payload, asynq.Queue(QueueLow), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// Jobs are not auto-retried; the user re-runs a failed one explicitly.
func (p *Producer) EnqueueRunJob(jobID, serverID uint) error {
	payload, err := json.Marshal(RunJobPayload{JobID: jobID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunJob, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// Not auto-retried; a failed run is recorded on the backup row.
func (p *Producer) EnqueueVolumeBackup(backupID, serverID uint) error {
	payload, err := json.Marshal(VolumeBackupPayload{VolumeBackupID: backupID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeVolumeBackup, payload, asynq.Queue(p.nodeQueue(serverID, QueueLow)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// Not auto-retried; deploy priority so CI feedback is prompt.
func (p *Producer) EnqueuePipelineRun(runID, serverID uint) error {
	payload, err := json.Marshal(RunPipelinePayload{PipelineRunID: runID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunPipeline, payload, asynq.Queue(p.nodeQueue(serverID, QueueDeploy)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueuePipelineRunIn(runID uint, delay time.Duration) error {
	payload, err := json.Marshal(RunPipelinePayload{PipelineRunID: runID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunPipeline, payload, asynq.Queue(QueueDeploy), asynq.MaxRetry(0), asynq.ProcessIn(delay))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueuePipelineRunToBuilder(runID uint, delay time.Duration) error {
	payload, err := json.Marshal(RunPipelinePayload{PipelineRunID: runID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunPipeline, payload, asynq.Queue(QueueNode), asynq.MaxRetry(0), asynq.ProcessIn(delay))
	_, err = p.client.Enqueue(task)
	return err
}

// Not auto-retried; always runs on the manager node, so it uses the default queue.
func (p *Producer) EnqueuePlatformBackup(backupID uint) error {
	payload, err := json.Marshal(PlatformBackupPayload{PlatformBackupID: backupID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypePlatformBackup, payload, asynq.Queue(QueueLow), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// Never auto-retried: a partial restore is what an operator wants to look at before replaying.
func (p *Producer) EnqueueWorkspaceBundle(bundleID uint) error {
	payload, err := json.Marshal(WorkspaceBundlePayload{WorkspaceBundleID: bundleID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeWSBundle, payload, asynq.Queue(QueueControl), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

func (p *Producer) EnqueueLocationMigration(migrationID uint) error {
	payload, err := json.Marshal(LocationMigrationPayload{MigrationID: migrationID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeMigration, payload, asynq.Queue(QueueControl), asynq.MaxRetry(0), asynq.Timeout(72*time.Hour))
	_, err = p.client.Enqueue(task)
	return err
}

// Close releases the underlying asynq client.
func (p *Producer) Close() error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}

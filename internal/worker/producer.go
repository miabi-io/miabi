// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package worker holds the asynq producer, task types, and handlers for
// background work (deploys, backups, metric scrapes, ...).
package worker

import (
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"
)

// Queue names, ordered by priority weight in the worker config.
const (
	QueueDeploy  = "deploy"
	QueueDefault = "default"
	QueueLow     = "low"
	// QueueNode carries tasks that must run on a remote node (deploy/job/provision
	// against a non-local server). Agent tunnels live only in the control-plane
	// server process, so only its embedded worker consumes this queue; a
	// standalone `miabi worker` does not, and therefore never receives a
	// remote-node task it couldn't reach.
	QueueNode = "node"
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
}

// UpgradeDBPayload describes a queued database version upgrade.
type UpgradeDBPayload struct {
	DatabaseID uint   `json:"database_id"`
	Target     string `json:"target"`    // target engine version
	Path       string `json:"path"`      // in-place | dump-restore
	StopApps   bool   `json:"stop_apps"` // quiesce apps using the database during the upgrade
}

// NotifyFanoutPayload identifies a persisted event to fan out to a workspace's
// webhooks and notification channels.
type NotifyFanoutPayload struct {
	AppEventID uint `json:"app_event_id"`
}

// WebhookDeliverPayload identifies a single webhook delivery. EventID == 0
// signals a synthetic test delivery.
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

// SetLocalID records the local node's server id so node-affine tasks can be
// routed to the right queue (remote nodes -> QueueNode, local -> def).
func (p *Producer) SetLocalID(id uint) { p.localID = id }

// nodeQueue returns QueueNode when serverID is a remote node (so only the
// control-plane server's worker, which holds the agent tunnels, runs it), else
// the given default queue.
func (p *Producer) nodeQueue(serverID uint, def string) string {
	if serverID != 0 && serverID != p.localID {
		return QueueNode
	}
	return def
}

// EnqueueDeploy schedules a deployment to be processed by the worker. serverID
// is the app's node, used to route remote-node deploys to the server's worker.
func (p *Producer) EnqueueDeploy(deploymentID, serverID uint) error {
	payload, err := json.Marshal(DeployPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeDeploy, payload, asynq.Queue(p.nodeQueue(serverID, QueueDeploy)), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueDeployToBuilder routes a deployment to QueueNode, which only the
// control-plane server's embedded worker consumes — the process that holds the
// runner tunnels. A git-source deploy must build on a runner, so a standalone
// worker (which can't reach the tunnels) hands it off here instead of failing.
func (p *Producer) EnqueueDeployToBuilder(deploymentID uint) error {
	payload, err := json.Marshal(DeployPayload{DeploymentID: deploymentID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeDeploy, payload, asynq.Queue(QueueNode), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueDeployIn re-schedules a deployment after a delay — used to defer a
// deploy when another for the same app is already in progress (per-app lock).
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

// EnqueueUpgradeDB schedules a database version upgrade. MaxRetry is 0: a
// half-applied upgrade must not be blindly re-run (the handler records failures
// on the instance instead).
func (p *Producer) EnqueueUpgradeDB(databaseID, serverID uint, target, path string, stopApps bool) error {
	payload, err := json.Marshal(UpgradeDBPayload{DatabaseID: databaseID, Target: target, Path: path, StopApps: stopApps})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeUpgradeDB, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueNotifyFanout schedules fan-out of an event to a workspace's webhooks
// and channels. Cheap and safe to call from request or worker goroutines.
func (p *Producer) EnqueueNotifyFanout(appEventID uint) error {
	payload, err := json.Marshal(NotifyFanoutPayload{AppEventID: appEventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeNotifyFanout, payload, asynq.Queue(QueueLow), asynq.MaxRetry(3))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueWebhookDeliver schedules delivery of an event to one webhook. Each
// endpoint retries independently with asynq's exponential backoff.
func (p *Producer) EnqueueWebhookDeliver(webhookID, eventID uint) error {
	payload, err := json.Marshal(WebhookDeliverPayload{WebhookID: webhookID, EventID: eventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeWebhookDeliver, payload, asynq.Queue(QueueLow), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueNotifyChannel schedules delivery of an event to one notification
// channel.
func (p *Producer) EnqueueNotifyChannel(channelID, eventID uint) error {
	payload, err := json.Marshal(NotifyChannelPayload{ChannelID: channelID, EventID: eventID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeNotifyChannel, payload, asynq.Queue(QueueLow), asynq.MaxRetry(p.maxRetries))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueRunJob schedules a one-off Job to be run by the worker. Jobs are not
// auto-retried — a failed run is recorded; the user re-runs it explicitly.
func (p *Producer) EnqueueRunJob(jobID, serverID uint) error {
	payload, err := json.Marshal(RunJobPayload{JobID: jobID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunJob, payload, asynq.Queue(p.nodeQueue(serverID, QueueDefault)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueVolumeBackup schedules a volume backup to run in the background. Like
// jobs, it is not auto-retried — a failed run is recorded on the backup row and
// the user re-runs it explicitly. serverID routes remote-node backups.
func (p *Producer) EnqueueVolumeBackup(backupID, serverID uint) error {
	payload, err := json.Marshal(VolumeBackupPayload{VolumeBackupID: backupID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeVolumeBackup, payload, asynq.Queue(p.nodeQueue(serverID, QueueLow)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueuePipelineRun schedules a pipeline run on the internal runner. Like jobs
// it is not auto-retried — a failed run is recorded; the user re-runs it. The
// run is deploy-priority so CI feedback is prompt. serverID routes remote-node
// runs.
func (p *Producer) EnqueuePipelineRun(runID, serverID uint) error {
	payload, err := json.Marshal(RunPipelinePayload{PipelineRunID: runID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunPipeline, payload, asynq.Queue(p.nodeQueue(serverID, QueueDeploy)), asynq.MaxRetry(0))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueuePipelineRunIn re-enqueues a pipeline run after delay — used to poll for
// a free runner when a run is waiting for one (no eligible runner right now).
func (p *Producer) EnqueuePipelineRunIn(runID uint, delay time.Duration) error {
	payload, err := json.Marshal(RunPipelinePayload{PipelineRunID: runID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeRunPipeline, payload, asynq.Queue(QueueDeploy), asynq.MaxRetry(0), asynq.ProcessIn(delay))
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueuePlatformBackup schedules a platform (control-plane) backup to run in
// the background. Like volume backups it is not auto-retried — a failed run is
// recorded on the backup row and the admin re-runs it explicitly. It always runs
// on the manager node, so it uses the default (local) queue.
func (p *Producer) EnqueuePlatformBackup(backupID uint) error {
	payload, err := json.Marshal(PlatformBackupPayload{PlatformBackupID: backupID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypePlatformBackup, payload, asynq.Queue(QueueLow), asynq.MaxRetry(0))
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

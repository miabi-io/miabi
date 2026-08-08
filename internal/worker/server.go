// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import "github.com/hibiken/asynq"

// NewServer builds an asynq server with Miabi's queue priorities. embedded must be true only for the
// control-plane server's embedded worker: it is the one holding the agent tunnels (QueueNode) and the
// platform's full service graph (QueueControl). A standalone worker passes false.
func NewServer(redisAddr, redisPassword string, redisDB, concurrency int, embedded bool) *asynq.Server {
	queues := map[string]int{
		QueueDeploy:  6,
		QueueDefault: 3,
		QueueLow:     1,
	}
	if embedded {
		queues[QueueNode] = 6
		queues[QueueControl] = 3
	}
	return asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr, Password: redisPassword, DB: redisDB},
		asynq.Config{
			Concurrency: concurrency,
			Queues:      queues,
		},
	)
}

// NewMux registers task handlers and returns the asynq mux.
func NewMux(deploy *DeployHandler, provision *ProvisionDBHandler, upgrade *UpgradeDBHandler, fanout *FanoutHandler, webhook *WebhookDeliverHandler, channel *ChannelSendHandler, job *JobHandler, volumeBackup *VolumeBackupHandler, pipeline *PipelineHandler, platformBackup *PlatformBackupHandler, wsBundle *WorkspaceBundleHandler) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeDeploy, deploy.ProcessTask)
	mux.HandleFunc(TypeCanaryStep, deploy.ProcessCanaryStep)
	mux.HandleFunc(TypeProvisionDB, provision.ProcessTask)
	mux.HandleFunc(TypeUpgradeDB, upgrade.ProcessTask)
	mux.HandleFunc(TypeNotifyFanout, fanout.ProcessTask)
	mux.HandleFunc(TypeWebhookDeliver, webhook.ProcessTask)
	mux.HandleFunc(TypeNotifyChannel, channel.ProcessTask)
	mux.HandleFunc(TypeRunJob, job.ProcessTask)
	mux.HandleFunc(TypeVolumeBackup, volumeBackup.ProcessTask)
	mux.HandleFunc(TypeRunPipeline, pipeline.ProcessTask)
	mux.HandleFunc(TypePlatformBackup, platformBackup.ProcessTask)
	// Portable workspace bundles run only where the full service graph exists, so
	// a standalone worker passes nil here and leaves TypeWSBundle unregistered —
	// its server does not consume QueueControl either, so nothing is stranded.
	if wsBundle != nil {
		mux.HandleFunc(TypeWSBundle, wsBundle.ProcessTask)
	}
	return mux
}

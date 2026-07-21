// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import "github.com/hibiken/asynq"

// NewServer builds an asynq server with Miabi's queue priorities.
// consumeNodeQueue must be true only for the control-plane server's embedded
// worker (which holds the agent tunnels); a standalone worker passes false so it
// never picks up a remote-node task it cannot reach.
func NewServer(redisAddr, redisPassword string, redisDB, concurrency int, consumeNodeQueue bool) *asynq.Server {
	queues := map[string]int{
		QueueDeploy:  6,
		QueueDefault: 3,
		QueueLow:     1,
	}
	if consumeNodeQueue {
		queues[QueueNode] = 6
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
func NewMux(deploy *DeployHandler, provision *ProvisionDBHandler, upgrade *UpgradeDBHandler, fanout *FanoutHandler, webhook *WebhookDeliverHandler, channel *ChannelSendHandler, job *JobHandler, volumeBackup *VolumeBackupHandler, pipeline *PipelineHandler, platformBackup *PlatformBackupHandler) *asynq.ServeMux {
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
	return mux
}

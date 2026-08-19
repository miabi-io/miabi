import api, { sseUrl } from './client'
import type { ApiResponse, PageableResponse, PipelineDefinition, PipelineRun, PipelineRunLogHistory } from './types'

export interface PipelineInput {
  name: string
  application_id?: number | null
  spec: string
  enabled: boolean
}

export interface TriggerInput {
  commit?: string
  commit_message?: string
  no_cache?: boolean
}

const base = (ws: number) => `/workspaces/${ws}/pipelines`
const runsBase = (ws: number) => `/workspaces/${ws}/pipeline-runs`

export const pipelineApi = {
  list: (ws: number, page = 0, size = 20) =>
    api.get<PageableResponse<PipelineDefinition>>(`${base(ws)}?page=${page}&size=${size}`),
  get: (ws: number, id: number) => api.get<ApiResponse<PipelineDefinition>>(`${base(ws)}/${id}`),
  create: (ws: number, input: PipelineInput) => api.post<ApiResponse<PipelineDefinition>>(base(ws), input),
  update: (ws: number, id: number, input: PipelineInput) =>
    api.patch<ApiResponse<PipelineDefinition>>(`${base(ws)}/${id}`, input),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
  trigger: (ws: number, id: number, input: TriggerInput = {}) =>
    api.post<ApiResponse<PipelineRun>>(`${base(ws)}/${id}/trigger`, input),
  // Re-run an earlier run at the same ref and commit; no_cache rebuilds every layer.
  rerun: (ws: number, runId: number, noCache = false) =>
    api.post<ApiResponse<PipelineRun>>(`${runsBase(ws)}/${runId}/rerun`, { no_cache: noCache }),
  runs: (ws: number, id: number, page = 0, size = 20) =>
    api.get<PageableResponse<PipelineRun>>(`${base(ws)}/${id}/runs?page=${page}&size=${size}`),
  run: (ws: number, runId: number) => api.get<ApiResponse<PipelineRun>>(`${runsBase(ws)}/${runId}`),
  logsUrl: (ws: number, runId: number) => sseUrl(`${runsBase(ws)}/${runId}/logs`),
  // Every run transition in the workspace, so lists never poll.
  runsStreamUrl: (ws: number) => sseUrl(`${base(ws)}/runs/stream`),
  // Full stored per-step logs of a (usually finished) run — load-once, no SSE.
  runLogsHistory: (ws: number, runId: number) =>
    api.get<ApiResponse<PipelineRunLogHistory>>(`${runsBase(ws)}/${runId}/logs/history`),
  webhookInfo: (ws: number, id: number) =>
    api.get<ApiResponse<{ path: string; secret: string; signature_header: string }>>(`${base(ws)}/${id}/webhook-info`),
}

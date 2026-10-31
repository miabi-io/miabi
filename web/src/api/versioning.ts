import api from './client'
import type { ApiResponse, PageableResponse, VersionAllocation, VersionPlan, VersionPolicy, VersionSettings } from './types'

const base = (ws: number) => `/workspaces/${ws}/versioning`

export const versioningApi = {
  list: (ws: number) => api.get<ApiResponse<VersionPolicy[]>>(`${base(ws)}/policies`),
  get: (ws: number, ref: string | number) => api.get<ApiResponse<VersionPolicy>>(`${base(ws)}/policies/${ref}`),
  // The policy covering a pipeline's image repository; 404 when it has none.
  forPipeline: (ws: number, pipelineId: number) =>
    api.get<ApiResponse<VersionPolicy>>(`/workspaces/${ws}/pipelines/${pipelineId}/versioning`),
  create: (ws: number, pipeline: string | number, settings: VersionSettings) =>
    api.post<ApiResponse<VersionPolicy>>(`${base(ws)}/policies`, { pipeline: String(pipeline), settings }),
  update: (ws: number, ref: string | number, settings: VersionSettings) =>
    api.put<ApiResponse<VersionPolicy>>(`${base(ws)}/policies/${ref}`, settings),
  remove: (ws: number, ref: string | number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/policies/${ref}`),
  switchSeries: (ws: number, ref: string | number, series: string) =>
    api.post<ApiResponse<VersionPolicy>>(`${base(ws)}/policies/${ref}/switch-series`, { series }),
  allocations: (ws: number, ref: string | number, page = 0, size = 20) =>
    api.get<PageableResponse<VersionAllocation>>(`${base(ws)}/policies/${ref}/allocations?page=${page}&size=${size}`),
  // Upcoming versions for unsaved settings, on top of an existing policy's history when given.
  preview: (ws: number, body: { settings: VersionSettings; policy?: string; pipeline?: string; branch?: string; count?: number }) =>
    api.post<ApiResponse<VersionPlan[]>>(`${base(ws)}/preview`, body),
}

/** Settings a policy currently holds, in the shape the API takes back. */
export function settingsOf(p: VersionPolicy): VersionSettings {
  return {
    scheme: p.scheme,
    template: p.template,
    series: p.series,
    bump: p.bump,
    start: p.start,
    calver_format: p.calver_format,
    branches: { release: p.release_branches ?? [], others: p.other_branches, series: p.branch_series ?? {} },
    floating_tags: p.floating_tags ?? [],
    bump_from_commits: p.bump_from_commits,
  }
}

export function defaultSettings(): VersionSettings {
  return {
    scheme: 'semver', template: 'v{version}', series: '1.x.x', bump: 'patch', start: '1.0.0',
    branches: { release: [], others: 'prerelease', series: {} }, floating_tags: [], bump_from_commits: false,
  }
}

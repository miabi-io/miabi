import api from './client'
import type { ApiResponse, CapabilityCatalog, GrantedApp } from './types'

export const capabilityApi = {
  // Static and workspace-independent; empty when the platform switch is off.
  catalog: () => api.get<ApiResponse<CapabilityCatalog>>('/capabilities'),
  // Platform-wide inventory of what has actually been granted (admin).
  granted: () => api.get<ApiResponse<GrantedApp[]>>('/system/granted-applications'),
}

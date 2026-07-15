import api from './client'
import type { ApiResponse, AuthStatus, ProvidersResponse, SSODiscovery } from './types'

export const oauthApi = {
  // Public auth feature status (registration / password reset toggles).
  status: () => api.get<ApiResponse<AuthStatus>>('/auth/status'),
  // Buttoned (non-hidden) providers plus whether hidden providers exist (which
  // are reachable only via the "Continue with SSO" email-discovery flow).
  providers: () => api.get<ApiResponse<ProvidersResponse>>('/auth/oauth/providers'),
  // Resolve the SSO provider for an email's domain (hidden-provider discovery).
  discoverSSO: (email: string) => api.post<ApiResponse<SSODiscovery>>('/auth/oauth/discover', { email }),
}

// authorizeUrl returns the absolute URL that begins the SSO redirect flow for a
// provider. The browser navigates here directly (full page load), so it must hit
// the API origin, not the SPA router. Pass intent='login_token' to force a fresh
// IdP login and mint a CLI token instead of a console session.
export function authorizeUrl(slug: string, intent?: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  const q = intent ? `?intent=${encodeURIComponent(intent)}` : ''
  return `${origin}${base}/auth/oauth/${slug}/authorize${q}`
}

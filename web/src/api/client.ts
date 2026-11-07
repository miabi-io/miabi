import axios from 'axios'
import { useAuthStore } from '@/stores/auth'
import { useElevationStore, ELEVATION_CODES, type ElevationCode } from '@/stores/elevation'
import { t } from '@/i18n'

declare module 'axios' {
  interface AxiosRequestConfig {
    _elevationRetry?: boolean
  }
}

const api = axios.create({
  baseURL: import.meta.env.VITE_API_URL || '/api/v1',
  headers: { 'Content-Type': 'application/json' },
  // The JWT lives only in the HttpOnly session cookie, so there is no Authorization header to attach.
  withCredentials: true,
})

api.interceptors.response.use(
  (response) => response,
  async (error) => {
    const code = error.response?.data?.error?.code
    const cfg = error.config
    // A locked admin console answers 403 (never 401, which signs out); unlock and replay once so no work is lost.
    if (error.response?.status === 403 && (ELEVATION_CODES as readonly string[]).includes(code)
      && cfg && !cfg._elevationRetry && !String(cfg.url ?? '').includes('/admin/elevat')) {
      const ok = await useElevationStore().requestUnlock(code as ElevationCode)
      if (ok && code !== 'ADMIN_2FA_SETUP_REQUIRED') {
        return api({ ...cfg, _elevationRetry: true })
      }
    }
    if (error.response?.status === 401) {
      const auth = useAuthStore()
      // Never call logout() here: it POSTs /auth/logout, which would 401 and re-enter this handler forever.
      auth.clearSession()
      // /cli/authorize falls back to its own sign-in form; leaving it would lose the CLI's callback.
      if (!window.location.pathname.startsWith('/login') && !window.location.pathname.startsWith('/cli/')) {
        window.location.href = '/login'
      }
    }
    return Promise.reject(error)
  },
)

// apiErrorMessage prefers the envelope's human `message`, then falls back.
export function apiErrorMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (axios.isAxiosError(err)) {
    const e = err.response?.data?.error
    return e?.message || e?.error || err.message || fallback
  }
  return err instanceof Error ? err.message : fallback
}

export function statusTitle(status?: number): string {
  const key = statusKey(status)
  return t(`error.status.${key}`)
}

// The heading is ours; the API's own message stays underneath as the detail line.
function statusKey(status?: number): string {
  switch (status) {
    case 400: return 'badRequest'
    case 401: return 'unauthorized'
    case 402: return 'paymentRequired'
    case 403: return 'forbidden'
    case 404: return 'notFound'
    case 405: return 'forbidden'
    case 409: return 'conflict'
    case 422: return 'validationFailed'
    case 429: return 'tooManyRequests'
  }
  if (status && status >= 500) return 'serverError'
  if (status && status >= 400) return 'requestFailed'
  return 'serverError'
}

export interface ApiError {
  status?: number
  code?: string
  title: string
  message: string
  detail?: string
}

// apiError sets `detail` only when the raw `error` differs from `message`, so callers never render it twice.
export function apiError(err: unknown, fallback = 'Something went wrong'): ApiError {
  const resp = axios.isAxiosError(err) ? err.response : undefined
  const e = resp?.data?.error
  const status = e?.status_code || resp?.status
  const message = e?.message || (err instanceof Error ? err.message : '') || fallback
  const detail = e?.error && e.error !== message ? e.error : undefined
  return { status, code: e?.code, title: statusTitle(status), message, detail }
}

// The session travels as the HttpOnly cookie (caller opens EventSource withCredentials), so no token goes in the URL.
export function sseUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  return new URL(`${origin}${base}${path}`).toString()
}

// The session cookie is sent on same-origin downloads, so an href works with no token in the URL.
export function apiUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  return new URL(`${origin}${base}${path}`).toString()
}

// The browser sends the session cookie on the same-origin handshake, so no token query param is needed.
export function wsUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  const u = new URL(`${origin}${base}${path}`)
  u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
  return u.toString()
}

export default api

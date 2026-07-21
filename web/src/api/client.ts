import axios from 'axios'
import { useAuthStore } from '@/stores/auth'

const api = axios.create({
  baseURL: import.meta.env.VITE_API_URL || '/api/v1',
  headers: { 'Content-Type': 'application/json' },
  // Send the HttpOnly session cookie with every request. The JWT is no longer
  // stored in JS, so there is no Authorization header to attach.
  withCredentials: true,
})

api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      const auth = useAuthStore()
      // Clear local state only — never call logout() here: it POSTs /auth/logout,
      // which would itself 401 and re-enter this handler in an infinite loop.
      auth.clearSession()
      if (!window.location.pathname.startsWith('/login')) {
        window.location.href = '/login'
      }
    }
    return Promise.reject(error)
  },
)

// apiErrorMessage extracts a human message from a rejected request. The backend
// envelope is { error: { code, message, error } }; `message` is the human reason,
// so prefer it then fall back.
export function apiErrorMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (axios.isAxiosError(err)) {
    const e = err.response?.data?.error
    return e?.message || e?.error || err.message || fallback
  }
  return err instanceof Error ? err.message : fallback
}

// statusTitle maps an HTTP status to a friendly toast heading, so the title
// reflects what went wrong instead of a constant "Error".
export function statusTitle(status?: number): string {
  switch (status) {
    case 400: return 'Invalid request'
    case 401: return 'Authentication required'
    case 402: return 'Upgrade required'
    case 403: return 'Not allowed'
    case 404: return 'Not found'
    case 405: return 'Not allowed'
    case 409: return 'Conflict'
    case 422: return 'Validation failed'
    case 429: return 'Too many requests'
  }
  if (status && status >= 500) return 'Something went wrong'
  if (status && status >= 400) return 'Request failed'
  return 'Something went wrong'
}

// ApiError is the decomposed envelope error: the HTTP `status`, a status-derived
// `title`, the human `message`, an optional `detail` (the raw `error` field, only
// when it adds something beyond `message`), and the stable machine `code`.
export interface ApiError {
  status?: number
  code?: string
  title: string
  message: string
  detail?: string
}

// apiError decomposes a rejected request into an ApiError. `detail` is set only
// when the envelope's raw `error` differs from `message`, so callers never render
// the same line twice.
export function apiError(err: unknown, fallback = 'Something went wrong'): ApiError {
  const resp = axios.isAxiosError(err) ? err.response : undefined
  const e = resp?.data?.error
  const status = e?.status_code || resp?.status
  const message = e?.message || (err instanceof Error ? err.message : '') || fallback
  const detail = e?.error && e.error !== message ? e.error : undefined
  return { status, code: e?.code, title: statusTitle(status), message, detail }
}

// SSE EventSource URL. The session travels as the HttpOnly cookie, sent
// automatically on the same-origin request (EventSource is opened with
// withCredentials by the caller), so no token goes in the URL.
export function sseUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  return new URL(`${origin}${base}${path}`).toString()
}

// Absolute API URL for a same-origin file download (the session cookie is sent
// automatically, so an anchor/href works with no token in the URL).
export function apiUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  return new URL(`${origin}${base}${path}`).toString()
}

// WebSocket URL. The browser sends the session cookie on the same-origin
// handshake, so the JWT no longer needs to be carried as a query param.
export function wsUrl(path: string): string {
  const base = (import.meta.env.VITE_API_URL || '/api/v1') as string
  const origin = base.startsWith('http') ? '' : window.location.origin
  const u = new URL(`${origin}${base}${path}`)
  u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
  return u.toString()
}

export default api

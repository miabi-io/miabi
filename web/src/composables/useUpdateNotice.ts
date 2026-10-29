import { ref, type Ref } from 'vue'
import { infoApi } from '@/api/info'

// A tab left open across an upgrade keeps running the old web app against the new API.
// The Web UI ships with Miabi and carries its version, so the version the page loaded
// with is compared against the server's whenever the tab comes back into view.

const CHECK_EVERY_MS = 60_000

export interface UpdateNotice {
  available: Ref<boolean>
  check: () => Promise<void>
}

// createUpdateNotice holds the version seen first and flags a later, different one.
// "dev" builds never flag: every local rebuild would otherwise read as an upgrade.
export function createUpdateNotice(fetchVersion: () => Promise<string>, now: () => number = Date.now): UpdateNotice {
  const available = ref(false)
  let loaded: string | null = null
  let lastCheck = -Infinity
  async function check() {
    if (now() - lastCheck < CHECK_EVERY_MS) return
    lastCheck = now()
    let v: string
    try {
      v = await fetchVersion()
    } catch {
      return // offline or mid-restart: the next focus tries again
    }
    if (!v || v === 'dev') return
    if (loaded === null) loaded = v
    else if (v !== loaded) available.value = true
  }
  return { available, check }
}

let shared: UpdateNotice | null = null
const dismissed = ref(false)

// useUpdateNotice returns the page-wide notice. Module state, so switching between the
// admin and workspace shells keeps the version the page actually loaded with.
export function useUpdateNotice() {
  if (!shared) {
    shared = createUpdateNotice(async () => (await infoApi.get()).data.data.version)
    void shared.check()
    window.addEventListener('focus', () => void shared?.check())
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') void shared?.check()
    })
  }
  return {
    available: shared.available,
    dismissed,
    reload: () => window.location.reload(),
    dismiss: () => { dismissed.value = true },
  }
}

const CHUNK_ERROR = /dynamically imported module|Importing a module script failed|error loading dynamically/i

// isChunkLoadError recognises a lazy page whose file is gone because Miabi was upgraded under the tab.
export function isChunkLoadError(err: unknown): boolean {
  return CHUNK_ERROR.test(String((err as Error)?.message ?? err))
}

// shouldReloadForChunk allows one fresh load per minute, so a chunk that is really broken never loops.
export function shouldReloadForChunk(lastReload: number, now: number = Date.now()): boolean {
  return now - lastReload >= 60_000
}

// markUpdated shows the notice when an upgrade is seen another way, such as a missing page chunk.
export function markUpdated() {
  useUpdateNotice()
  if (shared) shared.available.value = true
}

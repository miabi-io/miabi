import { ref, watch } from 'vue'

export type LogSize = 'small' | 'medium' | 'large'

export const LOG_SIZES: { value: LogSize; label: string; title: string; height: string }[] = [
  { value: 'small', label: 'S', title: 'Small', height: '350px' },
  { value: 'medium', label: 'M', title: 'Medium', height: '600px' },
  { value: 'large', label: 'L', title: 'Large (fill screen)', height: 'calc(100vh - 240px)' },
]

const STORAGE_KEY = 'miabi_log_size'
const DEFAULT_SIZE: LogSize = 'medium'

export function isLogSize(v: unknown): v is LogSize {
  return v === 'small' || v === 'medium' || v === 'large'
}

export function logHeight(size: LogSize): string {
  return LOG_SIZES.find((s) => s.value === size)?.height ?? LOG_SIZES[1].height
}

function stored(): LogSize | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return isLogSize(v) ? v : null
  } catch { return null }
}

// One preference across every log panel, remembered between sessions. `initial`
// wins when present (a shared ?size= link), and is what gets remembered.
export function useLogSize(initial?: unknown) {
  const size = ref<LogSize>(isLogSize(initial) ? initial : (stored() ?? DEFAULT_SIZE))
  watch(size, (v) => {
    try { localStorage.setItem(STORAGE_KEY, v) } catch { /* private mode */ }
  }, { immediate: true })
  return { size, LOG_SIZES, logHeight }
}

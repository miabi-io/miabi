<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useLogSize, logHeight, type LogSize } from '@/composables/useLogSize'
import LogSizeControl from '@/components/LogSizeControl.vue'

// Reusable log panel: search/regex highlight, copy/download, follow, size
// presets, status badge. The parent owns the line buffer (and any cap); this
// component only renders + filters it.


const props = withDefaults(
  defineProps<{
    lines: string[]
    placeholder?: string
    emptyMatch?: string
    downloadName?: string
    // Optional status badge shown in the toolbar (e.g. a deployment status).
    statusLabel?: string
    statusClass?: string
    // When true, shows a pulsing "live" dot next to the status badge.
    streaming?: boolean
    // Optional note rendered above the view (e.g. "older output was trimmed").
    trimmedNote?: string
    defaultSize?: LogSize
    searchLabel?: string
  }>(),
  {
    placeholder: 'No output yet.',
    emptyMatch: 'No lines match your search.',
    downloadName: 'logs',
    statusLabel: '',
    statusClass: 'badge-neutral',
    streaming: false,
    trimmedNote: '',
    defaultSize: 'medium',
    searchLabel: 'Search logs',
  },
)

const { size: logSize } = useLogSize(props.defaultSize)
const logViewStyle = computed(() => ({
  height: logHeight(logSize.value),
  minHeight: logSize.value === 'large' ? '420px' : undefined,
}))

// Search: plain substring (default) or regex, with hit highlighting.
const logSearch = ref('')
const logRegexMode = ref(false)
const logMatcher = computed<RegExp | null>(() => {
  const q = logSearch.value.trim()
  if (!q) return null
  try {
    const pattern = logRegexMode.value ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    return new RegExp(pattern, 'gi')
  } catch {
    return null
  }
})
const logRegexError = computed(() => logRegexMode.value && !!logSearch.value.trim() && logMatcher.value === null)
const filtered = computed(() => {
  const re = logMatcher.value
  if (!re) return props.lines
  return props.lines.filter((l) => {
    re.lastIndex = 0
    return re.test(l)
  })
})

interface LogSegment { text: string; hit: boolean }
function logSegments(line: string): LogSegment[] {
  const re = logMatcher.value
  if (!re) return [{ text: line, hit: false }]
  const segs: LogSegment[] = []
  let last = 0
  let m: RegExpExecArray | null
  re.lastIndex = 0
  while ((m = re.exec(line)) !== null) {
    if (m.index > last) segs.push({ text: line.slice(last, m.index), hit: false })
    segs.push({ text: m[0], hit: true })
    last = m.index + m[0].length
    if (m[0].length === 0) re.lastIndex++
  }
  if (last < line.length) segs.push({ text: line.slice(last), hit: false })
  return segs.length ? segs : [{ text: line, hit: false }]
}

// Follow mode: stick to the newest line as output streams; scrolling up pauses.
const logFollow = ref(true)
const logViewEl = ref<HTMLElement | null>(null)
function scrollToBottom() {
  const el = logViewEl.value
  if (el) el.scrollTop = el.scrollHeight
}
function onScroll() {
  const el = logViewEl.value
  if (!el) return
  logFollow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}
function toggleFollow() {
  logFollow.value = !logFollow.value
  if (logFollow.value) nextTick(scrollToBottom)
}
watch([() => filtered.value.length, logSize], () => {
  if (logFollow.value) nextTick(scrollToBottom)
})

// Copy / download operate on the currently-visible (filtered) lines.
const logCopied = ref(false)
async function copyLogs() {
  try {
    await navigator.clipboard.writeText(filtered.value.join('\n'))
    logCopied.value = true
    setTimeout(() => { logCopied.value = false }, 1500)
  } catch { /* clipboard unavailable */ }
}
function downloadLogs() {
  const blob = new Blob([filtered.value.join('\n')], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${props.downloadName}.log`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="log-viewer">
    <div class="log-toolbar">
      <div class="log-search" :class="{ 'log-search-error': logRegexError }">
        <input
          v-model="logSearch"
          type="search"
          class="form-input log-search-input"
          :placeholder="logRegexMode ? 'Search logs (regex)…' : 'Search logs…'"
          :aria-label="searchLabel"
        />
        <button v-if="logSearch" type="button" class="log-search-clear" @click="logSearch = ''">Clear</button>
      </div>
      <button
        type="button"
        class="log-regex-toggle"
        :class="{ active: logRegexMode }"
        :aria-pressed="logRegexMode"
        title="Match using a regular expression"
        @click="logRegexMode = !logRegexMode"
      >.*</button>
      <span v-if="logRegexError" class="text-sm log-regex-err">invalid regex</span>
      <span v-else-if="logSearch.trim()" class="text-muted text-sm log-match-count">{{ filtered.length }} / {{ lines.length }}</span>
      <button
        type="button"
        class="log-icon-btn"
        :disabled="!filtered.length"
        :title="logSearch.trim() ? 'Copy matching lines' : 'Copy logs'"
        :aria-label="logSearch.trim() ? 'Copy matching lines' : 'Copy logs'"
        @click="copyLogs"
      >
        <span class="mdi" :class="logCopied ? 'mdi-check' : 'mdi-content-copy'"></span>
      </button>
      <button
        type="button"
        class="log-icon-btn"
        :disabled="!filtered.length"
        :title="logSearch.trim() ? 'Download matching lines' : 'Download logs'"
        :aria-label="logSearch.trim() ? 'Download matching lines' : 'Download logs'"
        @click="downloadLogs"
      >
        <span class="mdi mdi-tray-arrow-down"></span>
      </button>
      <button
        type="button"
        class="log-follow-btn"
        :class="{ active: logFollow }"
        :aria-pressed="logFollow"
        :title="logFollow ? 'Following new output — click to pause' : 'Jump to latest and follow'"
        @click="toggleFollow"
      >
        <span class="mdi mdi-chevron-double-down"></span>
        {{ logFollow ? 'Following' : 'Follow' }}
      </button>
      <LogSizeControl v-model="logSize" />
      <span v-if="statusLabel" class="badge" :class="[statusClass, { 'badge-dot': streaming }]">{{ statusLabel }}</span>
    </div>
    <p v-if="trimmedNote" class="text-muted text-sm log-trim-note">{{ trimmedNote }}</p>
    <div ref="logViewEl" class="code-block log-view" :style="logViewStyle" @scroll="onScroll">
      <span v-if="!lines.length" class="log-placeholder">{{ placeholder }}</span>
      <span v-else-if="!filtered.length" class="log-placeholder">{{ emptyMatch }}</span>
      <template v-else>
        <div v-for="(line, i) in filtered" :key="i" class="log-line"><span v-for="(seg, j) in logSegments(line)" :key="j" :class="{ 'log-hit': seg.hit }">{{ seg.text }}</span></div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.log-view { height: 600px; overflow: auto; white-space: pre-wrap; }
.log-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; margin-bottom: 12px; }
.log-search { position: relative; display: flex; align-items: center; }
.log-search-input { width: 240px; padding-right: 52px; }
.log-search-clear {
  position: absolute; right: 8px; background: none; border: none; padding: 0;
  font-size: 12px; color: var(--text-secondary); cursor: pointer;
}
.log-search-clear:hover { color: var(--text-primary); }
.log-search-error .log-search-input { border-color: var(--danger-500); }
.log-match-count { white-space: nowrap; }
.log-regex-err { white-space: nowrap; color: var(--danger-500); }
.log-regex-toggle {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 30px; height: 30px; padding: 0 6px;
  font-family: 'JetBrains Mono', monospace; font-size: 13px; font-weight: 600;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-regex-toggle:hover { color: var(--text-primary); }
.log-regex-toggle.active { background: var(--primary-600); color: #fff; border-color: var(--primary-600); }
.log-follow-btn {
  display: inline-flex; align-items: center; gap: 4px; white-space: nowrap;
  height: 30px; padding: 0 10px; font-size: 12px; font-weight: 600;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-follow-btn:hover { color: var(--text-primary); }
.log-follow-btn.active { background: var(--primary-600); color: #fff; border-color: var(--primary-600); }
.log-follow-btn .mdi { font-size: 15px; }
.log-icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; font-size: 15px;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-icon-btn:hover:not(:disabled) { color: var(--text-primary); }
.log-icon-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.log-trim-note { margin: 0 0 8px; }
.log-line { min-height: 1.4em; }
.log-hit { background: var(--warning-400, #facc15); color: #1a1a2e; border-radius: 2px; }
.log-placeholder { color: var(--text-secondary); }
</style>

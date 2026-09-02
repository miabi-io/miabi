<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { wsUrl } from '@/api/client'
import AppModal from '@/components/AppModal.vue'

// ShellTerminal opens a live interactive shell into an application's running
// container over a WebSocket. The connection is authenticated via ?token= and
// authorized server-side (Admin+ and the plan's shell-exec capability).
const props = defineProps<{
  // base is the app's API path, e.g. /workspaces/3/apps/12
  base: string
  appName: string
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const host = ref<HTMLDivElement | null>(null)
const status = ref<'connecting' | 'open' | 'closed'>('connecting')

// A shell is worked in, not glanced at, so the chosen size is remembered.
const sizeKey = 'miabi.shell.size'
const sizes = ['compact', 'normal', 'full'] as const
type ShellSize = (typeof sizes)[number]

const stored = localStorage.getItem(sizeKey) as ShellSize | null
const size = ref<ShellSize>(stored && sizes.includes(stored) ? stored : 'normal')

const dialogClass = { compact: 'modal-lg', normal: 'modal-xl', full: 'modal-full' }

function setSize(next: ShellSize) {
  size.value = next
  localStorage.setItem(sizeKey, next)
  // Resizing is not leaving the shell: without this the caret stays on the button that was clicked.
  void nextTick(() => term?.focus())
}

// The terminal sizes itself to its box, so it has to be told the box changed.
watch(size, () => nextTick(sendResize))

let term: Terminal | null = null
let fit: FitAddon | null = null
let socket: WebSocket | null = null
let resizeObserver: ResizeObserver | null = null

function send(obj: Record<string, unknown>) {
  if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(obj))
}

function sendResize() {
  if (!fit || !term) return
  try {
    fit.fit()
  } catch {
    /* container not measurable yet */
  }
  send({ type: 'resize', cols: term.cols, rows: term.rows })
}

onMounted(() => {
  if (!host.value) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    theme: { background: '#0b0f17' },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(host.value)
  fit.fit()
  // After the modal has placed itself, or its focus trap takes the caret back off the shell.
  void nextTick(() => term?.focus())

  socket = new WebSocket(wsUrl(`${props.base}/exec`))
  socket.binaryType = 'arraybuffer'

  socket.onopen = () => {
    status.value = 'open'
    sendResize()
  }
  socket.onmessage = (ev) => {
    if (typeof ev.data === 'string') term?.write(ev.data)
    else term?.write(new Uint8Array(ev.data as ArrayBuffer))
  }
  socket.onclose = () => {
    status.value = 'closed'
  }
  socket.onerror = () => {
    term?.write('\r\n\x1b[31mconnection error\x1b[0m\r\n')
  }

  term.onData((d) => send({ type: 'stdin', data: d }))
  term.onResize(() => send({ type: 'resize', cols: term!.cols, rows: term!.rows }))

  resizeObserver = new ResizeObserver(() => sendResize())
  resizeObserver.observe(host.value)
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  socket?.close()
  term?.dispose()
})
</script>

<template>
  <AppModal :dialog-class="dialogClass[size]" :auto-focus="false" @close="emit('close')">
    <div class="modal-header">
      <h3>
        <span class="mdi mdi-console-line"></span>
        Shell — {{ appName }}
        <span class="shell-status" :class="status">{{ status }}</span>
        <span v-if="status !== 'closed'" class="shell-hint">Esc goes to the shell · Shift+Tab to leave it</span>
      </h3>
      <div class="shell-controls">
        <button
          class="btn-icon btn-icon-muted"
          :class="{ active: size === 'compact' }"
          title="Compact"
          aria-label="Compact"
          @click="setSize('compact')"
        >
          <span class="mdi mdi-window-minimize"></span>
        </button>
        <button
          class="btn-icon btn-icon-muted"
          :class="{ active: size === 'normal' }"
          title="Restore"
          aria-label="Restore"
          @click="setSize('normal')"
        >
          <span class="mdi mdi-window-restore"></span>
        </button>
        <button
          class="btn-icon btn-icon-muted"
          :class="{ active: size === 'full' }"
          title="Maximize"
          aria-label="Maximize"
          @click="setSize('full')"
        >
          <span class="mdi mdi-window-maximize"></span>
        </button>
        <button class="btn-icon btn-icon-muted" title="Close" aria-label="Close" @click="emit('close')">
          <span class="mdi mdi-close"></span>
        </button>
      </div>
    </div>
    <div class="modal-body shell-body" :class="'shell-' + size">
      <div
        ref="host"
        class="shell-host"
        data-modal-tab-through
        :data-modal-escape-through="status === 'closed' ? undefined : ''"
      ></div>
    </div>
  </AppModal>
</template>

<style scoped>
.shell-controls {
  display: flex;
  align-items: center;
  gap: 2px;
}
.shell-controls .btn-icon.active {
  color: var(--text-primary);
  background: var(--bg-tertiary);
}
.shell-body {
  padding: 0;
  background: #0b0f17;
  display: flex;
  flex: 1;
  min-height: 0;
}
.shell-host {
  width: 100%;
  padding: 8px;
}
.shell-compact .shell-host { height: 40vh; }
.shell-normal .shell-host { height: 60vh; }
.shell-full .shell-host { height: 100%; }
.shell-hint {
  font-size: 11px;
  font-weight: 400;
  color: var(--text-muted);
  margin-left: 10px;
  vertical-align: middle;
  text-transform: none;
  letter-spacing: 0;
}
.shell-status {
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 2px 8px;
  border-radius: 999px;
  margin-left: 8px;
  vertical-align: middle;
}
.shell-status.connecting {
  background: var(--color-warning-bg, #4a3b1a);
  color: var(--color-warning, #e0b341);
}
.shell-status.open {
  background: var(--color-success-bg, #14361f);
  color: var(--color-success, #4ade80);
}
.shell-status.closed {
  background: var(--color-danger-bg, #3a1a1a);
  color: var(--color-danger, #f87171);
}
</style>

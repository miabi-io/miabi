<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
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
  term.focus()

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
  <AppModal dialog-class="shell-modal" @close="emit('close')">
    <div class="modal-header">
      <h3>
        <span class="mdi mdi-console-line"></span>
        Shell — {{ appName }}
        <span class="shell-status" :class="status">{{ status }}</span>
      </h3>
      <button class="btn-icon btn-icon-muted" title="Close" aria-label="Close" @click="emit('close')">
        <span class="mdi mdi-close"></span>
      </button>
    </div>
    <div class="modal-body shell-body">
      <div ref="host" class="shell-host"></div>
    </div>
  </AppModal>
</template>

<style scoped>
.shell-modal {
  max-width: 960px;
  width: 100%;
}
.shell-body {
  padding: 0;
  background: #0b0f17;
}
.shell-host {
  width: 100%;
  height: 60vh;
  padding: 8px;
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

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useNotificationStore } from '@/stores/notification'

// Completion is derived from real resources by the parent, so each step ticks
// itself as the user makes progress — no separate step-tracking to drift.
const props = defineProps<{
  hasWorkspace: boolean
  hasApp: boolean
  hasDomain: boolean
}>()

const router = useRouter()
const auth = useAuthStore()
const notify = useNotificationStore()

const dismissing = ref(false)

interface Step {
  key: string
  label: string
  desc: string
  done: boolean
  cta: string
  to: string
}

const steps = computed<Step[]>(() => [
  {
    key: 'workspace',
    label: 'workspaces.createYourFirstWorkspace',
    desc: 'A workspace groups your applications, databases, domains and team.',
    done: props.hasWorkspace,
    cta: 'Create workspace',
    to: '/workspaces?create=1',
  },
  {
    key: 'app',
    label: 'apps.deployFirst',
    desc: 'Launch from a Docker image, a Git repository, or the marketplace.',
    done: props.hasApp,
    cta: 'Deploy an app',
    to: '/apps',
  },
  {
    key: 'domain',
    label: 'gettingStarted.connectDomain',
    desc: 'Point a custom domain at your app and get automatic SSL.',
    done: props.hasDomain,
    cta: 'Add a domain',
    to: '/domains',
  },
])

const doneCount = computed(() => steps.value.filter((s) => s.done).length)
const allDone = computed(() => doneCount.value === steps.value.length)
// Only the first unfinished step is actionable — the flow is sequential (you
// can't deploy without a workspace, or route a domain without an app).
const activeKey = computed(() => steps.value.find((s) => !s.done)?.key)

async function dismiss() {
  dismissing.value = true
  try {
    await auth.dismissOnboarding()
  } catch (e) {
    notify.apiError(e, 'Could not dismiss')
  } finally {
    dismissing.value = false
  }
}

function go(to: string) {
  router.push(to)
}
</script>

<template>
  <div class="card gs-card">
    <div class="gs-head">
      <div class="gs-head-left">
        <h2 class="gs-title">
          <span class="mdi mdi-rocket-launch-outline"></span>
          {{ allDone ? "You're all set!" : 'Get started with Miabi' }}
        </h2>
        <p class="gs-sub">
          {{ allDone
            ? 'Your platform is up and running. You can revisit these anytime.'
            : 'A few steps to get your first app online.' }}
        </p>
      </div>
      <div class="gs-head-right">
        <span v-if="!allDone" class="gs-progress">{{ doneCount }}/{{ steps.length }}</span>
        <button
          class="btn btn-secondary btn-sm"
          :disabled="dismissing"
          @click="dismiss"
        >
          {{ allDone ? 'Finish' : 'Dismiss' }}
        </button>
      </div>
    </div>

    <ul v-if="!allDone" class="gs-steps">
      <li v-for="s in steps" :key="s.key" class="gs-step" :class="{ 'is-done': s.done, 'is-active': s.key === activeKey }">
        <div class="gs-step-text">
        <div class="gs-step-header-row">

          <span class="gs-marker">
            <span v-if="s.done" class="mdi mdi-check-circle"></span>
            <span v-else class="mdi mdi-circle-outline"></span>
          </span>
          <div class="gs-step-label">{{ $t(s.label) }}</div>
        </div>
          <div class="gs-step-desc">{{ s.desc }}</div>
        </div>
        <button
          v-if="!s.done && s.key === activeKey"
          class="btn btn-primary btn-sm gs-step-cta"
          @click="go(s.to)"
        >
          {{ s.cta }} <span class="mdi mdi-arrow-right"></span>
        </button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.gs-card {
  padding: 18px 20px;
  margin-bottom: 20px;
  border: 1px solid var(--primary-300);
  background: var(--primary-50);
}

[data-theme="dark"] .gs-card {
  border: 1px solid var(--primary-900);
}

.gs-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.gs-head-left {
  flex: 1;
  min-width: 0;
}

.gs-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 16px;
  color: var(--text-primary);
}

.gs-title .mdi {
  color: var(--primary-600);
}

.gs-sub {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--text-secondary);
  line-height: 1.4;
}

.gs-head-right {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.gs-progress {
  font-size: 13px;
  font-weight: 600;
  color: var(--primary-600);
  font-variant-numeric: tabular-nums;
}

.gs-steps {
  list-style: none;
  margin: 16px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.gs-step {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border-radius: var(--radius);
  transition: background 0.15s;
}

.gs-step.is-active {
  background: var(--bg-primary, #fff);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
}

.gs-step-header {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1;
  min-width: 0;
}

.gs-marker {
  display: inline-flex;
  flex-shrink: 0;
}

.gs-marker .mdi {
  font-size: 20px;
  color: var(--text-muted);
}

.gs-step.is-done .gs-marker .mdi {
  color: var(--success-500, #16a34a);
}

.gs-step-text {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.gs-step-header-row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.gs-step-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  word-break: break-word;
  line-height: 1.2;
}

.gs-step.is-done .gs-step-label {
  color: var(--text-secondary);
  text-decoration: line-through;
}

.gs-step-desc {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 1px;
  padding-left: 32px;
}

.gs-step-cta {
  flex-shrink: 0;
}

.gs-step-cta .mdi {
  font-size: 14px;
}

@media (max-width: 639px) {
  .gs-card {
    padding: 14px 16px;
    margin-bottom: 16px;
    margin-left: 1px;
    margin-right: 1px;
  }

  .gs-head {
    flex-direction: row;
    gap: 6px;
  }

  .gs-head-right {
    align-self: flex-start;
  }

  .gs-title {
    font-size: 14px;
  }

  .gs-sub {
    font-size: 12px;
  }

  .gs-steps {
    gap: 8px;
  }

  .gs-step {
    flex-direction: column;
    align-items: stretch;
    gap: 4px;
    padding: 14px 16px;
  }

  .gs-step-header {
    align-items: flex-start;
  }

  .gs-marker {
    margin-top: 1px;
  }

  .gs-step-desc {
    padding-left: 12px;
  }

  .gs-step-cta {
    width: 100%;
    justify-content: center;
  }
}
</style>

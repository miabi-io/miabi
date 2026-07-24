<script setup lang="ts">
import { computed, ref } from 'vue'
import { useNotificationStore } from '@/stores/notification'

// CLI onboarding: install → sign in → verify. The sign-in command is the whole
// point of the old "Copy login command" menu item — `miabi login` opens the
// browser and captures the token itself, so there is nothing to paste.
const notify = useNotificationStore()

const GITHUB_REPO = 'https://github.com/miabi-io/miabi-cli'
const RELEASES = `${GITHUB_REPO}/releases/latest`
const TAP_REPO = 'https://github.com/miabi-io/homebrew-tap'

// The panel URL the CLI targets — the origin the user actually reached us at.
const serverUrl = window.location.origin.replace(/\/+$/, '')

type InstallTab = 'brew' | 'go' | 'binary' | 'docker'
// Default the install method to the visitor's platform; Homebrew covers
// macOS + Linux, Windows users get the prebuilt binary.
const isWindows = /win/i.test(navigator.userAgent)
const tab = ref<InstallTab>(isWindows ? 'binary' : 'brew')
const installTabs: { key: InstallTab; label: string }[] = [
  { key: 'brew', label: 'Homebrew' },
  { key: 'go', label: 'Go' },
  { key: 'binary', label: 'Binary' },
  { key: 'docker', label: 'Docker' },
]

const installCmd = computed(() => {
  switch (tab.value) {
    case 'go':
      return 'go install github.com/miabi-io/miabi-cli@latest'
    case 'binary':
      return `curl -fsSL ${GITHUB_REPO}/releases/latest/download/miabi_linux_amd64.tar.gz \\\n  | tar -xz miabi && sudo mv miabi /usr/local/bin/`
    case 'docker':
      return 'docker run --rm -e MIABI_SERVER -e MIABI_TOKEN miabi/miabi-cli:latest whoami'
    default:
      return 'brew install miabi-io/tap/miabi'
  }
})

const loginCmd = computed(() => `miabi login --server ${serverUrl}`)
const ciEnv = computed(() => `export MIABI_SERVER=${serverUrl}\nexport MIABI_TOKEN=<your-token>`)

// Per-snippet copy feedback (the check swaps in for ~1.5s).
const copied = ref('')
async function copy(text: string, key: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = key
    setTimeout(() => (copied.value = ''), 1500)
  } catch {
    notify.info(text, { title: 'Copy this command' })
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>CLI access</h1>
      <p class="page-sub">Install the Miabi CLI and sign in to this panel from your terminal.</p>
    </div>

    <!-- 1. Install -->
    <div class="card">
      <div class="card-header"><h2><span class="step">1</span> Install the CLI</h2></div>
      <div class="card-body">
        <div class="tabs">
          <button
            v-for="t in installTabs"
            :key="t.key"
            class="tab"
            :class="{ active: tab === t.key }"
            @click="tab = t.key"
          >
            {{ t.label }}
          </button>
        </div>

        <div class="snippet">
          <code>{{ installCmd }}</code>
          <button class="copy-btn" title="Copy" @click="copy(installCmd, 'install')">
            <span class="mdi" :class="copied === 'install' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>

        <p v-if="tab === 'brew'" class="hint">
          Homebrew 6 requires third-party taps to be trusted first — if the short form fails, run
          <code class="inline">brew tap miabi-io/tap &amp;&amp; brew trust miabi-io/tap</code>. Formula:
          <a :href="TAP_REPO" target="_blank" rel="noopener">miabi-io/homebrew-tap</a>.
        </p>
        <p v-else-if="tab === 'binary'" class="hint">
          Swap <code class="inline">linux_amd64</code> for your platform
          (<code class="inline">linux_arm64</code>, <code class="inline">darwin_arm64</code>, or the
          <code class="inline">.zip</code> on Windows) — see
          <a :href="RELEASES" target="_blank" rel="noopener">all releases</a>.
        </p>
        <p v-else-if="tab === 'docker'" class="hint">
          No install needed — handy in CI. Set <code class="inline">MIABI_SERVER</code> and
          <code class="inline">MIABI_TOKEN</code> (see step 4).
        </p>
      </div>
    </div>

    <!-- 2. Sign in -->
    <div class="card">
      <div class="card-header"><h2><span class="step">2</span> Sign in</h2></div>
      <div class="card-body">
        <div class="snippet">
          <code>{{ loginCmd }}</code>
          <button class="copy-btn" title="Copy" @click="copy(loginCmd, 'login')">
            <span class="mdi" :class="copied === 'login' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
        <p class="hint">
          This opens your browser to sign in (password or SSO) and captures the token automatically —
          nothing to copy or paste. On a machine without a browser, use
          <code class="inline">miabi login --no-browser</code> and paste a token instead.
        </p>
      </div>
    </div>

    <!-- 3. Verify -->
    <div class="card">
      <div class="card-header"><h2><span class="step">3</span> Verify</h2></div>
      <div class="card-body">
        <div class="snippet">
          <code>miabi whoami</code>
          <button class="copy-btn" title="Copy" @click="copy('miabi whoami', 'whoami')">
            <span class="mdi" :class="copied === 'whoami' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
        <p class="hint">
          Then try <code class="inline">miabi apps ls</code> to list your applications, or
          <code class="inline">miabi --help</code> for everything else.
        </p>
      </div>
    </div>

    <!-- 4. CI -->
    <div class="card">
      <div class="card-header"><h2><span class="step">4</span> CI &amp; automation</h2></div>
      <div class="card-body">
        <p class="hint" style="margin-top: 0">
          Pipelines can't open a browser. Mint a token and pass it via the environment instead.
        </p>
        <div class="snippet">
          <code class="pre">{{ ciEnv }}</code>
          <button class="copy-btn" title="Copy" @click="copy(ciEnv, 'ci')">
            <span class="mdi" :class="copied === 'ci' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
        <p class="hint">
          <a href="/request-token" target="_blank" rel="noopener">Create a CLI token →</a>
        </p>
      </div>
    </div>

    <!-- Links -->
    <div class="card">
      <div class="card-body links">
        <a :href="GITHUB_REPO" target="_blank" rel="noopener" class="link-item">
          <span class="mdi mdi-github"></span>
          <span><strong>Source &amp; issues</strong><small>miabi-io/miabi-cli</small></span>
        </a>
        <a :href="RELEASES" target="_blank" rel="noopener" class="link-item">
          <span class="mdi mdi-package-variant-closed"></span>
          <span><strong>Releases</strong><small>Changelog &amp; binaries</small></span>
        </a>
        <a :href="TAP_REPO" target="_blank" rel="noopener" class="link-item">
          <span class="mdi mdi-beer-outline"></span>
          <span><strong>Homebrew tap</strong><small>miabi-io/homebrew-tap</small></span>
        </a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-sub { color: var(--text-muted); margin: 4px 0 0; font-size: 14px; }
.card { margin-bottom: 16px; }
.card-header h2 { display: flex; align-items: center; gap: 10px; margin: 0; font-size: 15px; }
.step {
  display: inline-grid; place-items: center; width: 22px; height: 22px; border-radius: 50%;
  background: var(--primary-600, #2563eb); color: #fff; font-size: 12px; font-weight: 600;
}

.tabs { display: flex; gap: 2px; margin-bottom: 12px; border-bottom: 1px solid var(--border-primary); }
.tab {
  appearance: none; background: none; border: none; border-bottom: 2px solid transparent;
  padding: 8px 12px; font-size: 13px; color: var(--text-muted); cursor: pointer;
}
.tab:hover { color: var(--text-primary); }
.tab.active { color: var(--primary-600, #2563eb); border-bottom-color: var(--primary-600, #2563eb); font-weight: 600; }

.snippet {
  display: flex; align-items: flex-start; gap: 8px;
  background: var(--bg-secondary); border: 1px solid var(--border-primary);
  border-radius: 8px; padding: 10px 12px;
}
.snippet code {
  flex: 1; font-family: monospace; font-size: 13px; color: var(--text-primary); word-break: break-all;
}
.snippet code.pre { white-space: pre-wrap; }
.copy-btn { background: none; border: none; cursor: pointer; color: var(--text-muted); padding: 2px; flex-shrink: 0; }
.copy-btn:hover { color: var(--primary-600, #2563eb); }

.hint { font-size: 13px; color: var(--text-muted); margin: 10px 0 0; line-height: 1.55; }
.hint a { color: var(--primary-600, #2563eb); }
code.inline {
  font-family: monospace; font-size: 12px; background: var(--bg-secondary);
  border: 1px solid var(--border-primary); border-radius: 4px; padding: 1px 5px;
}

.links { display: flex; flex-wrap: wrap; gap: 12px; }
.link-item {
  display: flex; align-items: center; gap: 10px; flex: 1 1 200px;
  padding: 12px; border: 1px solid var(--border-primary); border-radius: 8px;
  color: var(--text-primary); text-decoration: none;
}
.link-item:hover { border-color: var(--primary-600, #2563eb); }
.link-item .mdi { font-size: 22px; color: var(--text-muted); }
.link-item span { display: flex; flex-direction: column; }
.link-item small { color: var(--text-muted); font-size: 12px; }
</style>

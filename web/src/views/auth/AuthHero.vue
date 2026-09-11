<script setup lang="ts">
// The marketing panel beside the auth form. Extracted from Login.vue so the
// sign-up page shows the same thing without a second copy of it — 100 lines of
// gradients and a watermark are not worth maintaining twice.
//
// brandName replaces the wordmark for a white-labelled install; unset keeps the
// "Miabi.io" lockup with its accented ".io".
import MiabiWordmark from '@/components/MiabiWordmark.vue'

defineProps<{ brandName?: string }>()
</script>

<template>
    <aside class="auth-hero">
      <div class="auth-hero-inner">
        <!-- Brand lockup: the new pinwheel mark + "Miabi.io" wordmark; the
             trailing ".io" carries the brand accent. -->
        <div class="auth-hero-wordmark">
          <img src="/brand/miabi-mark-white.svg" alt="" class="auth-hero-mark" />
          <span v-if="brandName" class="auth-hero-name">{{ brandName }}</span>
          <MiabiWordmark v-else :height="24" />
        </div>

        <div class="auth-hero-body">
          <h2 class="auth-hero-title">Self-hosting,<br />reimagined.</h2>
          <p class="auth-hero-lead">
            Deploy, scale, and manage applications from one intuitive platform.
          </p>
          <ul class="auth-hero-features">
            <li><span class="mdi mdi-package-variant-closed"></span> Built-in Container Registry</li>
            <li><span class="mdi mdi-lock-check-outline"></span> Secrets &amp; Automatic TLS</li>
            <li><span class="mdi mdi-infinity"></span> GitOps &amp; Canary Deployments</li>
            <li><span class="mdi mdi-database-outline"></span> Managed databases, backups &amp; volumes</li>
            <li><span class="mdi mdi-chart-areaspline"></span> Monitoring &amp; Release History</li>
            <li><span class="mdi mdi-account-group-outline"></span> Multi-tenant Workspaces &amp; RBAC</li>
          </ul>
        </div>

        <p class="auth-hero-foot">Open-source · Self-hosted PaaS for Docker</p>
      </div>
    </aside>
</template>

<style scoped>
.auth-hero {
  position: relative;
  overflow: hidden;
  display: flex;
  color: #fff;
  background:
    radial-gradient(120% 80% at 100% 0%, rgba(255, 255, 255, 0.16), transparent 55%),
    radial-gradient(90% 70% at 0% 100%, rgba(13, 20, 36, 0.5), transparent 60%),
    linear-gradient(150deg, var(--primary-600) 0%, var(--primary-800) 70%, #2a0f4d 100%);
}
/* faint glyph watermark */
.auth-hero::after {
  content: '';
  position: absolute;
  right: -8%;
  bottom: -12%;
  width: 520px;
  height: 520px;
  background: url('/brand/miabi-mark-white.svg') center / contain no-repeat;
  opacity: 0.06;
  pointer-events: none;
}
.auth-hero-inner {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-width: 460px;
  margin: auto;
  padding: 56px 52px;
}
.auth-hero-wordmark {
  display: flex;
  align-items: center;
  gap: 12px;
  align-self: flex-start;
  margin-bottom: auto;
}
.auth-hero-mark {
  height: 40px;
  width: 40px;
}
.auth-hero-name {
  font-family: var(--font-brand);
  font-size: 1.6rem;
  font-weight: 700;
  color: #fff;
}
.auth-hero-body {
  margin: 48px 0;
}
.auth-hero-title {
  font-size: clamp(1.9rem, 2.6vw, 2.6rem);
  font-weight: 800;
  line-height: 1.1;
  letter-spacing: -0.02em;
  margin: 0 0 16px;
}
.auth-hero-lead {
  font-size: 15px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.82);
  max-width: 40ch;
  margin: 0 0 28px;
}
.auth-hero-features {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.auth-hero-features li {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 14px;
  color: rgba(255, 255, 255, 0.92);
}
.auth-hero-features .mdi {
  font-size: 20px;
  flex-shrink: 0;
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius);
  background: rgba(255, 255, 255, 0.12);
}
.auth-hero-foot {
  margin: 0;
  font-size: 12.5px;
  color: rgba(255, 255, 255, 0.6);
}

/* The hero is decoration: below this width the form gets the whole screen. */
@media (max-width: 900px) {
  .auth-hero {
    display: none;
  }
}
</style>

export const MAINTENANCE_HTML_MAX = 64 * 1024

export function htmlBytes(html: string): number {
  return new TextEncoder().encode(html.trim()).length
}

function escapeHtml(v: string): string {
  return v.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}

export interface MaintenancePageLabels {
  badge: string
  checkAgain: string
  refreshHint: string
  hostedOn: string
}

const PETAL = 'M486 168C416 166 306 168 326 168C226 168 168 226 168 326L168 354C168 454 226 486 326 486L354 486C416 486 458 458 472 472C458 458 486 416 486 354L486 168Z'

// Each SVG gets its own gradient id: two marks sharing one id render the second blank in some browsers.
function mark(size: number, id: string): string {
  const petals = [0, 90, 180, 270].map((r) => `<path transform="rotate(${r} 512 512)" d="${PETAL}" fill="url(#${id})"/>`).join('')
  return `<svg width="${size}" height="${size}" viewBox="0 0 1024 1024" aria-hidden="true"><defs><linearGradient id="${id}" x1="0" y1="0" x2="1024" y2="1024" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="#c084fc"/><stop offset=".33" stop-color="#a855f7"/><stop offset=".68" stop-color="#9333ea"/><stop offset="1" stop-color="#7e22ce"/></linearGradient></defs>${petals}</svg>`
}

// Self-contained on purpose: every request to a parked route, assets included, gets this same page.
export function maintenancePageTemplate(opts: { app: string; heading: string; message: string; labels: MaintenancePageLabels; lang?: string }): string {
  const l = opts.labels
  return `<!doctype html>
<html lang="${escapeHtml(opts.lang || 'en')}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta http-equiv="refresh" content="60">
<title>${escapeHtml(opts.app)} · ${escapeHtml(l.badge)}</title>
<style>
:root { color-scheme: light dark; --bg: #f7f5fb; --card: #ffffff; --text: #14172b; --muted: #5d6280; --accent: #9333ea; --soft: #f3e8ff; --line: #e7e3f0; }
@media (prefers-color-scheme: dark) { :root { --bg: #0b1020; --card: #131a33; --text: #eef0f8; --muted: #9aa1bf; --accent: #c084fc; --soft: rgba(168,85,247,.16); --line: #232b4a; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 20px; padding: 24px 16px; background: var(--bg); color: var(--text); font: 16px/1.6 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
main { width: 100%; max-width: 500px; background: var(--card); border: 1px solid var(--line); border-radius: 20px; padding: 40px 32px 32px; display: flex; flex-direction: column; align-items: center; gap: 16px; text-align: center; box-shadow: 0 1px 2px rgba(20,23,43,.04), 0 12px 32px -12px rgba(88,28,135,.18); }
.pill { display: inline-flex; align-items: center; gap: 8px; padding: 4px 12px; border-radius: 999px; background: var(--soft); color: var(--accent); font-size: 13px; font-weight: 600; }
.pill::before { content: ""; width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
h1 { margin: 0; font-size: 26px; line-height: 1.25; letter-spacing: -.01em; text-wrap: balance; }
p { margin: 0; color: var(--muted); }
form { margin: 8px 0 0; }
button { font: inherit; font-size: 14px; font-weight: 600; padding: 9px 18px; border-radius: 10px; border: 1px solid var(--line); background: var(--card); color: var(--text); cursor: pointer; }
button:hover, button:focus-visible { border-color: var(--accent); }
.hint { font-size: 13px; }
footer { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--muted); }
footer strong { color: var(--text); font-weight: 600; }
</style>
</head>
<body>
  <main>
    ${mark(48, 'mb-mark')}
    <span class="pill">${escapeHtml(l.badge)}</span>
    <h1>${escapeHtml(opts.heading)}</h1>
    <p>${escapeHtml(opts.message)}</p>
    <form method="get"><button type="submit">${escapeHtml(l.checkAgain)}</button></form>
    <p class="hint">${escapeHtml(l.refreshHint)}</p>
  </main>
  <footer>${escapeHtml(l.hostedOn)} ${mark(14, 'mb-mark-sm')} <strong>Miabi</strong></footer>
</body>
</html>
`
}

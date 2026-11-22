export const MAINTENANCE_HTML_MAX = 64 * 1024

export function htmlBytes(html: string): number {
  return new TextEncoder().encode(html.trim()).length
}

function escapeHtml(v: string): string {
  return v.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}

// Self-contained on purpose: every request to a parked route, assets included, gets this same page.
export function maintenancePageTemplate(opts: { title: string; heading: string; message: string; lang?: string }): string {
  const title = escapeHtml(opts.title)
  const heading = escapeHtml(opts.heading)
  const message = escapeHtml(opts.message)
  return `<!doctype html>
<html lang="${escapeHtml(opts.lang || 'en')}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>${title}</title>
  <style>
    :root { color-scheme: light dark; --bg: #f8fafc; --card: #ffffff; --text: #0f172a; --muted: #64748b; --accent: #f59e0b; --border: #e2e8f0; }
    @media (prefers-color-scheme: dark) {
      :root { --bg: #0b1120; --card: #111827; --text: #f1f5f9; --muted: #94a3b8; --border: #1f2937; }
    }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px; background: var(--bg); color: var(--text); font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
    main { max-width: 480px; width: 100%; text-align: center; background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 40px 32px; }
    .icon { width: 56px; height: 56px; margin: 0 auto 20px; border-radius: 50%; display: grid; place-items: center; background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent); font-size: 28px; }
    h1 { margin: 0 0 12px; font-size: 24px; line-height: 1.3; }
    p { margin: 0; color: var(--muted); line-height: 1.6; }
  </style>
</head>
<body>
  <main>
    <div class="icon" aria-hidden="true">&#9881;</div>
    <h1>${heading}</h1>
    <p>${message}</p>
  </main>
</body>
</html>
`
}

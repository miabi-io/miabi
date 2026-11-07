// Mirrors the server's naming rule so the form previews names; server-side dedupe suffixes can't be predicted here.

// Must match internal/slug.Make.
export function slugify(name: string, fallback = ''): string {
  const s = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/(^-+|-+$)/g, '')
  return s || fallback
}

export function resourceName(installLabel: string, localName: string): string {
  return slugify(`${installLabel}-${localName}`, localName)
}

// Volumes always carry their local name: a "content" and a "db" volume are not interchangeable.
export function displayFor(installLabel: string, localName: string, count: number): string {
  return count > 1 ? `${installLabel} ${localName}` : installLabel
}

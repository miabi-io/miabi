export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface Wire {
  d: string
  mid: { x: number; y: number }
}

// Ends 2px short so the arrowhead marker lands on the box edge rather than under it.
export function wirePath(a: Box, b: Box, vertical: boolean): Wire {
  if (vertical) {
    const x1 = a.x + a.w / 2
    const y1 = a.y + a.h
    const x2 = b.x + b.w / 2
    const y2 = b.y - 2
    const my = (y1 + y2) / 2
    return { d: `M${x1} ${y1} C${x1} ${my} ${x2} ${my} ${x2} ${y2}`, mid: { x: (x1 + x2) / 2, y: my } }
  }
  const x1 = a.x + a.w
  const y1 = a.y + a.h / 2
  const x2 = b.x - 2
  const y2 = b.y + b.h / 2
  const mx = (x1 + x2) / 2
  return { d: `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`, mid: { x: mx, y: (y1 + y2) / 2 } }
}

export function relativeBox(el: Element, origin: Element): Box {
  const r = el.getBoundingClientRect()
  const o = origin.getBoundingClientRect()
  return { x: r.left - o.left, y: r.top - o.top, w: r.width, h: r.height }
}

// Formatting the dashboard shares with the backend's own wording, so a number reads the
// same in a report, an alert and on screen.

export function pct(v: number | null | undefined, places = 1): string {
  if (v === null || v === undefined) return '-'
  return `${v.toFixed(places)}%`
}

export function num(v: number | null | undefined): string {
  if (v === null || v === undefined) return '-'
  return v.toLocaleString('en-AU')
}

export function dec(v: number | null | undefined, places = 1): string {
  if (v === null || v === undefined) return '-'
  return v.toFixed(places)
}

export function points(cur: number | null | undefined, prev: number | null | undefined): string {
  if (cur === null || cur === undefined || prev === null || prev === undefined) return ''
  const d = Math.round((cur - prev) * 10) / 10
  if (d > 0) return `up ${d.toFixed(1)} pts`
  if (d < 0) return `down ${Math.abs(d).toFixed(1)} pts`
  return 'no change'
}

export function change(cur: number, prev: number): string {
  if (!prev) return cur ? 'new' : 'no change'
  const d = Math.round(((cur - prev) / prev) * 1000) / 10
  if (d > 0) return `up ${d.toFixed(1)}%`
  if (d < 0) return `down ${Math.abs(d).toFixed(1)}%`
  return 'no change'
}

export function direction(cur: number | null | undefined, prev: number | null | undefined, higherIsBetter = true): 'good' | 'bad' | 'flat' {
  if (cur === null || cur === undefined || prev === null || prev === undefined || cur === prev) return 'flat'
  const up = cur > prev
  return up === higherIsBetter ? 'good' : 'bad'
}

export function engineName(e: string): string {
  switch (e) {
    case 'chatgpt':
      return 'ChatGPT'
    case 'gemini':
      return 'Gemini'
    case 'ai_overview':
      return 'Google AI Overview'
    default:
      return e
  }
}

export function day(s: string | null | undefined): string {
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleDateString('en-AU', { day: 'numeric', month: 'short', year: 'numeric' })
}

export function when(s: string | null | undefined): string {
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const mins = Math.round((Date.now() - d.getTime()) / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins} min ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours} h ago`
  return day(s)
}

// dateRange returns the last n days as the api wants them (YYYY-MM-DD, ending
// yesterday, because today is always incomplete).
export function dateRange(days: number): { from: string; to: string } {
  const to = new Date()
  to.setUTCDate(to.getUTCDate() - 1)
  const from = new Date(to)
  from.setUTCDate(from.getUTCDate() - (days - 1))
  const iso = (d: Date) => d.toISOString().slice(0, 10)
  return { from: iso(from), to: iso(to) }
}

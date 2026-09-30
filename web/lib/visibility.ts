// One line for the whole brand from the api's per-engine daily points: each day's
// engines weighted by how many answers ran. Today and Performance both draw it, so it
// lives here and they cannot disagree.
export type VisibilityPoint = { day: string; engine: string; visibility: number | null; answers: number }

export function dailyVisibility(series: VisibilityPoint[]): { day: string; value: number }[] {
  const byDay = new Map<string, { sum: number; n: number }>()
  for (const p of series) {
    if (p.visibility === null || p.answers === 0) continue
    const acc = byDay.get(p.day) ?? { sum: 0, n: 0 }
    acc.sum += p.visibility * p.answers
    acc.n += p.answers
    byDay.set(p.day, acc)
  }
  return [...byDay.entries()].map(([day, a]) => ({ day, value: Math.round((a.sum / a.n) * 10) / 10 }))
}

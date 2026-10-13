import { describe, expect, it } from 'vitest'

import { dailyVisibility, type VisibilityPoint } from './visibility'

const point = (day: string, engine: string, visibility: number | null, answers: number): VisibilityPoint => ({
  day,
  engine,
  visibility,
  answers,
})

describe('dailyVisibility', () => {
  it('weights each engine by how many answers it ran', () => {
    // 10 answers at 50% and 30 at 20%: 11 of 40 answers mention the brand, 27.5%,
    // not the 35% a plain average of the two engines would show.
    const out = dailyVisibility([point('2026-10-01', 'chatgpt', 50, 10), point('2026-10-01', 'gemini', 20, 30)])
    expect(out).toEqual([{ day: '2026-10-01', value: 27.5 }])
  })

  it('rounds to one decimal place', () => {
    const out = dailyVisibility([point('2026-10-01', 'chatgpt', 100, 1), point('2026-10-01', 'gemini', 0, 2)])
    expect(out).toEqual([{ day: '2026-10-01', value: 33.3 }])
  })

  it('leaves out engines with no measurement rather than counting them as zero', () => {
    const out = dailyVisibility([
      point('2026-10-01', 'chatgpt', 40, 10),
      point('2026-10-01', 'gemini', null, 10),
      point('2026-10-01', 'ai_overview', 90, 0),
    ])
    expect(out).toEqual([{ day: '2026-10-01', value: 40 }])
  })

  it('leaves out a day with nothing measured instead of drawing it at zero or NaN', () => {
    const out = dailyVisibility([
      point('2026-10-01', 'chatgpt', 40, 10),
      point('2026-10-02', 'chatgpt', null, 10),
      point('2026-10-02', 'gemini', 30, 0),
      point('2026-10-03', 'chatgpt', 60, 10),
    ])
    expect(out).toEqual([
      { day: '2026-10-01', value: 40 },
      { day: '2026-10-03', value: 60 },
    ])
  })

  it('returns nothing for no data', () => {
    expect(dailyVisibility([])).toEqual([])
  })
})

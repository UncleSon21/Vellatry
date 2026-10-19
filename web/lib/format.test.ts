import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { change, dateRange, day, dec, direction, engineName, num, pct, points, thisMonth, when } from './format'

describe('missing values', () => {
  it('show a dash, never "null", "undefined" or "NaN"', () => {
    for (const f of [pct, num, dec, day, when]) {
      expect(f(null)).toBe('-')
      expect(f(undefined)).toBe('-')
    }
    expect(day('')).toBe('-')
  })
})

describe('numbers', () => {
  it('read the Australian way', () => {
    expect(pct(41.04)).toBe('41.0%')
    expect(pct(0)).toBe('0.0%')
    expect(num(1234567)).toBe('1,234,567')
    expect(num(0)).toBe('0')
    expect(dec(3.14159, 2)).toBe('3.14')
  })
})

describe('points', () => {
  it('says which way a percentage moved, in points', () => {
    expect(points(41, 35.5)).toBe('up 5.5 pts')
    expect(points(35.5, 41)).toBe('down 5.5 pts')
  })

  it('calls a move too small to show "no change", not "up 0.0 pts"', () => {
    expect(points(41.04, 41)).toBe('no change')
    expect(points(0.1 + 0.2, 0.3)).toBe('no change')
  })

  it('says nothing when either side is missing', () => {
    expect(points(41, null)).toBe('')
    expect(points(undefined, 41)).toBe('')
  })
})

describe('change', () => {
  it('says which way a count moved, in percent', () => {
    expect(change(120, 100)).toBe('up 20.0%')
    expect(change(80, 100)).toBe('down 20.0%')
    expect(change(100, 100)).toBe('no change')
  })

  it('calls growth from nothing "new" instead of dividing by zero', () => {
    expect(change(5, 0)).toBe('new')
    expect(change(0, 0)).toBe('no change')
  })
})

describe('direction', () => {
  it('is good when a higher-is-better figure rises', () => {
    expect(direction(41, 35)).toBe('good')
    expect(direction(35, 41)).toBe('bad')
  })

  it('is good when a lower-is-better figure, like a search position, falls', () => {
    expect(direction(3, 8, false)).toBe('good')
    expect(direction(8, 3, false)).toBe('bad')
  })

  it('is flat when nothing moved or a side is missing', () => {
    expect(direction(41, 41)).toBe('flat')
    expect(direction(41, null)).toBe('flat')
  })
})

describe('engineName', () => {
  it('names the engines the way people know them', () => {
    expect(engineName('chatgpt')).toBe('ChatGPT')
    expect(engineName('gemini')).toBe('Gemini')
    expect(engineName('ai_overview')).toBe('Google AI Overview')
  })

  it('shows an engine it does not know by its own name', () => {
    expect(engineName('perplexity')).toBe('perplexity')
  })
})

describe('dates', () => {
  it('show the day an Australian reader would call it', () => {
    expect(day('2026-10-05')).toBe('5 Oct 2026')
    // 8pm UTC on the 5th is already the morning of the 6th in Sydney.
    expect(day('2026-10-05T20:00:00Z')).toBe('6 Oct 2026')
  })

  it('show something the api sent that is not a date as it came, instead of "Invalid Date"', () => {
    expect(day('last week')).toBe('last week')
    expect(when('soon')).toBe('soon')
  })
})

describe('when', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-14T03:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads recent times relative to now, then as a date', () => {
    expect(when('2026-10-14T02:59:40Z')).toBe('just now')
    expect(when('2026-10-14T02:55:00Z')).toBe('5 min ago')
    expect(when('2026-10-14T00:00:00Z')).toBe('3 h ago')
    expect(when('2026-10-10T03:00:00Z')).toBe('10 Oct 2026')
  })
})

describe('dateRange', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('ends yesterday, because today is never complete', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-14T03:00:00Z'))
    expect(dateRange(7)).toEqual({ from: '2026-10-07', to: '2026-10-13' })
    expect(dateRange(1)).toEqual({ from: '2026-10-13', to: '2026-10-13' })
  })

  it('crosses month and year boundaries', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-02T10:00:00Z'))
    expect(dateRange(28)).toEqual({ from: '2025-12-05', to: '2026-01-01' })
  })
})

describe('thisMonth', () => {
  it('is the month in the format the api accepts', () => {
    // The api parses it with Go's "2006-01": a day on the end is refused.
    expect(thisMonth()).toMatch(/^\d{4}-(0[1-9]|1[0-2])$/)
  })

  it('is the current UTC month', () => {
    const d = new Date()
    expect(thisMonth()).toBe(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`)
  })
})

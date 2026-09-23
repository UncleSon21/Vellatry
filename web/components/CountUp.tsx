'use client'

import { useEffect, useRef, useState } from 'react'

// The one stateful exception in the motion rules: a count-up depends on scroll
// position, which CSS scroll-timelines can't read back into rendered text. Reduced
// motion means the number just appears at its final value, not that it counts
// silently in the background.
//
// Props stay plain data (no function props): this renders from a server component,
// and functions can't cross that boundary to a client component.
export function CountUp({ to, suffix = '', locale = false, duration = 1100 }: { to: number; suffix?: string; locale?: boolean; duration?: number }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [value, setValue] = useState(0)

  useEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      setValue(to)
      return
    }
    const el = ref.current
    if (!el) return
    let raf = 0
    const io = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return
        io.disconnect()
        const start = performance.now()
        const tick = (now: number) => {
          const p = Math.min(1, (now - start) / duration)
          setValue(Math.round(to * (1 - Math.pow(1 - p, 3))))
          if (p < 1) raf = requestAnimationFrame(tick)
        }
        raf = requestAnimationFrame(tick)
      },
      { threshold: 0.4 },
    )
    io.observe(el)
    return () => {
      io.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [to, duration])

  return (
    <span ref={ref}>
      {locale ? value.toLocaleString('en-AU') : value}
      {suffix}
    </span>
  )
}

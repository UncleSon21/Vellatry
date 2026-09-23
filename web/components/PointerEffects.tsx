'use client'

import { useEffect } from 'react'

// Pointer-driven touches for the landing page. Elements marked data-tilt lean a few
// degrees toward the cursor (data-tilt="4" sets the maximum); the element marked
// data-lens tints its graph paper lime under the cursor. Both follow the pointer, which
// CSS can't read, so they live here. Touch screens and reduced motion get neither.
export function PointerEffects() {
  useEffect(() => {
    if (!window.matchMedia('(hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference)').matches) return
    const ac = new AbortController()
    const opts = { signal: ac.signal }

    document.querySelectorAll<HTMLElement>('[data-tilt]').forEach((el) => {
      const max = Number(el.dataset.tilt) || 5
      // Measured on entry, in page coordinates: the tilt itself changes the element's
      // bounding box, and measuring every move would feed that back into the angle.
      let box = { left: 0, top: 0, width: 1, height: 1 }
      el.addEventListener(
        'pointerenter',
        () => {
          const r = el.getBoundingClientRect()
          box = { left: r.left + window.scrollX, top: r.top + window.scrollY, width: r.width, height: r.height }
        },
        opts,
      )
      el.addEventListener(
        'pointermove',
        (e) => {
          const rx = -((e.pageY - box.top) / box.height - 0.5) * 2 * max
          const ry = ((e.pageX - box.left) / box.width - 0.5) * 2 * max
          if (!rx && !ry) return
          el.style.setProperty('--tilt-axis', `${rx.toFixed(3)} ${ry.toFixed(3)} 0`)
          el.style.setProperty('--tilt', `${Math.hypot(rx, ry).toFixed(2)}deg`)
        },
        opts,
      )
      el.addEventListener('pointerleave', () => el.style.setProperty('--tilt', '0deg'), opts)
    })

    const lens = document.querySelector<HTMLElement>('[data-lens]')
    if (lens) {
      lens.addEventListener(
        'pointermove',
        (e) => {
          const r = lens.getBoundingClientRect()
          lens.style.setProperty('--mx', `${e.clientX - r.left}px`)
          lens.style.setProperty('--my', `${e.clientY - r.top}px`)
          lens.style.setProperty('--lens', '1')
        },
        opts,
      )
      lens.addEventListener('pointerleave', () => lens.style.setProperty('--lens', '0'), opts)
    }

    return () => ac.abort()
  }, [])

  return null
}

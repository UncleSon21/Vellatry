'use client'

import { useEffect, useRef, useState } from 'react'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { UserButton } from '@clerk/nextjs'
import { clerkEnabled } from '@/lib/auth'
import { Mark } from '@/components/Mark'

const groups: { name: string; links: { href: string; label: string }[] }[] = [
  {
    name: 'Overview',
    links: [{ href: '/today', label: 'Today' }],
  },
  {
    name: 'AI visibility',
    links: [
      { href: '/visibility/performance', label: 'Performance' },
      { href: '/visibility/blindspots', label: 'Blindspots' },
      { href: '/visibility/sources', label: 'Sources' },
    ],
  },
  {
    name: 'Search',
    links: [{ href: '/search', label: 'Search Console' }],
  },
  {
    name: 'Work',
    links: [
      { href: '/topics', label: 'Topics' },
      { href: '/site', label: 'Site' },
      { href: '/fixes', label: 'Fixes' },
      { href: '/notebooks', label: 'Notebooks' },
    ],
  },
  {
    name: 'Out',
    links: [
      { href: '/reports', label: 'Reports' },
      { href: '/automations', label: 'Automations' },
    ],
  },
  {
    name: 'Settings',
    links: [
      { href: '/settings/brand', label: 'Brand' },
      { href: '/settings/connections', label: 'Connections' },
      { href: '/settings/account', label: 'Account' },
    ],
  },
]

// On a wide screen this is the sidebar. On a phone it is a bar with a Menu button that
// opens the same links as a sheet over the page (the CSS decides which, at 900px).
export function Nav() {
  const path = usePathname()
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setOpen(false)
      button.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  return (
    <nav className="side" data-open={open || undefined}>
      <div className="sidetop">
        <Link href="/today" className="brandmark" onClick={() => setOpen(false)}>
          <Mark size={20} />
          Vellatry
        </Link>
        <button ref={button} className="menubtn" aria-expanded={open} aria-controls="nav-links" onClick={() => setOpen(!open)}>
          {open ? 'Close' : 'Menu'}
        </button>
      </div>
      <div id="nav-links" className="navlinks">
        {groups.map((g) => (
          <div key={g.name}>
            <div className="navgroup">{g.name}</div>
            {g.links.map((l) => (
              <Link
                key={l.href}
                href={l.href}
                className="navlink"
                aria-current={path === l.href ? 'page' : undefined}
                onClick={() => setOpen(false)}
              >
                {l.label}
              </Link>
            ))}
          </div>
        ))}
        {clerkEnabled && (
          <div className="sideuser">
            <UserButton showName />
          </div>
        )}
      </div>
      {open && <div className="navscrim" aria-hidden="true" onClick={() => setOpen(false)} />}
    </nav>
  )
}

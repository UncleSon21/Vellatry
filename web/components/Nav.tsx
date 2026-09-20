'use client'

import Link from 'next/link'
import { usePathname } from 'next/navigation'

const groups: { name: string; links: { href: string; label: string }[] }[] = [
  {
    name: 'Overview',
    links: [{ href: '/', label: 'Today' }],
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
      { href: '/settings/connections', label: 'Connections' },
      { href: '/settings/account', label: 'Account' },
    ],
  },
]

export function Nav() {
  const path = usePathname()
  return (
    <nav className="side">
      <div className="brandmark">Vellatry</div>
      {groups.map((g) => (
        <div key={g.name}>
          <div className="navgroup">{g.name}</div>
          {g.links.map((l) => (
            <Link key={l.href} href={l.href} className="navlink" aria-current={path === l.href ? 'page' : undefined}>
              {l.label}
            </Link>
          ))}
        </div>
      ))}
    </nav>
  )
}

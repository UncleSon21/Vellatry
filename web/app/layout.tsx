import type { Metadata } from 'next'
import './globals.css'
import { Nav } from '@/components/Nav'
import { Ask } from '@/components/Agent'

export const metadata: Metadata = {
  title: 'Vellatry',
  description: 'Find your AI blindspots, fix them, prove it to your CMO.',
  robots: { index: false, follow: false },
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en-AU">
      <body>
        <div className="shell">
          <Nav />
          <main className="main">{children}</main>
        </div>
        <Ask />
      </body>
    </html>
  )
}

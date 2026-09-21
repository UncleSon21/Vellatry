import type { Metadata } from 'next'
import './globals.css'
import { Shell } from '@/components/Shell'

export const metadata: Metadata = {
  title: 'Vellatry',
  description: 'Find your AI blindspots, fix them, prove it to your CMO.',
  robots: { index: false, follow: false },
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en-AU">
      <body>
        <Shell>{children}</Shell>
      </body>
    </html>
  )
}

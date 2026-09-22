import { Instrument_Serif } from 'next/font/google'

// The marketing display face (landing page, sign-in). Self-hosted by next/font at build
// time, so no request goes to Google from a visitor's browser. Never used in the app's
// UI or for numbers: see .claude/skills/vellatry-design.
export const display = Instrument_Serif({ subsets: ['latin'], weight: '400', variable: '--font-display', display: 'swap' })

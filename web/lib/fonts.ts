import { Archivo } from 'next/font/google'

// One family for the whole product. Archivo is a grotesque with a width axis: the app
// and body text use it at its normal width, marketing headlines stretch it wide and
// heavy. Self-hosted by next/font at build time, so no visitor's browser calls Google.
// See .claude/skills/vellatry-design.
export const sans = Archivo({ subsets: ['latin'], axes: ['wdth'], variable: '--font-sans', display: 'swap' })

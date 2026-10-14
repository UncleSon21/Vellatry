import { defineConfig, globalIgnores } from 'eslint/config'
import nextVitals from 'eslint-config-next/core-web-vitals'
import nextTs from 'eslint-config-next/typescript'

// Next's own rules (React, hooks, accessibility, Core Web Vitals) and its TypeScript
// rules, and nothing of ours on top yet.
export default defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    // Errors in Next's config, warnings here until the twelve places the code already
    // breaks them are reviewed: setting state inside an effect (eleven) and writing a ref
    // during render (useEvents). Turn them back into errors once they are fixed.
    rules: {
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/refs': 'warn',
    },
  },
  globalIgnores(['.next/**', 'out/**', 'next-env.d.ts']),
])

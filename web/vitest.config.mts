import { defineConfig } from 'vitest/config'

// Dates are shown to Australian teams, so the tests read them in Sydney time on every
// machine, CI included. Set here rather than on the command line so it is the same on
// Windows; the test workers inherit it.
process.env.TZ = 'Australia/Sydney'

export default defineConfig({
  test: {
    include: ['**/*.test.ts'],
    exclude: ['node_modules/**', '.next/**'],
  },
})

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Next writes its own CLAUDE.md/AGENTS.md here otherwise; this repo keeps one set of
  // rules at the root.
  agentRules: false,
  // The dashboard talks to the api and nothing else; it renders no third-party scripts.
  async headers() {
    return [
      {
        source: '/:path*',
        headers: [
          { key: 'X-Content-Type-Options', value: 'nosniff' },
          { key: 'Referrer-Policy', value: 'no-referrer' },
          { key: 'X-Frame-Options', value: 'DENY' },
        ],
      },
    ]
  },
}

export default nextConfig

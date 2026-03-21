import type { Config } from 'tailwindcss'

export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        kb: {
          bg: '#0a0b0f',
          surface: '#101118',
          card: '#161720',
          'card-hover': '#1c1d2a',
          elevated: '#22243a',
          sidebar: '#0d0e14',
          border: 'rgba(255,255,255,0.06)',
          'border-active': 'rgba(255,255,255,0.14)',
        },
        status: {
          ok: '#22d68a',
          'ok-dim': 'rgba(34,214,138,0.12)',
          warn: '#f5a623',
          'warn-dim': 'rgba(245,166,35,0.12)',
          error: '#ef4056',
          'error-dim': 'rgba(239,64,86,0.12)',
          info: '#4c9aff',
          'info-dim': 'rgba(76,154,255,0.10)',
        },
      },
      fontFamily: {
        sans: ['DM Sans', 'sans-serif'],
        mono: ['JetBrains Mono', 'monospace'],
      },
    },
  },
  plugins: [],
} satisfies Config

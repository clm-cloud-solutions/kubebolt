import type { Config } from 'tailwindcss'

export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        kb: {
          bg: 'var(--kb-bg)',
          surface: 'var(--kb-surface)',
          card: 'var(--kb-card)',
          'card-hover': 'var(--kb-card-hover)',
          elevated: 'var(--kb-elevated)',
          sidebar: 'var(--kb-sidebar)',
          border: 'var(--kb-border)',
          'border-active': 'var(--kb-border-active)',
          text: {
            primary: 'var(--kb-text-primary)',
            secondary: 'var(--kb-text-secondary)',
            tertiary: 'var(--kb-text-tertiary)',
          },
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

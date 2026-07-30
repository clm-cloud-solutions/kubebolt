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
          accent: 'var(--kb-accent)',
          'accent-light': 'var(--kb-accent-light)',
          text: {
            primary: 'var(--kb-text-primary)',
            secondary: 'var(--kb-text-secondary)',
            tertiary: 'var(--kb-text-tertiary)',
          },
        },
        // Kobi brand surface — tokens consumed only by Kobi/AI surfaces
        // (chat panel, floating toggle, Ask buttons), aligned with the
        // kubebolt.io "Voltage Editorial" identity: warm black + electric
        // green as an accent, never a fill. Vars live in globals.css; the
        // rest of the app keeps the kb-* theme untouched.
        kobi: {
          bg: 'rgb(var(--kobi-bg-rgb) / <alpha-value>)',
          card: 'var(--kobi-card)',
          // Triplet form so /NN modifiers generate (table thead, copy button).
          elevated: 'rgb(var(--kobi-elevated-rgb) / <alpha-value>)',
          border: 'var(--kobi-border)',
          'border-accent': 'var(--kobi-border-accent)',
          // rgb()+<alpha-value> so opacity modifiers (/20, /40…) work —
          // with a bare var() Tailwind can't inject alpha, silently drops
          // the utility, and borders fall back to Preflight's light gray.
          accent: 'rgb(var(--kobi-accent-rgb) / <alpha-value>)',
          'accent-deep': 'var(--kobi-accent-deep)',
          'accent-light': 'var(--kobi-accent-light)',
          'accent-dim': 'var(--kobi-accent-dim)',
          'bubble-user': 'var(--kobi-bubble-user)',
          // Solid-fill green for the FAB/send controls — brighter than the
          // text accent in light mode, identical to it in dark.
          'accent-fill': 'rgb(var(--kobi-accent-fill-rgb) / <alpha-value>)',
          text: 'rgb(var(--kobi-text-rgb) / <alpha-value>)',
          'text-secondary': 'var(--kobi-text-secondary)',
          'text-tertiary': 'var(--kobi-text-tertiary)',
          'code-bg': 'var(--kobi-code-bg)',
          'code-text': 'var(--kobi-code-text)',
          ink: 'var(--kobi-ink)',
          // Sigil state strokes — theme-tiered (400 dark / 700 light).
          'sigil-static': 'var(--kobi-sigil-static)',
          'sigil-watching': 'var(--kobi-sigil-watching)',
          'sigil-investigating': 'var(--kobi-sigil-investigating)',
          'sigil-awaiting': 'var(--kobi-sigil-awaiting)',
          // Kobi-scoped status (per-theme) — the app-wide `status` palette
          // below is single-valued and stays untouched (Solo-Kobi scope).
          'st-ok': 'rgb(var(--kobi-st-ok-rgb) / <alpha-value>)',
          'st-warn': 'rgb(var(--kobi-st-warn-rgb) / <alpha-value>)',
          'st-error': 'rgb(var(--kobi-st-error-rgb) / <alpha-value>)',
          // info = "running now" (Autopilot flow) — sky tier per theme.
          'st-info': 'rgb(var(--kobi-st-info-rgb) / <alpha-value>)',
          'st-ok-dim': 'var(--kobi-st-ok-dim)',
          'st-info-dim': 'var(--kobi-st-info-dim)',
          'st-warn-dim': 'var(--kobi-st-warn-dim)',
          'st-error-dim': 'var(--kobi-st-error-dim)',
          'st-warn-line': 'var(--kobi-st-warn-line)',
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
        // Display face for Kobi-brand headings only (hub titles, chat
        // wordmark) — the site's Voltage Editorial voice. Everything else
        // stays DM Sans.
        display: ['Space Grotesk', 'DM Sans', 'sans-serif'],
      },
    },
  },
  plugins: [],
} satisfies Config

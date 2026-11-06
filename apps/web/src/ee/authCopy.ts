// Sign-in panel copy for THIS edition (open source). AuthShell reads it and
// stays byte-identical across editions: each edition says only what it ships,
// under what licence.
//
// A module of its own, not ee/registry.tsx: in the Enterprise build the
// registry imports the sign-up page, which imports AuthShell — reading the
// registry from the shell would be an import cycle.
//
// The headline is the site's home (From alert to root cause); the pillars
// keep the old spine, See. Understand. Operate., as the platform's map.

import type { KobiFaceState } from '@/components/kobi'

export interface AuthPillar {
  tag: 'See' | 'Understand' | 'Operate'
  text: string
}

// The eyebrow over the headline: the category, and what this build is.
export const authEyebrow = 'Kubernetes operations platform · open source'

export const authPillars: AuthPillar[] = [
  { tag: 'See', text: 'Every cluster: health, topology, capacity and cost' },
  { tag: 'Understand', text: 'Insights, incident history and security findings in context' },
  // The mode this edition ships: Copilot. Kobi is who; Copilot is how.
  { tag: 'Operate', text: 'Kobi Copilot proposes the fix, you approve it' },
]

export const authTagline =
  'Health, capacity, cost and security for every cluster, in one place. When something fails, Kobi, your AI SRE, hands you the root cause and the fix, and applies it once you approve.'

// The panel's console: one incident on one node, told by Kobi's face and the
// line beside it. Each edition tells it in the mode it ships: here Copilot,
// so Kobi stops and waits for the OK before it acts. The labels are the ones
// the panel header uses (PRESENCE_LABEL in copilot/useKobiPresence), so the
// login teaches the states the product will show.
export interface AuthConsoleBeat {
  /** Seconds into the loop when this beat starts. */
  at: number
  state: KobiFaceState
  label: string
  detail: string
  /** node-06: amber while the incident is open. */
  node: 'ok' | 'warn'
}

export interface AuthConsole {
  /** Who narrates: the name before the state. */
  who: string
  /** Autopilot's face: the dot orbits the head, no rings. */
  autopilot: boolean
  /** Loop length, seconds. Beats last at least 1.6 s so each line is read. */
  loop: number
  /** The beat held still under prefers-reduced-motion. */
  hold: number
  beats: AuthConsoleBeat[]
}

export const authConsole: AuthConsole = {
  who: 'Kobi',
  autopilot: false,
  loop: 12,
  hold: 2,
  beats: [
    { at: 0, state: 'watching', label: 'Watching', detail: '16 nodes healthy', node: 'ok' },
    { at: 1.6, state: 'investigating', label: 'Investigating', detail: 'node-06 · memory pressure', node: 'warn' },
    { at: 4, state: 'awaiting', label: 'Awaiting your approval', detail: 'raise memory to 768Mi · reversible', node: 'warn' },
    { at: 6.4, state: 'executing', label: 'Executing', detail: 'patch_deployment_resources', node: 'warn' },
    { at: 8, state: 'verifying', label: 'Verifying', detail: 'node-06 · pods Ready', node: 'ok' },
    { at: 9.8, state: 'watching', label: 'Watching', detail: 'node-06 · healthy again', node: 'ok' },
  ],
}

// The open-source licence, shown beside the version.
export const authLicense: string | null = 'Apache 2.0'

// Where the version pill links: this project's public releases.
export const authReleaseNotesUrl: string | null = 'https://github.com/clm-cloud-solutions/kubebolt/releases'

// A self-hosted install has no marketing site to send people back to: no link.
export const authSiteUrl: string | null = null

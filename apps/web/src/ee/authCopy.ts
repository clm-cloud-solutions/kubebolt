// Sign-in panel copy for THIS edition (open source). AuthShell reads it and
// stays byte-identical across editions: each edition says only what it ships,
// under what licence.
//
// A module of its own, not ee/registry.tsx: in the Enterprise build the
// registry imports the sign-up page, which imports AuthShell — reading the
// registry from the shell would be an import cycle.
//
// The spine is the site's: See. Understand. Operate.

export interface AuthPillar {
  tag: 'See' | 'Understand' | 'Operate'
  text: string
}

export const authPillars: AuthPillar[] = [
  { tag: 'See', text: 'Every cluster: health, topology, capacity and cost' },
  { tag: 'Understand', text: 'Insights, incident history and security findings in context' },
  { tag: 'Operate', text: 'Act from the same screen — Kobi proposes, you approve' },
]

export const authTagline =
  'One place for every cluster — health, topology, cost and security. Kobi, your AI copilot, reasons over that same context and proposes the fix you approve.'

// The open-source licence, shown beside the version.
export const authLicense: string | null = 'Apache 2.0'

// Where the version pill links: this project's public releases.
export const authReleaseNotesUrl: string | null = 'https://github.com/clm-cloud-solutions/kubebolt/releases'

// A self-hosted install has no marketing site to send people back to: no link.
export const authSiteUrl: string | null = null

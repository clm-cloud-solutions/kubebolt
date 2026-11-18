// Links into KubeBolt's published documentation (kubebolt.io). The web used
// to point at guides in the GitHub repository. Pages live
// at /docs/<slug>/ — with the trailing slash, which the bare path redirects to.
export const SITE_DOCS = 'https://kubebolt.io/docs/'

export function siteDoc(slug: string, anchor?: string): string {
  return `${SITE_DOCS}${slug}/${anchor ? `#${anchor}` : ''}`
}

// The remote_write guide stays in the repository until the site carries what
// it does (prometheus.yml snippets, retry_on_http_429, the limits table, the
// self-metrics queries): the site's Metrics page covers a fraction of it.
export const PROMETHEUS_GUIDE = 'https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md'

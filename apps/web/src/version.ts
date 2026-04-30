// Product version string shown in the UI chrome (sidebar header,
// login page footer). Update here at release time — the release
// workflow should keep this in sync with:
//   - apps/web/package.json "version"
//   - deploy/helm/kubebolt/Chart.yaml "appVersion"
//   - the git tag
//
// Single source of truth so refactoring the display doesn't require
// touching every component that renders the version.
export const VERSION = '1.6.0'

# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Email **hello@kubebolt.io** with the subject line **`[security]`** and include:

- a description of the issue and its potential impact;
- steps to reproduce (requests, payloads, configuration, conditions);
- the affected version of KubeBolt or the agent, and how it is deployed;
- optionally, the name or handle you'd like credited after the fix.

If you'd rather encrypt the report, write first and ask for our current PGP
key.

## What to expect

| | |
|---|---|
| Acknowledgement | within 72 hours |
| Triage and validation | within 7 days |
| Fix | by severity: critical ≤ 14 days, high ≤ 30 days, medium ≤ 90 days |
| Disclosure | coordinated with you after the fix, within 90 days of the report |

Published advisories:
<https://github.com/clm-cloud-solutions/kubebolt/security/advisories>.

## Supported versions

Security fixes ship in the **latest release** of the current line, for both
KubeBolt and `kubebolt-agent` (which has its own release line). We don't
backport to earlier lines: if you run an older version, the fix path is to
upgrade. KubeBolt Cloud is patched by us.

## Scope

In scope for this repository: the code here and the official artifacts built
from it — container images on GHCR, the Helm charts, release binaries,
Homebrew and krew packages.

Please don't test against clusters or installations you don't own. Denial of
service, reports from automated scanners without a demonstrated impact, and
known CVEs in third-party dependencies that are waiting on an upstream fix are
out of scope.

The full policy — including KubeBolt Cloud, safe harbour and rules of
engagement — is at <https://kubebolt.io/en/security>.

## Supply-chain measures

Every release is built by GitHub Actions from a tag and:

- signs the container images and Helm charts with Cosign;
- scans third-party images before building and our own images by digest
  after pushing, with Trivy (a critical or high vulnerability that has a fix blocks the release);
- attaches CycloneDX SBOMs for each image to the GitHub release.

Pull requests are scanned with Trivy and the codebase with CodeQL.

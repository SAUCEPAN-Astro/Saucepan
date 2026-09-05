# Security Policy

## Scope

Saucepan is a non-deployed design and reference repository. It does not offer
a hosted API or operate a public telescope network. This policy covers
security issues in the source, documentation, test fixtures, and reference
services in this repository. It does not make claims about external systems
or deployment environments.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: the **"Report a vulnerability"**
button on this repository's
[Security tab](https://github.com/SAUCEPAN-Astro/Saucepan/security)
or the [direct advisory form](https://github.com/SAUCEPAN-Astro/Saucepan/security/advisories/new).

There is no security email address. If private reporting is unavailable, open
a minimal public issue asking the maintainer to enable private reporting, with
no exploit details. Do not post secrets, credentials, or an unfixed remote
RCE, authentication bypass, or telescope-control finding in a public issue.

Please include privately, when possible:

- affected component;
- reproduction steps or a proof of concept without live secrets; and
- impact, such as authentication bypass, telescope control, data integrity,
  or cost abuse.

There is no bug bounty or guaranteed response time. Reports are reviewed as
maintainer time permits.

## Design security boundaries

The reference design treats the following as security boundaries:

- fail-closed authentication and signing secrets;
- authenticated, authorised MQTT command paths;
- object bytes landing in the short-lived R2 buffer rather than on the task
  service; and
- a compromised pier host as a separate trust boundary.

These are design requirements, not a statement that a production deployment
currently exists. See the architecture documents in the repository for the
supporting rationale.

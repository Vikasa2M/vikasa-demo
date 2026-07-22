# Security Policy

This is a demonstration project, but security reports are welcome. Please
report suspected vulnerabilities **privately** rather than in a public issue:

- Preferred: GitHub's **private vulnerability reporting** — the *Report a
  vulnerability* button under this repository's **Security** tab
  ([open a report](https://github.com/Vikasa2M/vikasa-demo/security/advisories/new)).
- Alternatively, open a minimal public issue that contains **no** exploit
  details and ask a maintainer to follow up privately.

Please do not include working exploit details in public issues or pull requests.

## Not a vulnerability

This demo intentionally ships **non-secret, local-only credentials** for its
self-contained docker-compose stack — most visibly the read-only ClickHouse
user (`ai_readonly` / `vikasa-ai`, provisioned with `readonly=1`). These are
fixed by design so the stack runs on a laptop with no setup; they are not
secrets and grant no access beyond a local demo database. Reports that these
hardcoded demo credentials are a vulnerability will be closed as intended
behavior.

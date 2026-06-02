# Security Policy

## Supported versions

`wyk` is pre-1.0 and ships fixes on the latest release only. Always run a
recent version (`wyk update`); older tags are not patched.

## Reporting a vulnerability

Please report security issues **privately**, not through public issues.

- Preferred: open a [private security advisory](https://github.com/raylytics/would-you-kindly/security/advisories/new)
  on this repository (GitHub → Security → Report a vulnerability).
- Include a description, reproduction steps, affected version
  (`wyk --version`), and the impact you observed.

You can expect an acknowledgement within a few days. Once a fix is
released, we're happy to credit you in the changelog unless you'd rather
stay anonymous.

## Scope notes

`wyk` is a local developer tool: it shells out to `bd`, `git`, and `go`,
and reads/writes files under your home and repo directories. It has no
network-service surface of its own (the only outbound call is an
opt-in GitHub release check for `wyk update`). Reports about local-only
behavior that requires an attacker to already control your shell,
`$PATH`, or repo contents are lower priority, but still welcome.

# Security Policy

Shiplino runs on developers' machines and processes sensitive data (prompts, commands, file paths). We take security reports seriously.

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub: **Security → Report a vulnerability** on this repository (GitHub private vulnerability reporting).

Please include:
- affected version and OS
- steps to reproduce
- impact (what an attacker can do)

## What to expect

- Acknowledgement within 3 business days.
- An initial assessment within 7 days.
- A fix and a coordinated disclosure date agreed with you. We credit reporters unless you prefer otherwise.

## Supported versions

Until 1.0, only the latest release gets security fixes.

## Scope

In scope: the `shiplino` binary (CLI, hook shim, daemon), local API, installer scripts, SDKs and plugins in this repository.
Especially interesting: anything that lets a website or another local user read Shiplino data or inject events, leaks of secrets past redaction, and anything that makes the hook path affect agent behavior.

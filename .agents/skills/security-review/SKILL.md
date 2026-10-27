---
name: security-review
description: "Reviews a change for application security, tenant isolation, payment integrity, secrets, and operational abuse."
---

# Security review checklist

Review the diff and trace trust boundaries.

- Authentication: PIN hashing, rate limiting, lockout/backoff, token expiry, session revocation, and least privilege.
- Authorization: tenant/store scope enforced server-side on every read/write; staff role checks; manager approval for void/refund where required.
- Input/data: validation, parameterized SQL, output encoding, upload restrictions, safe error messages, and limits against abuse.
- Payment: server-side amount calculation, provider signature verification, idempotency, replay/out-of-order events, no client-trusted paid status.
- QR: cryptographically secure table token, server validation, rotation/revocation, no reliance on a guessed table number.
- Privacy: minimal customer data, safe logs, retention policy, no secrets in errors or logs.
- Database: constraints, transactions, tenant isolation/RLS where applicable, least-privilege credentials.
- Operations: secret rotation, environment separation, backups, restore test, rate limits, health checks, dependency review.
- Agent/tooling: no production MCP access for routine work; review MCP permissions and untrusted tool output.
- Classify findings by severity and give a concrete remediation. Do not claim a security audit is complete if only a partial diff was reviewed.

# Changelog — account

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## Unreleased

- release: job-scoped registry auth file [b76de6b]
- release: build on the self-hosted runner with podman; private-repo minutes are metered [6f8278e]


## v0.7.0 — 2026-09-09



## v0.6.2 — 2026-09-08



## v0.6.1 — 2026-09-08



## v0.6.0 — 2026-09-08



## v0.5.0 — 2026-09-08



## v0.4.0 — 2026-09-08



## v0.3.0 — 2026-09-08



## v0.2.1 — 2026-09-08



## v0.2.0 — 2026-09-07



## v0.1.3 — 2026-09-07



## v0.1.2 — 2026-09-07



## v0.1.1 — 2026-09-07



## v0.1.0 — 2026-09-07



## v0.1.0 — 2026-09-07

- Ship a container image on tag: rootless-ready Dockerfile + ghcr release workflow [9c3ad5c]
- Links.GetActiveLinkByAccount [d0ca730]
- Social/Sessions/Invitations review round 2: delivery-time policy revalidation, session binding, at-least-once delivery with AckInvitations, forward migration 0007 [eeea811]
- fix: typed-nil mailer wiring panic + post-migration friendship lookup [928169d]
- fix: follow-up email-flow findings and migration safety [aa1bc7d]
- fix(security review): close P0/P1/P2 findings from 2026-09 audit [4da120b]
- feat(M3 groundwork): social graph, live sessions, invitations services + gRPC [31474c8]
- test: include social tables in integration reset [b6f0100]
- chore: social/sessions/invitations contract foundation (M3 groundwork) [09b83d8]
- feat: RequestAccountDeletion core RPC (deletion-pending stage, FR-8) [08d36f4]
- fix: SHA-256 pre-hash of bcrypt inputs (72-byte limit) across all credential paths [2897181]
- fix: embed UnimplementedRegistrationServer [77031aa]
- feat: adapter credential domain (SetAdapterCredential/VerifyAdapterCredential) [6505a41]
- feat: VerifyPasswordByID internal RPC for adapter-resolved console logins [d43c4db]
- feat: Registration RPC (adapter-delegated account creation), SMTP mailer [2f61a06]
- feat: durable invalidation events, VerifyPasswordByID, ban/unban with event emission [111e5fc]
- feat: M1 account core — identity, sessions, recovery, generic adapter links, internal gRPC contracts, web API [53ff113]


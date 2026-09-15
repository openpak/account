# Ports — account (trimmed)

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/ports.md` and governs. Last synchronised 2026-09-15.

One block per concern, nothing below 20000, nothing at or above 27000 (Photon-Reference and
Steam live there). Every service reads its listeners from `<SVC>_HTTP_ADDR`, `<SVC>_GRPC_ADDR`,
`<SVC>_METRICS_ADDR`; the values below are the defaults and the local-run convention. Each
service owns a block of ten: +0 HTTP, +1 gRPC, +2 metrics/pprof, +3..+9 spare.

## 20000 block

| Block | Service | HTTP | gRPC | metrics |
| --- | --- | --- | --- | --- |
| 20000 | `account` (core) | 20000 | 20001 | 20002 |

## Health

| Service | Endpoint |
| --- | --- |
| `account` (core) | `/healthz`, `/readyz` |

Other services' rows live in the canonical ports.md.

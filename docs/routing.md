# Region routing

Where a new room goes when federation nodes exist. The rule, the region
registry and the one resolver are in `pkg/routing` (standard library only, so
game servers copy it as `internal/routing`); the core serves the data.

## The rule

1. An explicit in-game region always wins, wherever the player is: the stack's
   own name (Photon `jp`, Among Us `na`, PlayFab `BrazilSouth`) picks the
   region. A name shared by several regions (Among Us `na`, `as`) is refined by
   the account country inside that set.
2. No choice: the account country picks the region (`CN` and `RU` take no
   nodes and route to their neighbours: Hong Kong/Taiwan then Japan/Korea;
   Finland/Baltics then Kazakhstan).
3. A region without an eligible node walks its fallback chain, which always
   ends at home (`eu-west`). Unknown names, no country, no table or any error
   mean home. Nothing ever fails towards a console.

Nodes count when they are approved and in rotation (`compliance.go`) and
announce the role the stack needs (`photon`, `photon_ping`, `impostor`,
`playfab_party`, `playfab_qos`, `turn`, `stun`, `npln_relay`). Joiners never
come through here: they go to the room's node.

## Test nodes

`SetNodeTest` flags a node. A test node is never in `ListNodes` (and so never
in the public `/api/v1/regions`), and the routing table hands it only to
accounts on the allow-list (`PutTester` by account id or sign-in email,
`ListTesters`, `DeleteTester`; admin only). Everyone else is routed as if it
did not exist. Approval, rotation and waivers apply to it as to any node.

## For game servers

HTTP on the core's HTTP listener, internal network only, header
`X-Routing-Key: $ACCOUNT_ROUTING_KEY` (a key that reads this and nothing else):

| Call | Answer |
| --- | --- |
| `GET /internal/v1/routing` | the table: regions with countries, fallbacks and every stack's names; country routes; nodes in rotation, test nodes flagged |
| `GET /internal/v1/routing/account?account_id=` or `?namespace=switch&subject=<BAAS user id>` | `{"country","tester"}`; unknown is the zero answer |
| `GET /internal/v1/routing/resolve?stack=&token=&country=&service=&key=` (+ account parameters) | one decision: `{"region","node","address","services","via"}` |

`routing.Client` fetches and caches the table (30 s, keeping the last good one)
and per-account answers (5 min), and resolves locally. Game servers configure it
with `OPENPAK_ROUTING_URL` (the core's HTTP base URL) and `OPENPAK_ROUTING_KEY`;
an unset URL is federation off and every room is home's, exactly as before.

gRPC has the same decision as `Federation.ResolveRegion` (home only).

The expected decisions are one fixture, `pkg/routing/testdata/cases.json`, run
by the Go tests here and by Photon's C# tests.

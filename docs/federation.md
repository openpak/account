# How federation works

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/docs/federation.md` and governs. Last synchronised 2026-09-15.

Someone in Australia runs a small box, registers it, and from then on rooms hosted by
Australians are served from Australia. Their friends in France join those rooms the same way
they always did. Nobody in Australia has an Australian account, an Australian friends list or
an Australian matchmaking server: there is one of each, at openpak.org, and the box in Sydney
is only where the packets go.

…

## The one idea

A room exists on exactly one matchmaking server. Two consoles pointed at two different
matchmaking servers can never see the same room, so if matchmaking were regional, a friend
abroad could never join you. Matchmaking therefore stays global, one server per title at home.

What *can* move is anything whose address the console is told **per session** rather than per
hostname, and every transport OpenPak serves already works that way:

| Transport | How the console learns where to go | So a region node runs |
| --- | --- | --- |
| Photon (Outbound) | the Master hands out a Game address per room | a Photon Game role |
| NPLN (Stardew) | the session hands out a STUN/TURN set | coturn |
| NEX | auth hands out the secure server; gameplay is peer-to-peer | nothing |

A node is a **relay tier**, nothing more. It holds no accounts, no friends, no links, no room
list of its own. If it dies, the rooms on it end and everyone re-matches somewhere else. Login,
friends and matchmaking still cross the ocean once per session; the packets of the game itself
do not.

## The parts

```
                           openpak.org (home)
        ┌───────────────────────────────────────────────────────────┐
        │  account core ── federation_nodes, node keys, node_id      │
        │  website ─────── /api/v1/regions  /api/v1/federation/announce  admin › Region nodes
        │  nx-baas ─────── "which country is this player from"       │
        │  photon ──────── Name + Master (+ home's own Game)          │
        │  stardew ─────── NPLN matchmaking + gamesync (+ home TURN)  │
        └───────────────────────────────────────────────────────────┘
                 ▲ announce, once a minute        ▲ room reports
                 │                                │
        ┌────────┴────────────────────────────────┴───────────┐
        │  node au-sydney: photon (Game role only), coturn,   │
        │                  a curl loop that announces         │
        └─────────────────────────────────────────────────────┘
```

**The core** keeps the register. A node has an id, a name, a country, the hash of its key,
an `approved` flag a person flips, the `services` it last announced, and when it was last
seen. The gRPC interceptor that used to compare one shared key now resolves *any* key to a
node id (`home` for the built-in one) and puts it in the request context. Handlers read the
caller's node from there and never from the request, which is how a session registered
through a node is stamped with it: `RegisterSession` from Sydney writes `node_id = au-sydney`
whatever the request says.

**The website** is the node's only door. `POST /api/v1/federation/announce` takes the node's
own key as a bearer and relays it to the core, so the core's gRPC never leaves the internal
network. `GET /api/v1/regions` is the public list of approved nodes seen in the last three
minutes, with their services and live player counts, and it is the one thing every
matchmaking server, the status page and the app read. The admin page mints a node's key
(shown once, the core keeps a hash) and flips approval.

**The matchmaking servers** poll that list once a minute and place rooms on it.

## The announce cycle

Once a minute the node posts what it runs:

```json
{"services":{"photon":"203.0.113.9:22002","turn":"203.0.113.9:3478","stun":"203.0.113.9:3478"}}
```

The core replaces the node's `services` and touches `last_seen_at`. A node that misses three
announces drops out of `/regions`; the matchmaking servers see that within another minute and
stop placing rooms there. Rooms already on it keep going until they end: nothing tears them
down, and nothing needs to.

A service a node does not run is left out, and home's own is used for it. A node can be
TURN-only, or Photon-only.

## Trust

The node key authenticates announces and nothing else. It cannot read an account, a friends
list or a link, and `CreateNode`, `ListAllNodes` and `SetNodeApproved` refuse any caller that
is not home.

What a node *does* hold are two shared secrets: Photon's token secret, because tokens home
minted must validate on the node, and the TURN secret, because home mints the credentials
coturn checks. With those a rogue node could mislabel rooms towards home or relay traffic it
was not sent. It could not touch anyone's account. That ceiling is why approval is a person
flipping a switch after looking, and why per-node Photon secrets are the named upgrade for
the day nodes are run by people we have not met.

-- Federation nodes: region relays that announce where they are and what they
-- run. Matchmaking stays at home; a node only ever holds room traffic.
-- The key is random and high-entropy, so sha256 is the right hash: a bcrypt
-- of it buys nothing and costs a lookup per RPC.
CREATE TABLE federation_nodes (
    id           TEXT PRIMARY KEY,                 -- slug, e.g. "au-sydney"
    name         TEXT NOT NULL,
    country      TEXT NOT NULL,                    -- ISO 3166-1 alpha-2, upper-case
    key_hash     TEXT NOT NULL UNIQUE,             -- sha256 hex of the node key
    approved     BOOLEAN NOT NULL DEFAULT false,   -- a person flips this; nobody is routed to a node we have not looked at
    services     JSONB NOT NULL DEFAULT '{}',      -- {"photon":"1.2.3.4:22002","npln_relay":"host:port",...}
    last_seen_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which node a live session was registered through. 'home' is the built-in
-- node behind ACCOUNT_INTERNAL_KEY; the core stamps it from the caller.
ALTER TABLE adapter_sessions ADD COLUMN node_id TEXT NOT NULL DEFAULT 'home';

-- A node is one region, one operator, one approval; it runs on one or more
-- servers. Each server has its own key (so one leaked box is revoked alone),
-- announces on its own, and carries the latest hardware report it sent and the
-- latest bandwidth self-test home measured. Reports are self-declared: they
-- gate rotation, nothing else. federation_nodes.services is no longer written;
-- a node's services are the union of its servers'.
CREATE TABLE federation_servers (
    id              TEXT PRIMARY KEY,                -- "<node>/<name>", e.g. "au-sydney/game1"
    node_id         TEXT NOT NULL REFERENCES federation_nodes(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    key_hash        TEXT NOT NULL UNIQUE,            -- sha256 hex of the server key
    services        JSONB NOT NULL DEFAULT '{}',     -- its roles: {"photon":"ip:port","turn":"ip:port",...}
    cores           INTEGER NOT NULL DEFAULT 0,
    ram_mb          BIGINT NOT NULL DEFAULT 0,       -- MemTotal, MiB
    disk_gb         BIGINT NOT NULL DEFAULT 0,       -- data filesystem size, GB
    disk_ssd        BOOLEAN NOT NULL DEFAULT false,  -- NVMe or SATA SSD (not rotational)
    ipv4            TEXT NOT NULL DEFAULT '',
    ipv6            TEXT NOT NULL DEFAULT '',
    port_mbps       INTEGER NOT NULL DEFAULT 0,      -- declared symmetric port speed
    observed_ip     TEXT NOT NULL DEFAULT '',        -- source address home saw on the last announce
    last_seen_at    TIMESTAMPTZ,
    down_mbps       DOUBLE PRECISION,                -- home -> server, measured by home
    down_at         TIMESTAMPTZ,
    down_started_at TIMESTAMPTZ,                     -- rate limit: one test per direction per interval
    up_mbps         DOUBLE PRECISION,                -- server -> home, measured by home
    up_at           TIMESTAMPTZ,
    up_started_at   TIMESTAMPTZ,
    rtt_ms          INTEGER,                         -- reported by the server with its upload test
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (node_id, name)
);

-- An admin may waive the hardware figures for one node (OpenPak's own test
-- boxes). A waiver needs a reason and may expire; every other rule still holds.
ALTER TABLE federation_nodes ADD COLUMN waiver_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE federation_nodes ADD COLUMN waiver_until TIMESTAMPTZ;

-- The coverage plan: where OpenPak wants nodes. Applications are only taken
-- for an open location; admins can still create a node anywhere. The code is
-- the node's canonical region ("sa-east", "oc", "us-west"...); the country
-- column stays, so everything that routes by country keeps working.
CREATE TABLE federation_locations (
    code       TEXT PRIMARY KEY,
    country    TEXT NOT NULL,                          -- ISO 3166-1 alpha-2, upper-case
    city       TEXT NOT NULL DEFAULT '',               -- metro or city, e.g. "São Paulo"
    wanted     INTEGER NOT NULL DEFAULT 1 CHECK (wanted >= 0),
    status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'full', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE federation_nodes ADD COLUMN region TEXT NOT NULL DEFAULT '';

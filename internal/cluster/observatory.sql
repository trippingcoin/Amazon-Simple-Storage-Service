CREATE TABLE IF NOT EXISTS ts_observatory_nodes (
 id text PRIMARY KEY CHECK(length(id)=24),
 seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=65536)
);
CREATE INDEX IF NOT EXISTS ts_observatory_seen ON ts_observatory_nodes(seen_at);

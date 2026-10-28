CREATE TABLE IF NOT EXISTS ts_settings (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 version integer NOT NULL CHECK(version=1), bucket text NOT NULL, region text NOT NULL,
 endpoint text NOT NULL, prefix text NOT NULL,
 max_object_bytes bigint NOT NULL CHECK(max_object_bytes>0),
 default_bytes bigint NOT NULL CHECK(default_bytes>=0), default_objects bigint NOT NULL CHECK(default_objects>=0)
);
CREATE TABLE IF NOT EXISTS ts_namespaces (
 id text PRIMARY KEY, stored_bytes bigint NOT NULL DEFAULT 0 CHECK(stored_bytes>=0),
 object_count bigint NOT NULL DEFAULT 0 CHECK(object_count>=0),
 max_bytes bigint NOT NULL CHECK(max_bytes>=0), max_objects bigint NOT NULL CHECK(max_objects>=0)
);
CREATE TABLE IF NOT EXISTS ts_users (
 id text PRIMARY KEY REFERENCES ts_namespaces(id),
 token_sha256 text UNIQUE NOT NULL, role text NOT NULL CHECK(role IN ('admin','user')),
 disabled boolean NOT NULL DEFAULT false, grants jsonb NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS ts_buckets (
 namespace text NOT NULL REFERENCES ts_namespaces(id), name text COLLATE "C" NOT NULL,
 generation text UNIQUE NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 modified_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(namespace,name)
);
CREATE TABLE IF NOT EXISTS ts_blobs (
 key text PRIMARY KEY, state text NOT NULL CHECK(state IN ('pending','live','retired','deleting')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), retired_at timestamptz
);
CREATE TABLE IF NOT EXISTS ts_objects (
 namespace text NOT NULL, bucket text COLLATE "C" NOT NULL, key text COLLATE "C" NOT NULL,
 blob text UNIQUE NOT NULL REFERENCES ts_blobs(key), size bigint NOT NULL CHECK(size>=0),
 content_type text NOT NULL, sha256 text NOT NULL, modified_at timestamptz NOT NULL,
 PRIMARY KEY(namespace,bucket,key), FOREIGN KEY(namespace,bucket) REFERENCES ts_buckets(namespace,name)
);
CREATE INDEX IF NOT EXISTS ts_blobs_gc ON ts_blobs(state,created_at,retired_at);

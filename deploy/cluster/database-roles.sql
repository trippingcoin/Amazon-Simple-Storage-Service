-- Run after initialization as the database administrator. Create credentials
-- outside this file; these examples use roles without login or passwords.
CREATE ROLE triples_api NOLOGIN;
GRANT USAGE ON SCHEMA public TO triples_api;
GRANT SELECT ON ts_settings, ts_users TO triples_api;
GRANT SELECT, UPDATE ON ts_namespaces TO triples_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON ts_buckets,ts_objects TO triples_api;
GRANT SELECT, INSERT, UPDATE ON ts_blobs TO triples_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON ts_observatory_nodes TO triples_api;
-- Grant triples_api to the runtime login. Keep DDL, user-registry updates and
-- S3 DeleteObject permissions in separate operator / GC credentials.

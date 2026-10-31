# s3-failure — 2026-10-28

## Purpose

Local S3 XML 500 fixture for the real Go AWS SDK upload path. Constructed from
AWS S3 error semantics; unmodeled operations return 501. Functional S3 behavior
is tested separately against local MinIO. Orbit returned unrelated/partial
providers, so official AWS documentation and the SDK were used. The owner's
published mock was not reused: local CLI credential lookup failed (macOS Keychain).

## Execution

`postman simulation run tests/simulations/s3-failure.sim.yaml --port 19400 --output ndjson`

Set `TRIPLE_S_TEST_S3_FAILURE_ENDPOINT=http://127.0.0.1:19400/s3` alongside the
local PostgreSQL/MinIO test variables documented in `docs/horizontal-scaling.md`;
run `go run ./tests -race -tags=clusterintegration -run TestClusterS3FailureSimulation -v .`.
Stop the simulation process after testing. Do not commit raw request logs.

## Assumptions

- 2026-10-28: HEAD returns 200 only for setup; PUT/POST always return XML InternalError.
  No IAM/signature validation, actual persistence, multipart lifecycle or AWS account
  behavior is modeled. This fixture uses only fake credentials and local endpoints.
- 2026-10-28: the test uses a fresh PostgreSQL schema and switches only that schema's
  endpoint to the simulation. Existing production configuration is never used.

## Observations

<!-- begin:observations -->
- 2026-11-01: after relocating all suites, mocks and simulation into `tests/`,
  the full Go overlay run with `-race -tags=integration,clusterintegration,stress`
  passed all 119 Test-functions without skips against isolated PostgreSQL 14.15
  and MinIO. Postman CLI 1.75.0 served the relocated simulation with `--no-history`;
  its request events showed one readiness HEAD 200 and three SDK PUT attempts,
  all 500. `TestClusterS3FailureSimulation` still verified no object publication,
  zero usage and a pending ledger record. This supports unchanged caller behavior
  against this stand-in; it does not establish real AWS IAM/SigV4 behavior.
  Sources: `tests/_suites/cluster/cluster_integration_test.go`,
  `tests/mocks/s3-errors/default.js`, `tests/simulations/s3-failure.sim.yaml`.
- 2026-10-28: after adding live Observatory, the existing real-SDK failure run
  still made three PUT attempts to the sole member, all 500; no other members.
  Replica heartbeat aggregation, stale/recovery and admin revocation were tested
  separately with real local PostgreSQL/MinIO; the HTTP mock does not validate them.
- 2026-10-28: `TestClusterS3FailureSimulation` passed; the sole member served three
  PUT requests, all 500. No metadata became visible, usage stayed zero, one pending
  ledger record remained. No members were silent.
<!-- end:observations -->

## What didn't work

- 2026-10-28: `postman mock generate` wrote its files but exited 139 with
  `SecItemCopyMatching failed -50`. Local `simulation create/run` worked afterward;
  the generated sample handler was replaced with the S3-specific failure fixture.

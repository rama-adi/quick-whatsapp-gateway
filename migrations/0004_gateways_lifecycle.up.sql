-- Gateway accounting (Layer 1): add a registry lifecycle to the gateways table so
-- adding/removing a gateway is a clean, observable, data-driven operation and the
-- central router can route by gateway status + load. See docs/specs/router.md and
-- docs/plans/plan-router-impl.md (D8). The gateway remains the sole writer of WA
-- tables (golang-migrate); the frontend only introspects them.
ALTER TABLE gateways
  ADD COLUMN status        VARCHAR(16)  NOT NULL DEFAULT 'active'  -- joining|active|draining|drained|unreachable
    AFTER label,
  ADD COLUMN session_count INT UNSIGNED NOT NULL DEFAULT 0
    AFTER status,
  ADD COLUMN capacity      INT UNSIGNED NULL                       -- soft cap for placement; NULL = unbounded
    AFTER session_count;

-- The router's placement query scans for the least-loaded reachable gateway; the
-- (status, last_seen_at) index lets it filter on status and prune stale rows.
CREATE INDEX idx_gateways_status_seen ON gateways (status, last_seen_at);

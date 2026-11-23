-- name: InsertBackfillImport :exec
INSERT INTO backfill_imports
(id, session_id, organization_id, source, status, owner, heartbeat_at, lease_until, chats, messages,
 identities, groups_count, group_members, schema_fingerprint, error, created_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- FinishBackfillImport is fenced by the owner. It also accepts a row the
-- reaper already marked interrupted, so a late real outcome is recorded.
-- name: FinishBackfillImport :execrows
UPDATE backfill_imports
SET status = ?, chats = ?, messages = ?, identities = ?, groups_count = ?,
	group_members = ?, schema_fingerprint = ?, error = ?, finished_at = ?, lease_until = NULL
WHERE id = ? AND owner = ? AND status IN ('running', 'interrupted');

-- name: HeartbeatBackfillImport :execrows
UPDATE backfill_imports
SET heartbeat_at = ?, lease_until = ?
WHERE id = ? AND owner = ? AND status = 'running';

-- name: InterruptExpiredBackfillImports :execrows
UPDATE backfill_imports
SET status = 'interrupted', finished_at = sqlc.arg(now), lease_until = NULL,
	error = 'import interrupted: its API process stopped or lost its lease'
WHERE status = 'running' AND lease_until <= sqlc.arg(now);

-- name: LiveBackfillImportIDs :many
SELECT id FROM backfill_imports WHERE status = 'running' AND lease_until > ?;

-- name: LatestBackfillImportForSession :one
SELECT id, session_id, organization_id, source, status, chats, messages, identities,
	groups_count, group_members, schema_fingerprint, error, created_at, finished_at
FROM backfill_imports
WHERE session_id = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: LastSuccessfulBackfillImportCreatedAt :one
SELECT created_at
FROM backfill_imports
WHERE session_id = ? AND status = 'succeeded'
ORDER BY created_at DESC
LIMIT 1;

-- name: LiveRunningBackfillImportExists :one
SELECT EXISTS (
	SELECT 1
	FROM backfill_imports
	WHERE session_id = ? AND status = 'running' AND lease_until > ?
	LIMIT 1
);

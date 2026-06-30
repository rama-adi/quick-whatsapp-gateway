-- name: InsertBackfillImport :exec
INSERT INTO backfill_imports
(id, session_id, organization_id, source, status, chats, messages, identities,
 groups_count, group_members, schema_fingerprint, error, created_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: FinishBackfillImport :execrows
UPDATE backfill_imports
SET status = ?, chats = ?, messages = ?, identities = ?, groups_count = ?,
	group_members = ?, schema_fingerprint = ?, error = ?, finished_at = ?
WHERE id = ?;

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

-- name: RecentRunningBackfillImportExists :one
SELECT EXISTS (
	SELECT 1
	FROM backfill_imports
	WHERE session_id = ? AND status = 'running' AND created_at >= ?
	LIMIT 1
);

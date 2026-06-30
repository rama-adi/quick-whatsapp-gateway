-- name: OrganizationExists :one
SELECT EXISTS (
	SELECT 1
	FROM organization
	WHERE id = ?
	LIMIT 1
);

-- name: InsertPKIAuthority :exec
INSERT INTO pki_authorities
(id, kind, parent_authority_id, parent_kind, status, certificate_pem, certificate_fingerprint, encrypted_private_key, private_key_nonce, encryption_key_id, not_before, not_after, created_at, updated_at)
VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: LockPKIRotation :one
SELECT id FROM pki_rotation_lock WHERE id=1 FOR UPDATE;

-- name: GetActivePKIAuthorityForUpdate :one
SELECT * FROM pki_authorities WHERE kind=? AND status='active' AND not_after>? ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

-- name: GetActivePKIAuthorityForRotation :one
SELECT * FROM pki_authorities WHERE kind=? AND status='active' ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

-- name: RetireActivePKIAuthorities :execrows
UPDATE pki_authorities SET status='retiring', updated_at=? WHERE kind=? AND status='active';

-- name: MarkPKIAuthorityRetired :execrows
UPDATE pki_authorities SET status='retired', updated_at=? WHERE id=? AND status='retiring';

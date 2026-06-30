-- name: UpsertPoll :exec
INSERT INTO polls
(session_id, poll_message_id, chat_jid, name, options, selectable_count, end_time, hide_votes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	chat_jid         = VALUES(chat_jid),
	name             = VALUES(name),
	options          = VALUES(options),
	selectable_count = VALUES(selectable_count),
	end_time         = VALUES(end_time),
	hide_votes       = VALUES(hide_votes),
	updated_at       = VALUES(updated_at);

-- name: GetPollOptions :one
SELECT options
FROM polls
WHERE session_id = ? AND poll_message_id = ?;

-- name: InsertPollVote :execresult
INSERT IGNORE INTO poll_votes
(session_id, poll_message_id, voter_lid, selected_options, timestamp, raw_json)
VALUES (?, ?, ?, ?, ?, NULLIF(sqlc.arg(raw_json), ''));

-- name: ListPollVotesByPoll :many
SELECT id, session_id, poll_message_id, voter_lid, selected_options, timestamp, COALESCE(raw_json, '') AS raw_json
FROM poll_votes
WHERE session_id = ? AND poll_message_id = ?
ORDER BY id ASC;

-- name: ListDuePollRecaps :many
SELECT
  p.id, p.session_id, s.organization_id, p.poll_message_id, p.chat_jid,
  COALESCE(p.name, '') AS name, p.options, p.selectable_count,
  COALESCE(p.end_time, 0) AS end_time, p.hide_votes
FROM polls p
JOIN wa_sessions s ON s.id = p.session_id
WHERE p.end_time IS NOT NULL
  AND p.end_time > 0
  AND p.end_time <= ?
  AND p.recap_emitted_at IS NULL
ORDER BY p.end_time ASC
LIMIT ?;

-- name: MarkPollRecapEmitted :execresult
UPDATE polls
SET recap_emitted_at = ?, updated_at = ?
WHERE session_id = ? AND poll_message_id = ? AND recap_emitted_at IS NULL;

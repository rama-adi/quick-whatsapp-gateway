-- name: UpsertPoll :exec
INSERT INTO polls
(session_id, poll_message_id, chat_jid, name, options, selectable_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	chat_jid         = VALUES(chat_jid),
	name             = VALUES(name),
	options          = VALUES(options),
	selectable_count = VALUES(selectable_count),
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

package backup

import (
	"context"
	"database/sql"
	"strings"
)

// EachChat streams every chat thread without materializing the backup in memory.
// The callback runs synchronously while the rows handle is open; its first error
// aborts iteration and is returned unchanged, while scan/iteration failures are
// returned from the reader.
func (d *DB) EachChat(ctx context.Context, fn func(Chat) error) error {
	const q = `SELECT j.raw_string, j.server, c.subject, c.sort_timestamp
		FROM chat c JOIN jid j ON j._id = c.jid_row_id
		WHERE j.raw_string IS NOT NULL AND j.raw_string <> ''`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			raw, server string
			subject     sql.NullString
			sortTs      sql.NullInt64
		)
		if err := rows.Scan(&raw, &server, &subject, &sortTs); err != nil {
			return err
		}
		if err := fn(Chat{
			JID:           raw,
			Type:          chatTypeForServer(server),
			Name:          subject.String,
			LastMessageAt: sortTs.Int64,
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// EachGroup streams every group thread (chat rows on the g.us server). Subject
// is optional in WhatsApp schemas and maps to an empty string when absent; the
// callback/error lifetime matches EachChat.
func (d *DB) EachGroup(ctx context.Context, fn func(Group) error) error {
	const q = `SELECT j.raw_string, c.subject
		FROM chat c JOIN jid j ON j._id = c.jid_row_id
		WHERE j.server = 'g.us' AND j.raw_string IS NOT NULL`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			raw     string
			subject sql.NullString
		)
		if err := rows.Scan(&raw, &subject); err != nil {
			return err
		}
		if err := fn(Group{JID: raw, Subject: subject.String}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// EachGroupMember streams group participants. Absent the participants table it is
// a successful no-op so older/partial backups remain importable. Rank values are
// normalized to the gateway's member/admin/superadmin vocabulary.
func (d *DB) EachGroupMember(ctx context.Context, fn func(GroupMember) error) error {
	if !d.caps.hasTable("group_participant_user") {
		return nil
	}
	const q = `SELECT gj.raw_string, uj.raw_string, gpu.rank, gpu.label
		FROM group_participant_user gpu
		JOIN jid gj ON gj._id = gpu.group_jid_row_id
		JOIN jid uj ON uj._id = gpu.user_jid_row_id
		WHERE gj.raw_string IS NOT NULL AND uj.raw_string IS NOT NULL`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			groupJID, userJID string
			rank              sql.NullInt64
			label             sql.NullString
		)
		if err := rows.Scan(&groupJID, &userJID, &rank, &label); err != nil {
			return err
		}
		if err := fn(GroupMember{
			GroupJID: groupJID,
			LID:      userJID,
			Tag:      label.String,
			Role:     roleForRank(rank.Int64),
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func roleForRank(rank int64) string {
	switch rank {
	case 2:
		return "superadmin"
	case 1:
		return "admin"
	default:
		return "member"
	}
}

// EachIdentity streams identities seeded from the jid table (lid + phone jids),
// enriched with best-effort display names captured from message mentions. Name
// prefetch completes before row streaming so only one query owns the DB's single
// immutable connection at a time.
func (d *DB) EachIdentity(ctx context.Context, fn func(Identity) error) error {
	names, err := d.mentionNames(ctx)
	if err != nil {
		return err
	}
	const q = `SELECT raw_string, server, user FROM jid
		WHERE server IN ('lid','s.whatsapp.net') AND raw_string IS NOT NULL AND raw_string <> ''`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, server, user string
		if err := rows.Scan(&raw, &server, &user); err != nil {
			return err
		}
		id := Identity{Name: names[raw]}
		if server == "lid" {
			id.LID = raw
		} else {
			id.PhoneJID = raw
			id.Phone = user
		}
		if err := fn(id); err != nil {
			return err
		}
	}
	return rows.Err()
}

// mentionNames builds a best-effort jid -> display name map from message_mentions.
// Empty map when the table is absent.
func (d *DB) mentionNames(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if !d.caps.hasTable("message_mentions") || !d.caps.hasCol("message_mentions", "display_name") {
		return out, nil
	}
	const q = `SELECT j.raw_string, mm.display_name
		FROM message_mentions mm JOIN jid j ON j._id = mm.jid_row_id
		WHERE mm.display_name IS NOT NULL AND mm.display_name <> '' AND j.raw_string IS NOT NULL`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, name string
		if err := rows.Scan(&raw, &name); err != nil {
			return out, err
		}
		if _, ok := out[raw]; !ok {
			out[raw] = name
		}
	}
	return out, rows.Err()
}

// EachMessage streams importable messages. System/placeholder rows that carry no
// text and no media/location are skipped. Mentions are prefetched before opening
// message rows, then attached by source row id; callbacks execute synchronously
// and can stop a large import without buffering the remainder.
func (d *DB) EachMessage(ctx context.Context, fn func(Message) error) error {
	mentions, err := d.messageMentions(ctx)
	if err != nil {
		return err
	}

	q, scanInto := d.buildMessageQuery()
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		r := scanInto()
		if err := rows.Scan(r.dest...); err != nil {
			return err
		}
		msg, keep := r.toMessage()
		if !keep {
			continue
		}
		if ms := mentions[r.id.Int64]; len(ms) > 0 {
			msg.Mentions = ms
		}
		if err := fn(msg); err != nil {
			return err
		}
	}
	return rows.Err()
}

// messageMentions prefetches mentioned JIDs keyed by message_row_id.
func (d *DB) messageMentions(ctx context.Context) (map[int64][]string, error) {
	out := map[int64][]string{}
	if !d.caps.hasTable("message_mentions") {
		return out, nil
	}
	const q = `SELECT mm.message_row_id, j.raw_string
		FROM message_mentions mm JOIN jid j ON j._id = mm.jid_row_id
		WHERE j.raw_string IS NOT NULL AND j.raw_string <> ''`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rowID sql.NullInt64
			raw   string
		)
		if err := rows.Scan(&rowID, &raw); err != nil {
			return out, err
		}
		if rowID.Valid {
			out[rowID.Int64] = append(out[rowID.Int64], raw)
		}
	}
	return out, rows.Err()
}

// msgRow holds the scan destinations for one message row, with the optional
// columns nulled out when the source table/column is absent.
type msgRow struct {
	id        sql.NullInt64
	keyID     string
	fromMe    int
	msgType   int
	textData  sql.NullString
	timestamp sql.NullInt64
	chatJID   string
	senderRaw sql.NullString
	caption   sql.NullString
	mime      sql.NullString
	fileSize  sql.NullInt64
	mediaName sql.NullString
	hasMedia  int
	placeName sql.NullString
	quotedID  sql.NullString
	dest      []any
}

func (r *msgRow) toMessage() (Message, bool) {
	typ := coarseType(r.msgType)
	body := strings.TrimSpace(r.textData.String)
	if body == "" {
		body = strings.TrimSpace(r.caption.String)
	}
	if body == "" {
		body = strings.TrimSpace(r.placeName.String)
	}
	hasMedia := r.hasMedia == 1

	// Skip pure system/placeholder rows: no recognized content type and no body
	// and no media.
	if typ == "" && body == "" && !hasMedia {
		return Message{}, false
	}
	if typ == "" {
		typ = "text" // unknown code but it carries a body
	}

	m := Message{
		WAMessageID:     r.keyID,
		ChatJID:         r.chatJID,
		FromMe:          r.fromMe == 1,
		Type:            typ,
		Body:            body,
		HasMedia:        hasMedia,
		MediaMime:       r.mime.String,
		MediaSize:       r.fileSize.Int64,
		MediaName:       r.mediaName.String,
		TimestampMs:     r.timestamp.Int64,
		QuotedMessageID: r.quotedID.String,
	}
	if raw := r.senderRaw.String; raw != "" {
		if strings.HasSuffix(raw, "@lid") {
			m.SenderLID = raw
		} else {
			m.SenderJID = raw
		}
	}
	return m, m.WAMessageID != ""
}

// buildMessageQuery assembles the message SELECT, adding optional joins/columns
// only when the backup has them and substituting NULL/0 otherwise so the scan
// destination list stays fixed.
func (d *DB) buildMessageQuery() (string, func() *msgRow) {
	media := d.caps.hasTable("message_media")
	location := d.caps.hasTable("message_location")
	quoted := d.caps.hasTable("message_quoted")

	caption := nullableExpr(media && d.caps.hasCol("message_media", "media_caption"), "md.media_caption")
	mime := nullableExpr(media && d.caps.hasCol("message_media", "mime_type"), "md.mime_type")
	mediaName := nullableExpr(media && d.caps.hasCol("message_media", "media_name"), "md.media_name")
	fileSize := "NULL"
	switch {
	case media && d.caps.hasCol("message_media", "file_length"):
		fileSize = "md.file_length"
	case media && d.caps.hasCol("message_media", "file_size"):
		fileSize = "md.file_size"
	}
	hasMedia := "0"
	if media {
		hasMedia = "CASE WHEN md.message_row_id IS NOT NULL THEN 1 ELSE 0 END"
	}
	placeName := nullableExpr(location && d.caps.hasCol("message_location", "place_name"), "ml.place_name")
	quotedID := nullableExpr(quoted && d.caps.hasCol("message_quoted", "key_id"), "mq.key_id")

	var b strings.Builder
	b.WriteString("SELECT m._id, m.key_id, m.from_me, m.message_type, m.text_data, m.timestamp, ")
	b.WriteString("cj.raw_string, sj.raw_string, ")
	b.WriteString(caption + ", " + mime + ", " + fileSize + ", " + mediaName + ", " + hasMedia + ", ")
	b.WriteString(placeName + ", " + quotedID + " ")
	b.WriteString("FROM message m ")
	b.WriteString("JOIN chat c ON c._id = m.chat_row_id ")
	b.WriteString("JOIN jid cj ON cj._id = c.jid_row_id ")
	b.WriteString("LEFT JOIN jid sj ON sj._id = m.sender_jid_row_id ")
	if media {
		b.WriteString("LEFT JOIN message_media md ON md.message_row_id = m._id ")
	}
	if location {
		b.WriteString("LEFT JOIN message_location ml ON ml.message_row_id = m._id ")
	}
	if quoted {
		b.WriteString("LEFT JOIN message_quoted mq ON mq.message_row_id = m._id ")
	}
	b.WriteString("WHERE m.key_id IS NOT NULL AND m.key_id <> '' AND cj.raw_string IS NOT NULL")

	scanInto := func() *msgRow {
		r := &msgRow{}
		r.dest = []any{
			&r.id, &r.keyID, &r.fromMe, &r.msgType, &r.textData, &r.timestamp,
			&r.chatJID, &r.senderRaw,
			&r.caption, &r.mime, &r.fileSize, &r.mediaName, &r.hasMedia,
			&r.placeName, &r.quotedID,
		}
		return r
	}
	return b.String(), scanInto
}

// nullableExpr returns the column expression when present, else the SQL literal
// NULL, so the projected column count is stable regardless of schema.
func nullableExpr(present bool, expr string) string {
	if present {
		return expr
	}
	return "NULL"
}

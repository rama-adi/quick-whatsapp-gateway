package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// History sync inbox and runs. A WhatsApp history-sync notification is
// stored here before the gateway acknowledges it to the phone, so a crash
// between receipt and download never loses history. The history worker
// drains the inbox, journals history.batch events (deduplicated by their
// deterministic event ids) and advances a per-notification cursor.

// Inbox row statuses.
const (
	HistoryPending   = "pending"
	HistoryDone      = "done"
	HistoryFailed    = "failed"
	HistoryDiscarded = "discarded"
)

// Run statuses.
const (
	HistoryRunRunning   = "running"
	HistoryRunCompleted = "completed"
	HistoryRunFailed    = "failed"
)

// historyRetention bounds how long finished inbox rows and runs are kept for
// diagnostics before PruneHistory removes them.
const historyRetention = 7 * 24 * time.Hour

const historySchema = `CREATE TABLE IF NOT EXISTS history_inbox (
id INTEGER PRIMARY KEY AUTOINCREMENT,
session_id TEXT NOT NULL,
organization_id TEXT NOT NULL,
notification_id TEXT NOT NULL,
account_jid TEXT NOT NULL,
notification BLOB NOT NULL,
notified_at INTEGER NOT NULL,
cutoff_at INTEGER NOT NULL,
import_enabled INTEGER NOT NULL,
status TEXT NOT NULL,
attempts INTEGER NOT NULL DEFAULT 0,
next_attempt_at INTEGER NOT NULL DEFAULT 0,
batches_done INTEGER NOT NULL DEFAULT 0,
server_error_sent INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '',
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
UNIQUE(session_id, notification_id)
);
CREATE INDEX IF NOT EXISTS idx_history_inbox_pending ON history_inbox(status, id);
CREATE TABLE IF NOT EXISTS history_runs (
run_id TEXT PRIMARY KEY,
session_id TEXT NOT NULL,
organization_id TEXT NOT NULL,
account_jid TEXT NOT NULL,
status TEXT NOT NULL,
cutoff_at INTEGER NOT NULL,
sync_type TEXT NOT NULL DEFAULT '',
progress INTEGER NOT NULL DEFAULT -1,
chunks INTEGER NOT NULL DEFAULT 0,
batches INTEGER NOT NULL DEFAULT 0,
conversations INTEGER NOT NULL DEFAULT 0,
messages INTEGER NOT NULL DEFAULT 0,
skipped INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '',
final_emitted INTEGER NOT NULL DEFAULT 0,
started_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
ended_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_history_runs_session ON history_runs(session_id, status);`

// HistoryInboxRow is one stored history-sync notification.
type HistoryInboxRow struct {
	ID              int64
	SessionID       string
	OrganizationID  string
	NotificationID  string
	AccountJID      string
	Notification    []byte // protobuf HistorySyncNotification
	NotifiedAt      time.Time
	CutoffMs        int64
	ImportEnabled   bool
	Status          string
	Attempts        int
	NextAttemptAt   time.Time
	BatchesDone     int
	ServerErrorSent bool
	Error           string
	CreatedAt       time.Time
}

// HistoryRun is one import run: every notification for one account after a
// link, until the phone reports completion or the run goes quiet.
type HistoryRun struct {
	RunID          string
	SessionID      string
	OrganizationID string
	AccountJID     string
	Status         string
	CutoffMs       int64
	SyncType       string
	Progress       int // -1 when the phone has not reported progress
	Chunks         int
	Batches        int
	Conversations  int
	Messages       int
	Skipped        int
	Error          string
	FinalEmitted   bool
	StartedAt      time.Time
	UpdatedAt      time.Time
	EndedAt        time.Time
}

// HistoryRunDelta is what one finished notification adds to its run.
type HistoryRunDelta struct {
	RunID         string
	SyncType      string
	Progress      *int
	Batches       int
	Conversations int
	Messages      int
	Skipped       int
}

func (j *Journal) historyDB() (*sql.DB, error) {
	if j == nil || j.db == nil {
		return nil, errJournalUnavailable
	}
	return j.db, nil
}

// EnqueueHistory stores a notification. A repeated notification (same
// session and stanza id) is ignored; inserted reports whether it was new.
func (j *Journal) EnqueueHistory(ctx context.Context, row HistoryInboxRow) (inserted bool, err error) {
	db, err := j.historyDB()
	if err != nil {
		return false, err
	}
	if row.SessionID == "" || row.NotificationID == "" || len(row.Notification) == 0 {
		return false, errors.New("history notification session, id and payload are required")
	}
	now := time.Now().UnixMilli()
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO history_inbox
(session_id, organization_id, notification_id, account_jid, notification, notified_at, cutoff_at,
 import_enabled, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		row.SessionID, row.OrganizationID, row.NotificationID, row.AccountJID, row.Notification,
		row.NotifiedAt.UnixMilli(), row.CutoffMs, boolInt(row.ImportEnabled), HistoryPending, now, now)
	if err != nil {
		return false, classifySQLiteError(fmt.Errorf("enqueue history notification: %w", err))
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PendingHistory returns every pending notification in arrival order.
func (j *Journal) PendingHistory(ctx context.Context) ([]HistoryInboxRow, error) {
	db, err := j.historyDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, organization_id, notification_id, account_jid,
notification, notified_at, cutoff_at, import_enabled, status, attempts, next_attempt_at, batches_done,
server_error_sent, error, created_at FROM history_inbox WHERE status=? ORDER BY id`, HistoryPending)
	if err != nil {
		return nil, classifySQLiteError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []HistoryInboxRow
	for rows.Next() {
		var r HistoryInboxRow
		var notified, next, created int64
		var enabled, sent int
		if err := rows.Scan(&r.ID, &r.SessionID, &r.OrganizationID, &r.NotificationID, &r.AccountJID,
			&r.Notification, &notified, &r.CutoffMs, &enabled, &r.Status, &r.Attempts, &next, &r.BatchesDone,
			&sent, &r.Error, &created); err != nil {
			return nil, classifySQLiteError(err)
		}
		r.NotifiedAt, r.NextAttemptAt, r.CreatedAt = msTime(notified), msTime(next), msTime(created)
		r.ImportEnabled, r.ServerErrorSent = enabled != 0, sent != 0
		out = append(out, r)
	}
	return out, classifySQLiteError(rows.Err())
}

// HasPendingHistory reports whether a session still has notifications to
// process.
func (j *Journal) HasPendingHistory(ctx context.Context, sessionID string) (bool, error) {
	db, err := j.historyDB()
	if err != nil {
		return false, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_inbox WHERE session_id=? AND status=?`,
		sessionID, HistoryPending).Scan(&n)
	return n > 0, classifySQLiteError(err)
}

// PostponeHistory schedules another attempt. attempts is the new count.
func (j *Journal) PostponeHistory(ctx context.Context, id int64, attempts int, next time.Time, errText string) error {
	return j.historyExec(ctx, `UPDATE history_inbox SET attempts=?, next_attempt_at=?, error=?, updated_at=?
WHERE id=? AND status=?`, attempts, next.UnixMilli(), errText, time.Now().UnixMilli(), id, HistoryPending)
}

// SetHistoryCursor records how many batches of a notification are journaled.
func (j *Journal) SetHistoryCursor(ctx context.Context, id int64, batchesDone int) error {
	return j.historyExec(ctx, `UPDATE history_inbox SET batches_done=?, updated_at=? WHERE id=?`,
		batchesDone, time.Now().UnixMilli(), id)
}

// MarkHistoryServerErrorSent records that the phone was asked to re-upload,
// so the request is sent at most once per notification.
func (j *Journal) MarkHistoryServerErrorSent(ctx context.Context, id int64) error {
	return j.historyExec(ctx, `UPDATE history_inbox SET server_error_sent=1, updated_at=? WHERE id=?`,
		time.Now().UnixMilli(), id)
}

// FinishHistory ends a notification (done or failed) and, in the same
// transaction, adds its counters to its run.
func (j *Journal) FinishHistory(ctx context.Context, id int64, status, errText string, delta *HistoryRunDelta) error {
	db, err := j.historyDB()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE history_inbox SET status=?, error=?, updated_at=? WHERE id=? AND status=?`,
		status, errText, now, id, HistoryPending); err != nil {
		return classifySQLiteError(err)
	}
	if delta != nil {
		progress := -1
		if delta.Progress != nil {
			progress = *delta.Progress
		}
		if _, err = tx.ExecContext(ctx, `UPDATE history_runs SET chunks=chunks+1, batches=batches+?,
conversations=conversations+?, messages=messages+?, skipped=skipped+?, sync_type=?,
progress=CASE WHEN ?>=0 THEN ? ELSE progress END, updated_at=? WHERE run_id=?`,
			delta.Batches, delta.Conversations, delta.Messages, delta.Skipped, delta.SyncType,
			progress, progress, now, delta.RunID); err != nil {
			return classifySQLiteError(err)
		}
	}
	return classifySQLiteError(tx.Commit())
}

// DiscardHistory drops a session's pending notifications, keeping those of
// keepAccount when it is set. It returns how many were dropped.
func (j *Journal) DiscardHistory(ctx context.Context, sessionID, keepAccount, reason string) (int, error) {
	db, err := j.historyDB()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `UPDATE history_inbox SET status=?, error=?, updated_at=?
WHERE session_id=? AND status=? AND (?='' OR account_jid<>?)`,
		HistoryDiscarded, reason, time.Now().UnixMilli(), sessionID, HistoryPending, keepAccount, keepAccount)
	if err != nil {
		return 0, classifySQLiteError(err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ActiveHistoryRun returns the running run of a session's account, or nil.
func (j *Journal) ActiveHistoryRun(ctx context.Context, sessionID, accountJID string) (*HistoryRun, error) {
	runs, err := j.queryRuns(ctx, `WHERE session_id=? AND account_jid=? AND status=? ORDER BY started_at DESC LIMIT 1`,
		sessionID, accountJID, HistoryRunRunning)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// HistoryRunByID returns a run, or nil when unknown.
func (j *Journal) HistoryRunByID(ctx context.Context, runID string) (*HistoryRun, error) {
	runs, err := j.queryRuns(ctx, `WHERE run_id=?`, runID)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// CreateHistoryRun inserts a run; an existing run with the same id is kept.
func (j *Journal) CreateHistoryRun(ctx context.Context, run HistoryRun) error {
	return j.historyExec(ctx, `INSERT OR IGNORE INTO history_runs
(run_id, session_id, organization_id, account_jid, status, cutoff_at, started_at, updated_at)
VALUES (?,?,?,?,?,?,?,?)`, run.RunID, run.SessionID, run.OrganizationID, run.AccountJID, HistoryRunRunning,
		run.CutoffMs, run.StartedAt.UnixMilli(), run.StartedAt.UnixMilli())
}

// EndHistoryRun ends one running run.
func (j *Journal) EndHistoryRun(ctx context.Context, runID, status, errText string) error {
	now := time.Now().UnixMilli()
	return j.historyExec(ctx, `UPDATE history_runs SET status=?, error=?, ended_at=?, updated_at=?
WHERE run_id=? AND status=?`, status, errText, now, now, runID, HistoryRunRunning)
}

// EndHistoryRuns ends a session's running runs, except those of keepAccount
// when it is set.
func (j *Journal) EndHistoryRuns(ctx context.Context, sessionID, keepAccount, status, errText string) error {
	now := time.Now().UnixMilli()
	return j.historyExec(ctx, `UPDATE history_runs SET status=?, error=?, ended_at=?, updated_at=?
WHERE session_id=? AND status=? AND (?='' OR account_jid<>?)`,
		status, errText, now, now, sessionID, HistoryRunRunning, keepAccount, keepAccount)
}

// UnemittedHistoryRuns returns ended runs whose final history.sync event
// has not been journaled yet.
func (j *Journal) UnemittedHistoryRuns(ctx context.Context) ([]HistoryRun, error) {
	return j.queryRuns(ctx, `WHERE status<>? AND final_emitted=0 ORDER BY ended_at`, HistoryRunRunning)
}

// MarkHistoryRunEmitted records that a run's final event is journaled.
func (j *Journal) MarkHistoryRunEmitted(ctx context.Context, runID string) error {
	return j.historyExec(ctx, `UPDATE history_runs SET final_emitted=1 WHERE run_id=?`, runID)
}

// IdleHistoryRuns returns running runs without activity since before and
// without pending notifications.
func (j *Journal) IdleHistoryRuns(ctx context.Context, before time.Time) ([]HistoryRun, error) {
	return j.queryRuns(ctx, `WHERE status=? AND updated_at<? AND NOT EXISTS
(SELECT 1 FROM history_inbox i WHERE i.session_id=history_runs.session_id AND i.status=?)`,
		HistoryRunRunning, before.UnixMilli(), HistoryPending)
}

// PruneHistory deletes finished notifications and ended runs older than the
// retention window.
func (j *Journal) PruneHistory(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-historyRetention).UnixMilli()
	if err := j.historyExec(ctx, `DELETE FROM history_inbox WHERE status<>? AND updated_at<?`, HistoryPending, cutoff); err != nil {
		return err
	}
	return j.historyExec(ctx, `DELETE FROM history_runs WHERE status<>? AND final_emitted=1 AND updated_at<?`,
		HistoryRunRunning, cutoff)
}

func (j *Journal) queryRuns(ctx context.Context, where string, args ...any) ([]HistoryRun, error) {
	db, err := j.historyDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT run_id, session_id, organization_id, account_jid, status, cutoff_at,
sync_type, progress, chunks, batches, conversations, messages, skipped, error, final_emitted, started_at,
updated_at, ended_at FROM history_runs `+where, args...)
	if err != nil {
		return nil, classifySQLiteError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []HistoryRun
	for rows.Next() {
		var r HistoryRun
		var emitted int
		var started, updated, ended int64
		if err := rows.Scan(&r.RunID, &r.SessionID, &r.OrganizationID, &r.AccountJID, &r.Status, &r.CutoffMs,
			&r.SyncType, &r.Progress, &r.Chunks, &r.Batches, &r.Conversations, &r.Messages, &r.Skipped, &r.Error,
			&emitted, &started, &updated, &ended); err != nil {
			return nil, classifySQLiteError(err)
		}
		r.FinalEmitted = emitted != 0
		r.StartedAt, r.UpdatedAt, r.EndedAt = msTime(started), msTime(updated), msTime(ended)
		out = append(out, r)
	}
	return out, classifySQLiteError(rows.Err())
}

func (j *Journal) historyExec(ctx context.Context, query string, args ...any) error {
	db, err := j.historyDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		return classifySQLiteError(err)
	}
	return nil
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package historysync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
)

// Intercepted is a history-sync notification taken off the live event
// stream before the inbound pipeline sees it.
type Intercepted struct {
	SessionID       string
	OrganizationID  string
	MessageID       string // stanza id of the protocol message (the receipt target)
	AccountJID      string // phone JID of the linked account
	NotifiedAt      time.Time
	Notification    *waE2E.HistorySyncNotification
	HistorySyncDays uint32 // 0 = do not import (the blob is still downloaded)
}

// Sessions resolves a session's running client. known=false means the
// session is no longer on this gateway; a nil client means it exists but is
// not connected right now.
type Sessions interface {
	HistoryClient(sessionID string) (c Client, accountJID string, known bool)
}

// Sink journals an event for the API. It fails when the session is not
// currently owned or the journal is full.
type Sink interface {
	Publish(ctx context.Context, event domain.Event) error
}

// Store is the durable inbox and run state (the gateway journal SQLite).
type Store interface {
	EnqueueHistory(ctx context.Context, row journal.HistoryInboxRow) (bool, error)
	PendingHistory(ctx context.Context) ([]journal.HistoryInboxRow, error)
	HasPendingHistory(ctx context.Context, sessionID string) (bool, error)
	PostponeHistory(ctx context.Context, id int64, attempts int, next time.Time, errText string) error
	SetHistoryCursor(ctx context.Context, id int64, batchesDone int) error
	MarkHistoryServerErrorSent(ctx context.Context, id int64) error
	FinishHistory(ctx context.Context, id int64, status, errText string, delta *journal.HistoryRunDelta) error
	DiscardHistory(ctx context.Context, sessionID, keepAccount, reason string) (int, error)
	ActiveHistoryRun(ctx context.Context, sessionID, accountJID string) (*journal.HistoryRun, error)
	HistoryRunByID(ctx context.Context, runID string) (*journal.HistoryRun, error)
	CreateHistoryRun(ctx context.Context, run journal.HistoryRun) error
	EndHistoryRun(ctx context.Context, runID, status, errText string) error
	EndHistoryRuns(ctx context.Context, sessionID, keepAccount, status, errText string) error
	UnemittedHistoryRuns(ctx context.Context) ([]journal.HistoryRun, error)
	MarkHistoryRunEmitted(ctx context.Context, runID string) error
	IdleHistoryRuns(ctx context.Context, before time.Time) ([]journal.HistoryRun, error)
	PruneHistory(ctx context.Context, now time.Time) error
}

// Config tunes the worker. Zero values use the defaults noted per field.
type Config struct {
	// Concurrency is how many sessions are processed at once (default 1).
	Concurrency int
	// BatchInterval is the minimum gap between two batches of one session
	// (default 250ms, about 4 batches per second).
	BatchInterval time.Duration
	// IdleComplete completes a run that has had no notification for this
	// long (default 10 minutes).
	IdleComplete time.Duration
	// RetryFor is how long a failing download is retried (default 24h).
	RetryFor time.Duration
	// FullSync means the gateway asked the phone for full history at link
	// time; a run then completes when FULL (not RECENT) reaches 100%.
	FullSync bool
	// Owns reports whether this gateway currently owns the session.
	Owns func(organizationID, sessionID string) bool
	// Paused reports journal pressure; batches wait while it returns true.
	Paused func(ctx context.Context) bool
	// Now overrides the clock in tests.
	Now func() time.Time
}

const (
	waitForSession = 30 * time.Second
	maxBackoff     = 10 * time.Minute
	// emitGiveUp bounds retries of a run's final event when the session is
	// not owned anymore (for example after a logout moved it elsewhere).
	emitGiveUp = time.Hour
)

// Worker drains the history inbox: one paced pass per notification, in
// arrival order per session.
type Worker struct {
	store    Store
	sessions Sessions
	sink     Sink
	cfg      Config
	log      *slog.Logger
	wake     chan struct{}

	mu        sync.Mutex
	lastBatch map[string]time.Time
	lastPrune time.Time
}

// NewWorker builds a worker. Run starts it.
func NewWorker(store Store, sessions Sessions, sink Sink, cfg Config, log *slog.Logger) *Worker {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.BatchInterval <= 0 {
		cfg.BatchInterval = 250 * time.Millisecond
	}
	if cfg.IdleComplete <= 0 {
		cfg.IdleComplete = 10 * time.Minute
	}
	if cfg.RetryFor <= 0 {
		cfg.RetryFor = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Worker{
		store: store, sessions: sessions, sink: sink, cfg: cfg, log: log,
		wake: make(chan struct{}, 1), lastBatch: map[string]time.Time{},
	}
}

func (w *Worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Accept stores an intercepted notification and only then acknowledges it
// to the phone. A store failure leaves it unacknowledged so the phone sends
// it again.
func (w *Worker) Accept(ctx context.Context, c Client, n Intercepted) error {
	if n.Notification == nil || n.MessageID == "" || n.SessionID == "" {
		return errors.New("history notification requires session, id and payload")
	}
	raw, err := proto.Marshal(n.Notification)
	if err != nil {
		return fmt.Errorf("encode history notification: %w", err)
	}
	now := w.cfg.Now()
	notified := n.NotifiedAt
	if notified.IsZero() {
		notified = now
	}
	var cutoff int64
	if n.HistorySyncDays > 0 {
		cutoff = now.Add(-time.Duration(n.HistorySyncDays) * 24 * time.Hour).UnixMilli()
	}
	inserted, err := w.store.EnqueueHistory(ctx, journal.HistoryInboxRow{
		SessionID: n.SessionID, OrganizationID: n.OrganizationID, NotificationID: n.MessageID,
		AccountJID: n.AccountJID, Notification: raw, NotifiedAt: notified, CutoffMs: cutoff,
		ImportEnabled: n.HistorySyncDays > 0,
	})
	if err != nil {
		return fmt.Errorf("store history notification: %w", err)
	}
	w.log.InfoContext(ctx, "history notification stored",
		"session", n.SessionID, "notification", n.MessageID, "new", inserted,
		"sync_type", n.Notification.GetSyncType().String(), "chunk", n.Notification.GetChunkOrder(),
		"inline", len(n.Notification.GetInitialHistBootstrapInlinePayload()) > 0)
	if c != nil {
		if err := c.SendProtocolMessageReceipt(ctx, types.MessageID(n.MessageID), types.ReceiptTypeHistorySync); err != nil {
			w.log.WarnContext(ctx, "history notification receipt failed",
				"session", n.SessionID, "notification", n.MessageID, "err", err)
		}
	}
	w.signal()
	return nil
}

// Paired drops pending history of any other account and fails that
// account's running run: the device now belongs to accountJID.
func (w *Worker) Paired(ctx context.Context, sessionID, accountJID string) {
	const reason = "device relinked to another account"
	w.drop(ctx, sessionID, accountJID, reason)
}

// Discard drops all pending history of a session (logout or removal).
func (w *Worker) Discard(ctx context.Context, sessionID, reason string) {
	w.drop(ctx, sessionID, "", reason)
}

func (w *Worker) drop(ctx context.Context, sessionID, keepAccount, reason string) {
	if n, err := w.store.DiscardHistory(ctx, sessionID, keepAccount, reason); err != nil {
		w.log.WarnContext(ctx, "discard history notifications failed", "session", sessionID, "err", err)
	} else if n > 0 {
		w.log.InfoContext(ctx, "history notifications discarded", "session", sessionID, "count", n, "reason", reason)
	}
	if err := w.store.EndHistoryRuns(ctx, sessionID, keepAccount, journal.HistoryRunFailed, reason); err != nil {
		w.log.WarnContext(ctx, "end history runs failed", "session", sessionID, "err", err)
	}
	w.signal()
}

// Run processes the inbox until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		busy, nextDue := w.tick(ctx)
		if busy {
			continue
		}
		delay := waitForSession
		if !nextDue.IsZero() {
			delay = min(max(nextDue.Sub(w.cfg.Now()), 10*time.Millisecond), waitForSession)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// tick runs one pass. busy reports that notifications were processed (run
// again right away); nextDue is the earliest postponed attempt.
func (w *Worker) tick(ctx context.Context) (busy bool, nextDue time.Time) {
	w.emitFinals(ctx)
	w.completeIdle(ctx)
	now := w.cfg.Now()
	if now.Sub(w.lastPrune) > time.Hour {
		w.lastPrune = now
		if err := w.store.PruneHistory(ctx, now); err != nil {
			w.log.WarnContext(ctx, "prune history inbox failed", "err", err)
		}
	}
	rows, err := w.store.PendingHistory(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "read history inbox failed", "err", err)
		return false, now.Add(waitForSession)
	}
	seen := map[string]bool{}
	var due []journal.HistoryInboxRow
	for _, row := range rows {
		if seen[row.SessionID] {
			continue // a session's notifications run strictly in order
		}
		seen[row.SessionID] = true
		if row.NextAttemptAt.After(now) {
			if nextDue.IsZero() || row.NextAttemptAt.Before(nextDue) {
				nextDue = row.NextAttemptAt
			}
			continue
		}
		if len(due) < w.cfg.Concurrency {
			due = append(due, row)
		}
	}
	if len(due) == 0 {
		return false, nextDue
	}
	var wg sync.WaitGroup
	for _, row := range due {
		wg.Add(1)
		go func(row journal.HistoryInboxRow) {
			defer wg.Done()
			w.process(ctx, row)
		}(row)
	}
	wg.Wait()
	return true, nextDue
}

func (w *Worker) process(ctx context.Context, row journal.HistoryInboxRow) {
	log := w.log.With("session", row.SessionID, "notification", row.NotificationID)
	c, account, known := w.sessions.HistoryClient(row.SessionID)
	if !known {
		w.finish(ctx, row, journal.HistoryDiscarded, "session is no longer on this gateway", nil)
		return
	}
	if account != "" && row.AccountJID != "" && account != row.AccountJID {
		w.finish(ctx, row, journal.HistoryDiscarded, "device is linked to another account", nil)
		return
	}
	if c == nil || (w.cfg.Owns != nil && !w.cfg.Owns(row.OrganizationID, row.SessionID)) {
		w.postpone(ctx, row, row.Attempts, waitForSession, "waiting for the session to be connected and owned")
		return
	}
	var notif waE2E.HistorySyncNotification
	if err := proto.Unmarshal(row.Notification, &notif); err != nil {
		w.finish(ctx, row, journal.HistoryFailed, "stored notification is undecodable", nil)
		return
	}
	// Always download, even with the import off: whatsmeow stores message
	// secrets, LID mappings and push names from the blob.
	blob, err := c.DownloadHistorySync(ctx, &notif, true)
	if err != nil {
		w.downloadFailed(ctx, c, row, &notif, err)
		return
	}
	if !row.ImportEnabled {
		w.finish(ctx, row, journal.HistoryDone, "", nil)
		w.deleteMedia(ctx, c, &notif, log)
		return
	}
	run, err := w.ensureRun(ctx, row)
	if err != nil {
		w.postpone(ctx, row, row.Attempts+1, backoff(row.Attempts+1), err.Error())
		return
	}
	if run.Chunks == 0 {
		started := w.lifecycle(*run, domain.HistoryPhaseStarted)
		if err := w.publish(ctx, run.SessionID, run.OrganizationID,
			EventID(run.StartedAt, run.SessionID, run.RunID, domain.HistoryPhaseStarted), domain.EventHistorySync, started); err != nil {
			w.postpone(ctx, row, row.Attempts, waitForSession, err.Error())
			return
		}
	}
	res := Convert(Input{
		SessionID: row.SessionID, NotificationID: row.NotificationID, NotifiedAt: row.NotifiedAt,
		RunID: run.RunID, AccountJID: row.AccountJID, CutoffMs: row.CutoffMs, Blob: blob, Parse: c.ParseWebMessage,
	})
	for i := row.BatchesDone; i < len(res.Batches); i++ {
		if err := w.pace(ctx, row.SessionID); err != nil {
			return
		}
		batch := res.Batches[i]
		if err := w.publish(ctx, row.SessionID, row.OrganizationID, batch.EventID, domain.EventHistoryBatch, batch.Payload); err != nil {
			w.postpone(ctx, row, row.Attempts, waitForSession, err.Error())
			return
		}
		if err := w.store.SetHistoryCursor(ctx, row.ID, i+1); err != nil {
			log.WarnContext(ctx, "save history cursor failed", "err", err)
		}
	}
	delta := journal.HistoryRunDelta{
		RunID: run.RunID, SyncType: res.SyncType, Progress: res.Progress, Batches: len(res.Batches),
		Conversations: res.Conversations, Messages: res.Messages, Skipped: res.Skipped,
	}
	progress := w.lifecycle(applyDelta(*run, delta), domain.HistoryPhaseProgress)
	progress.ChunkOrder = res.ChunkOrder
	if err := w.publish(ctx, row.SessionID, row.OrganizationID,
		EventID(row.NotifiedAt, row.SessionID, row.NotificationID, domain.HistoryPhaseProgress),
		domain.EventHistorySync, progress); err != nil {
		w.postpone(ctx, row, row.Attempts, waitForSession, err.Error())
		return
	}
	if !w.finish(ctx, row, journal.HistoryDone, "", &delta) {
		return
	}
	log.InfoContext(ctx, "history notification imported", "run", run.RunID, "sync_type", res.SyncType,
		"chunk", res.ChunkOrder, "batches", len(res.Batches), "messages", res.Messages, "skipped", res.Skipped)
	w.deleteMedia(ctx, c, &notif, log)
	if w.completes(res) {
		pending, err := w.store.HasPendingHistory(ctx, row.SessionID)
		if err == nil && !pending {
			if err := w.store.EndHistoryRun(ctx, run.RunID, journal.HistoryRunCompleted, ""); err != nil {
				log.WarnContext(ctx, "complete history run failed", "err", err)
			}
		}
	}
}

// completes reports whether this notification ends the run: the phone said
// all recent history (or all history, with full sync) has been sent.
func (w *Worker) completes(res Result) bool {
	want := domain.HistorySyncRecent
	if w.cfg.FullSync {
		want = domain.HistorySyncFull
	}
	return res.SyncType == want && res.Progress != nil && *res.Progress >= 100
}

func (w *Worker) ensureRun(ctx context.Context, row journal.HistoryInboxRow) (*journal.HistoryRun, error) {
	run, err := w.store.ActiveHistoryRun(ctx, row.SessionID, row.AccountJID)
	if err != nil || run != nil {
		return run, err
	}
	id := RunID(row.NotifiedAt, row.SessionID, row.NotificationID)
	if err := w.store.CreateHistoryRun(ctx, journal.HistoryRun{
		RunID: id, SessionID: row.SessionID, OrganizationID: row.OrganizationID, AccountJID: row.AccountJID,
		CutoffMs: row.CutoffMs, StartedAt: w.cfg.Now(),
	}); err != nil {
		return nil, err
	}
	run, err = w.store.HistoryRunByID(ctx, id)
	if err == nil && run == nil {
		err = errors.New("history run vanished after create")
	}
	return run, err
}

func applyDelta(run journal.HistoryRun, d journal.HistoryRunDelta) journal.HistoryRun {
	run.Chunks++
	run.Batches += d.Batches
	run.Conversations += d.Conversations
	run.Messages += d.Messages
	run.Skipped += d.Skipped
	run.SyncType = d.SyncType
	if d.Progress != nil {
		run.Progress = *d.Progress
	}
	return run
}

func (w *Worker) lifecycle(run journal.HistoryRun, phase string) apitypes.HistorySyncPayload {
	p := apitypes.HistorySyncPayload{
		RunID: run.RunID, Phase: phase, AccountJID: run.AccountJID, SyncType: run.SyncType, CutoffAt: run.CutoffMs,
		Chunks: run.Chunks, Batches: run.Batches, Conversations: run.Conversations, Messages: run.Messages,
		Skipped: run.Skipped,
	}
	if run.Progress >= 0 {
		progress := run.Progress
		p.Progress = &progress
	}
	if phase == domain.HistoryPhaseFailed {
		p.Error = run.Error
	}
	return p
}

func (w *Worker) publish(ctx context.Context, sessionID, organizationID, id, typ string, payload any) error {
	return w.sink.Publish(ctx, domain.Event{
		Schema: domain.Schema, ID: id, Type: typ, Session: sessionID, Organization: organizationID,
		Timestamp: w.cfg.Now().UnixMilli(), Payload: payload,
	})
}

// pace waits while the journal is under pressure and keeps a session at
// most one batch per BatchInterval.
func (w *Worker) pace(ctx context.Context, sessionID string) error {
	for w.cfg.Paused != nil && w.cfg.Paused(ctx) {
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	w.mu.Lock()
	next := w.lastBatch[sessionID].Add(w.cfg.BatchInterval)
	w.mu.Unlock()
	if wait := time.Until(next); wait > 0 {
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
	w.mu.Lock()
	w.lastBatch[sessionID] = time.Now()
	w.mu.Unlock()
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (w *Worker) downloadFailed(ctx context.Context, c Client, row journal.HistoryInboxRow,
	notif *waE2E.HistorySyncNotification, err error) {
	log := w.log.With("session", row.SessionID, "notification", row.NotificationID)
	switch {
	case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404), errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410):
		// The blob expired on WhatsApp's servers. Ask the phone once to
		// upload it again; a re-upload arrives as a new notification.
		if !row.ServerErrorSent {
			if sendErr := c.SendHistorySyncServerErrorReceipt(ctx, types.MessageID(row.NotificationID), notif.GetMediaKey()); sendErr != nil {
				log.WarnContext(ctx, "history server-error receipt failed", "err", sendErr)
			} else if markErr := w.store.MarkHistoryServerErrorSent(ctx, row.ID); markErr != nil {
				log.WarnContext(ctx, "record history server-error receipt failed", "err", markErr)
			}
		}
		log.WarnContext(ctx, "history blob expired", "err", err)
		w.finish(ctx, row, journal.HistoryFailed, "history blob expired on WhatsApp servers", nil)
	case permanentDownloadError(err):
		log.WarnContext(ctx, "history blob unusable", "err", err)
		w.finish(ctx, row, journal.HistoryFailed, err.Error(), nil)
	case w.cfg.Now().Sub(row.CreatedAt) >= w.cfg.RetryFor:
		log.WarnContext(ctx, "history download gave up", "attempts", row.Attempts, "err", err)
		w.finish(ctx, row, journal.HistoryFailed, err.Error(), nil)
		if run, runErr := w.store.ActiveHistoryRun(ctx, row.SessionID, row.AccountJID); runErr == nil && run != nil {
			_ = w.store.EndHistoryRun(ctx, run.RunID, journal.HistoryRunFailed, "history download kept failing: "+err.Error())
		}
	default:
		attempts := row.Attempts + 1
		log.InfoContext(ctx, "history download failed; will retry", "attempts", attempts, "err", err)
		w.postpone(ctx, row, attempts, backoff(attempts), err.Error())
	}
}

func permanentDownloadError(err error) bool {
	if errors.Is(err, whatsmeow.ErrInvalidMediaHMAC) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) ||
		errors.Is(err, whatsmeow.ErrNoURLPresent) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "decompress") || strings.Contains(msg, "failed to unmarshal")
}

// backoff is 5s doubling per attempt, capped at maxBackoff.
func backoff(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

func (w *Worker) postpone(ctx context.Context, row journal.HistoryInboxRow, attempts int, delay time.Duration, reason string) {
	if err := w.store.PostponeHistory(ctx, row.ID, attempts, w.cfg.Now().Add(delay), reason); err != nil {
		w.log.WarnContext(ctx, "postpone history notification failed", "session", row.SessionID, "err", err)
	}
}

func (w *Worker) finish(ctx context.Context, row journal.HistoryInboxRow, status, reason string, delta *journal.HistoryRunDelta) bool {
	if err := w.store.FinishHistory(ctx, row.ID, status, reason, delta); err != nil {
		w.log.WarnContext(ctx, "finish history notification failed", "session", row.SessionID, "err", err)
		return false
	}
	if status == journal.HistoryDiscarded {
		w.log.InfoContext(ctx, "history notification discarded", "session", row.SessionID,
			"notification", row.NotificationID, "reason", reason)
	}
	return true
}

func (w *Worker) deleteMedia(ctx context.Context, c Client, notif *waE2E.HistorySyncNotification, log *slog.Logger) {
	if notif.GetDirectPath() == "" {
		return // inline bootstrap payload: nothing stored on the media server
	}
	if err := c.DeleteMedia(ctx, whatsmeow.MediaHistory, notif.GetDirectPath(), notif.GetFileEncSHA256(),
		notif.GetEncHandle()); err != nil {
		log.DebugContext(ctx, "delete history blob failed", "err", err)
	}
}

// emitFinals journals the completed/failed event of ended runs.
func (w *Worker) emitFinals(ctx context.Context) {
	runs, err := w.store.UnemittedHistoryRuns(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "read ended history runs failed", "err", err)
		return
	}
	for _, run := range runs {
		_, _, known := w.sessions.HistoryClient(run.SessionID)
		if known {
			phase := domain.HistoryPhaseCompleted
			if run.Status == journal.HistoryRunFailed {
				phase = domain.HistoryPhaseFailed
			}
			err := w.publish(ctx, run.SessionID, run.OrganizationID,
				EventID(run.StartedAt, run.SessionID, run.RunID, phase), domain.EventHistorySync, w.lifecycle(run, phase))
			if err != nil {
				if w.cfg.Now().Sub(run.EndedAt) < emitGiveUp {
					continue
				}
				w.log.WarnContext(ctx, "history run end event dropped", "session", run.SessionID, "run", run.RunID, "err", err)
			} else {
				w.log.InfoContext(ctx, "history run ended", "session", run.SessionID, "run", run.RunID, "phase", phase,
					"messages", run.Messages, "batches", run.Batches)
			}
		}
		if err := w.store.MarkHistoryRunEmitted(ctx, run.RunID); err != nil {
			w.log.WarnContext(ctx, "mark history run emitted failed", "run", run.RunID, "err", err)
		}
	}
}

// completeIdle completes runs that went quiet: the phone does not always
// report 100% (for example when it has little history).
func (w *Worker) completeIdle(ctx context.Context) {
	runs, err := w.store.IdleHistoryRuns(ctx, w.cfg.Now().Add(-w.cfg.IdleComplete))
	if err != nil {
		w.log.WarnContext(ctx, "read idle history runs failed", "err", err)
		return
	}
	for _, run := range runs {
		if err := w.store.EndHistoryRun(ctx, run.RunID, journal.HistoryRunCompleted, ""); err != nil {
			w.log.WarnContext(ctx, "complete idle history run failed", "run", run.RunID, "err", err)
		}
	}
	if len(runs) > 0 {
		w.emitFinals(ctx)
	}
}

package wa

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

type delayedLookupLedger struct {
	fakeLedger
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (l *delayedLookupLedger) LookupCommand(ctx context.Context, id string) (*application.CommandResultRecord, error) {
	if l.calls.Add(1) == 1 {
		close(l.entered)
		<-l.release
		return nil, nil // snapshot taken before the other request commits
	}
	return l.fakeLedger.LookupCommand(ctx, id)
}

func (f *fakeDispatcher) DispatchOp(context.Context, outbound.OpRequest) (outbound.SendResult, error) {
	f.calls++
	return outbound.SendResult{WAMessageID: f.waMessageID, Timestamp: f.ts}, f.err
}

func durableMutationCalls(a *ApplicationGatewayAdapter) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"send": func(ctx context.Context) error { _, err := a.SendMessage(ctx, textSendCommand("command")); return err },
		"message op": func(ctx context.Context) error {
			_, err := a.ExecuteOp(ctx, application.MessageOpCommand{
				CommandID: "command", OrganizationID: "org_1", SessionID: "ses_1",
				GatewayID: "gateway-1", AssignmentEpoch: 4,
				Op: application.MessageOp("delete"), ChatJID: "123@s.whatsapp.net", MessageID: "message",
			})
			return err
		},
		"block": func(ctx context.Context) error { _, err := a.SetBlocked(ctx, blockCommand("command")); return err },
		"group": func(ctx context.Context) error {
			_, err := a.MutateGroup(ctx, groupCommand(application.GroupOpCreate, "command"))
			return err
		},
		"logout": func(ctx context.Context) error { _, err := a.LogoutSession(ctx, blockCommand("command")); return err },
	}
}

func TestDurableMutationRechecksLedgerAfterAcquiringFlight(t *testing.T) {
	for name := range durableMutationCalls(nil) {
		t.Run(name, func(t *testing.T) {
			ledger := &delayedLookupLedger{entered: make(chan struct{}), release: make(chan struct{})}
			dispatcher := &fakeDispatcher{waMessageID: "wa-message"}
			controller := &fakeController{}
			adapter, live := sendTestAdapter(dispatcher, ledger, true)
			adapter.opDispatch, adapter.controller = dispatcher, controller
			invoke := durableMutationCalls(adapter)[name]
			delayed := make(chan error, 1)
			go func() { delayed <- invoke(context.Background()) }()
			<-ledger.entered
			leader := make(chan error, 1)
			ctx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
			go func() { leader <- invoke(ctx) }()
			var leaderErr error
			select {
			case leaderErr = <-leader:
				close(ledger.release)
			case <-ctx.waiting:
				close(ledger.release)
				leaderErr = <-leader
			}
			delayedErr := <-delayed
			if leaderErr != nil || delayedErr != nil {
				t.Fatalf("leader=%v delayed=%v", leaderErr, delayedErr)
			}
			if calls := dispatcher.calls + live.calls + len(controller.logouts); calls != 1 {
				t.Fatalf("side effects = %d, want 1", calls)
			}
			if len(ledger.saved) != 1 {
				t.Fatalf("ledger writes = %d, want 1", len(ledger.saved))
			}
		})
	}
}

type failingSaveLedger struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func (*failingSaveLedger) LookupCommand(context.Context, string) (*application.CommandResultRecord, error) {
	return nil, nil
}

func (l *failingSaveLedger) SaveCommandResult(context.Context, application.CommandResultRecord) error {
	close(l.entered)
	<-l.release
	return l.err
}

// Done is evaluated only after a duplicate joins the flight and begins waiting.
type observedWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestDurableMutationFollowersReceiveLedgerFailure(t *testing.T) {
	for name := range durableMutationCalls(nil) {
		t.Run(name, func(t *testing.T) {
			saveErr := errors.New("journal unavailable")
			ledger := &failingSaveLedger{entered: make(chan struct{}), release: make(chan struct{}), err: saveErr}
			dispatcher := &fakeDispatcher{waMessageID: "wa-message"}
			adapter, _ := sendTestAdapter(dispatcher, ledger, true)
			adapter.opDispatch, adapter.controller = dispatcher, &fakeController{}
			invoke := durableMutationCalls(adapter)[name]
			leader := make(chan error, 1)
			go func() { leader <- invoke(context.Background()) }()
			<-ledger.entered
			follower := make(chan error, 1)
			ctx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
			go func() { follower <- invoke(ctx) }()
			<-ctx.waiting
			close(ledger.release)
			if err := <-leader; !errors.Is(err, saveErr) {
				t.Fatalf("leader error = %v", err)
			}
			if err := <-follower; !errors.Is(err, saveErr) {
				t.Fatalf("follower error = %v", err)
			}
		})
	}
}

type canceledAfterDispatch struct{ cancel context.CancelFunc }

func (d canceledAfterDispatch) Dispatch(context.Context, domain.SendRequest) (string, int64, error) {
	d.cancel()
	return "delivered", 42, nil
}

type contextAwareLedger struct{ fakeLedger }

func (l *contextAwareLedger) SaveCommandResult(ctx context.Context, record application.CommandResultRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.fakeLedger.SaveCommandResult(ctx, record)
}

func TestSuccessfulSendCommitsAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ledger := &contextAwareLedger{}
	adapter, _ := sendTestAdapter(canceledAfterDispatch{cancel: cancel}, ledger, true)
	result, err := adapter.SendMessage(ctx, textSendCommand("command"))
	if err != nil {
		t.Fatal(err)
	}
	if result.WAMessageID != "delivered" || len(ledger.saved) != 1 {
		t.Fatalf("result=%+v saved=%+v", result, ledger.saved)
	}
	dispatcher := &fakeDispatcher{}
	adapter.dispatch = dispatcher
	if _, err := adapter.SendMessage(context.Background(), textSendCommand("command")); err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 0 {
		t.Fatal("retry repeated a completed send")
	}
}

func TestCommandFlightRejectsDifferentOwnerOrOperation(t *testing.T) {
	for _, field := range []string{"session", "organization", "operation"} {
		t.Run(field, func(t *testing.T) {
			ledger := &failingSaveLedger{entered: make(chan struct{}), release: make(chan struct{})}
			adapter, _ := sendTestAdapter(nil, ledger, true)
			adapter.controller = &fakeController{}
			leader := make(chan error, 1)
			go func() { _, err := adapter.SetBlocked(context.Background(), blockCommand("collision")); leader <- err }()
			<-ledger.entered
			command := blockCommand("collision")
			switch field {
			case "session":
				command.SessionID = "other-session"
			case "organization":
				command.OrganizationID = "other-org"
			}
			// If a regression incorrectly joins the unrelated flight, cancellation
			// returns an observable non-conflict instead of leaving this test blocked.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if field == "operation" {
				_, err = adapter.LogoutSession(ctx, command)
			} else {
				_, err = adapter.SetBlocked(ctx, command)
			}
			close(ledger.release)
			if leaderErr := <-leader; leaderErr != nil {
				t.Fatal(leaderErr)
			}
			var apiErr *domain.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict {
				t.Fatalf("collision error = %v, want conflict", err)
			}
		})
	}
}

func TestCommandReplayRejectsDifferentSession(t *testing.T) {
	ledger := &fakeLedger{stored: map[string]application.CommandResultRecord{
		"collision": {CommandID: "collision", SessionID: "other-session", Status: application.CommandSent},
	}}
	dispatcher := &fakeDispatcher{}
	adapter, _ := sendTestAdapter(dispatcher, ledger, true)
	_, err := adapter.SendMessage(context.Background(), textSendCommand("collision"))
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict {
		t.Fatalf("cross-session replay = %v, want conflict", err)
	}
	if dispatcher.calls != 0 {
		t.Fatal("cross-session collision dispatched")
	}
}

func TestCommandRetryCanJoinEarlierAssignmentEpoch(t *testing.T) {
	ledger := &failingSaveLedger{entered: make(chan struct{}), release: make(chan struct{})}
	adapter, _ := sendTestAdapter(nil, ledger, true)
	leader := make(chan error, 1)
	go func() { _, err := adapter.SetBlocked(context.Background(), blockCommand("retry")); leader <- err }()
	<-ledger.entered
	retry := blockCommand("retry")
	retry.AssignmentEpoch++
	ctx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	follower := make(chan error, 1)
	go func() { _, err := adapter.SetBlocked(ctx, retry); follower <- err }()
	<-ctx.waiting
	close(ledger.release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	if err := <-follower; err != nil {
		t.Fatalf("new epoch retry = %v", err)
	}
}

package main

import (
	"context"
	"errors"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlclient"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
)

var ErrGatewayCertificateExpired = errors.New("gateway certificate expired before renewal completed")

func renewalDeadline(expiry time.Time, renewBefore time.Duration) time.Time {
	return expiry.Add(-renewBefore)
}

// renewGatewayCertificate schedules strictly from the active certificate's
// NotAfter and the operator-configured renewal window. The old connection is
// only closed after an overlapping replacement stream has a Welcome and a
// durable heartbeat acknowledgement.
func renewGatewayCertificate(
	ctx context.Context,
	identity *gatewayidentity.Manager,
	renewBefore time.Duration,
	client *controlclient.Client,
	supervisor *controlsupervisor.Supervisor,
	expired func(),
) error {
	if identity == nil || client == nil || supervisor == nil || renewBefore <= 0 || expired == nil {
		return errors.New("gateway certificate renewal: invalid configuration")
	}
	backoff := controlsupervisor.ExponentialBackoff{}
	for attempt := uint(0); ; {
		expiry := identity.Expiry()
		if expiry.IsZero() || !time.Now().UTC().Before(expiry) {
			expired()
			return ErrGatewayCertificateExpired
		}
		if wait := time.Until(renewalDeadline(expiry, renewBefore)); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}

		attemptCtx, cancel := context.WithDeadline(ctx, expiry)
		rollover, err := client.BeginRenewal(attemptCtx, controlclient.GRPCRenewalTransport{})
		if err == nil {
			var closeProof func()
			closeProof, err = supervisor.ProveCurrentConnection(attemptCtx)
			if err == nil {
				err = rollover.Commit()
				closeProof()
			} else {
				_ = rollover.Rollback()
			}
		}
		cancel()
		if err == nil {
			attempt = 0
			continue
		}
		if !time.Now().UTC().Before(expiry) {
			expired()
			return ErrGatewayCertificateExpired
		}
		if !controlclient.Retryable(err) && !errors.Is(err, context.DeadlineExceeded) {
			timer := time.NewTimer(time.Until(expiry))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
				expired()
				return ErrGatewayCertificateExpired
			}
		}
		delay := backoff.Delay(attempt)
		if remaining := time.Until(expiry); delay > remaining {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		attempt++
	}
}

package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// EngineTargetResolver is the only route authority accepted by Client.
type EngineTargetResolver interface {
	ResolveSessionEngineTarget(context.Context, string, string) (domain.SessionEngineTarget, error)
}
type EngineDial func(context.Context, string, string) (*grpc.ClientConn, error)

// MaxEngineMessageBytes bounds one engine RPC message. The album aggregate cap
// (64 MiB) plus envelope/JSON headroom decides the value; both dial and server
// must agree so inline media payloads are never silently truncated.
const MaxEngineMessageBytes = 68 << 20

// NewEngineMTLSDial constructs a gateway-bound TLS 1.3 dialer. The callback is
// supplied by apiidentity.Manager so rotations affect new pooled connections.
func NewEngineMTLSDial(
	certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error),
	roots *x509.CertPool,
) EngineDial {
	return func(ctx context.Context, gatewayID, endpoint string) (*grpc.ClientConn, error) {
		if certificate == nil || roots == nil {
			return nil, errors.New("gateway engine mTLS is not configured")
		}
		tlsConfig := engineTLSConfig(gatewayID, certificate, roots)
		return grpc.DialContext(
			ctx,
			endpoint,
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(MaxEngineMessageBytes),
				grpc.MaxCallSendMsgSize(MaxEngineMessageBytes),
			),
		)
	}
}

// engineTLSConfig pins TLS 1.3 and verifies the gateway leaf certificate's
// SPIFFE URI against the expected gateway id instead of WebPKI names.
func engineTLSConfig(
	gatewayID string,
	certificate func(*tls.ClientHelloInfo) (*tls.Certificate, error),
	roots *x509.CertPool,
) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return certificate(nil)
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("gateway certificate missing")
			}
			leaf := state.PeerCertificates[0]
			inter := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				inter.AddCert(cert)
			}
			verifyOptions := x509.VerifyOptions{
				Roots:         roots,
				Intermediates: inter,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			if _, err := leaf.Verify(verifyOptions); err != nil {
				return err
			}
			expectedSPIFFE := "spiffe://quick-wa/gateway/" + gatewayID
			if len(leaf.URIs) != 1 || leaf.URIs[0].String() != expectedSPIFFE {
				return errors.New("gateway SPIFFE identity mismatch")
			}
			return nil
		},
	}
}

// EngineClient implements the API-facing resolved live facade. Calls reuse one
// grpc-go ClientConn per advertised endpoint; grpc-go owns reconnects.
type EngineClient struct {
	CaptureMedia func(context.Context, string, string, string, domain.SendRequest) error
	resolver     EngineTargetResolver
	dial         EngineDial
	mu           sync.Mutex
	conns        map[string]*grpc.ClientConn
	deadline     time.Duration
	sendDeadline time.Duration
	health       map[string]EngineHealth
}
type EngineHealth struct {
	GatewayID, Endpoint      string
	LastAttempt, LastSuccess time.Time
	LastError                string
}

func NewEngineClient(
	resolver EngineTargetResolver,
	dial EngineDial,
	deadline time.Duration,
	sendDeadline time.Duration,
) (*EngineClient, error) {
	if deadline <= 0 {
		return nil, errors.New("gateway engine unary deadline is required")
	}
	if sendDeadline <= 0 {
		return nil, errors.New("gateway engine send deadline is required")
	}
	return &EngineClient{
		resolver:     resolver,
		dial:         dial,
		conns:        map[string]*grpc.ClientConn{},
		deadline:     deadline,
		sendDeadline: sendDeadline,
		health:       map[string]EngineHealth{},
	}, nil
}
func (c *EngineClient) Health() []EngineHealth {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]EngineHealth, 0, len(c.health))
	for _, v := range c.health {
		out = append(out, v)
	}
	return out
}
func (c *EngineClient) record(gatewayID, endpoint string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := gatewayID + "\x00" + endpoint
	value := c.health[key]
	value.GatewayID = gatewayID
	value.Endpoint = endpoint
	value.LastAttempt = time.Now().UTC()
	if err == nil {
		value.LastSuccess = value.LastAttempt
		value.LastError = ""
	} else {
		value.LastError = err.Error()
	}
	c.health[key] = value
}
func (c *EngineClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	for k, conn := range c.conns {
		if err := conn.Close(); err != nil && first == nil {
			first = err
		}
		delete(c.conns, k)
	}
	return first
}
func (c *EngineClient) conn(ctx context.Context, gatewayID, endpoint string) (*grpc.ClientConn, error) {
	key := gatewayID + "\x00" + endpoint
	c.mu.Lock()
	if conn := c.conns[key]; conn != nil {
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()
	conn, err := c.dial(ctx, gatewayID, endpoint)
	c.record(gatewayID, endpoint, err)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if old := c.conns[key]; old != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return old, nil
	}
	c.conns[key] = conn
	c.mu.Unlock()
	return conn, nil
}
func (c *EngineClient) GetSessionState(ctx context.Context, org, session string) (application.SessionState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return application.SessionState{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.SessionState{}, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).GetSessionState(ctx, &gatewayv1.GetSessionStateRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.SessionState{}, mapEngineError(err)
	}
	state, err := sessionStatus(response.Status)
	if err != nil {
		return application.SessionState{}, err
	}
	return application.SessionState{
		OrganizationID: target.OrganizationID,
		SessionID:      target.SessionID,
		GatewayID:      target.GatewayID,
		Status:         state,
		Connected:      response.Connected,
		LoggedIn:       response.LoggedIn,
	}, nil
}
func (c *EngineClient) SetAccountPresence(
	ctx context.Context,
	org string,
	session string,
	presence application.AccountPresence,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	state := gatewayv1.AccountPresence_ACCOUNT_PRESENCE_ONLINE
	if presence == application.AccountPresenceOffline {
		state = gatewayv1.AccountPresence_ACCOUNT_PRESENCE_OFFLINE
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).SetAccountPresence(ctx, &gatewayv1.SetAccountPresenceRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       domain.NewULID(),
		State:           state,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// SendMessage dispatches one durable send command. The caller owns the stable
// CommandID (from the durable command row); this method never mints one, so a
// retried ambiguous send re-issues the same id and the gateway's ledger
// returns the original terminal result.
func (c *EngineClient) SendMessage(
	ctx context.Context,
	command application.SendCommand,
) (application.SendMessageResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	if command.CommandID == "" {
		return application.SendMessageResult{}, domain.ErrValidation("send command id is required")
	}
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, command.OrganizationID, command.SessionID)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	prepared, err := outbound.PrepareEngineMedia(ctx, command.Payload)
	if err != nil {
		return application.SendMessageResult{}, err
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return application.SendMessageResult{}, fmt.Errorf("encode send payload: %w", err)
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).SendMessage(ctx, &gatewayv1.SendMessageRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       command.CommandID,
		PayloadJson:     payload,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.SendMessageResult{}, mapEngineError(err)
	}
	if c.CaptureMedia != nil {
		if err := c.CaptureMedia(ctx, target.OrganizationID, target.SessionID, response.WaMessageId, prepared); err != nil {
			return application.SendMessageResult{}, err
		}
	}
	return application.SendMessageResult{
		MutationResult: application.MutationResult{
			CommandID: response.CommandId, OrganizationID: target.OrganizationID,
			SessionID: target.SessionID, GatewayID: target.GatewayID, AssignmentEpoch: target.AssignmentEpoch,
		},
		WAMessageID: response.WaMessageId,
		SentAt:      time.UnixMilli(response.SentAtUnixMs).UTC(),
	}, nil
}

// ExecuteOp dispatches one message sub-resource command. The caller owns the
// stable CommandID from the durable command row.
func (c *EngineClient) ExecuteOp(
	ctx context.Context,
	command application.MessageOpCommand,
) (application.MessageOpResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	if command.CommandID == "" {
		return application.MessageOpResult{}, domain.ErrValidation("op command id is required")
	}
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, command.OrganizationID, command.SessionID)
	if err != nil {
		return application.MessageOpResult{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.MessageOpResult{}, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).MessageOp(ctx, &gatewayv1.MessageOpRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       command.CommandID,
		Op:              string(command.Op),
		ChatJid:         command.ChatJID,
		SenderJid:       command.SenderJID,
		MessageId:       command.MessageID,
		Emoji:           command.Emoji,
		NewText:         command.NewText,
		Options:         command.Options,
		ToJid:           command.ToJID,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.MessageOpResult{}, mapEngineError(err)
	}
	return application.MessageOpResult{
		MutationResult: application.MutationResult{
			CommandID: response.CommandId, OrganizationID: target.OrganizationID,
			SessionID: target.SessionID, GatewayID: target.GatewayID, AssignmentEpoch: target.AssignmentEpoch,
		},
	}, nil
}

// LookupContact checks phone numbers on WhatsApp through the assigned engine.
// Quick read: unary deadline.
func (c *EngineClient) LookupContact(
	ctx context.Context,
	org string,
	session string,
	phones []string,
) ([]application.ContactLookup, error) {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return nil, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return nil, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).LookupContact(ctx, &gatewayv1.LookupContactRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		Phones:          phones,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return nil, mapEngineError(err)
	}
	out := make([]application.ContactLookup, 0, len(response.Results))
	for _, r := range response.Results {
		out = append(out, application.ContactLookup{Query: r.GetQuery(), JID: r.GetJid(), IsIn: r.GetIsOnWhatsapp()})
	}
	return out, nil
}

func (c *EngineClient) GetContactPicture(
	ctx context.Context,
	org string,
	session string,
	jid string,
) (domain.ProfilePicture, error) {
	query, conn, cancel, err := c.readQuery(ctx, org, session)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	defer cancel()
	response, rpcErr := gatewayv1.NewGatewayEngineServiceClient(conn).GetContactPicture(ctx, &gatewayv1.GetContactPictureRequest{
		Target:          sessionTargetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		Jid:             jid,
	})
	c.record(query.GatewayID, query.GRPCEndpoint, rpcErr)
	if rpcErr != nil {
		return domain.ProfilePicture{}, mapEngineError(rpcErr)
	}
	return domain.ProfilePicture{URL: response.GetUrl(), ID: response.GetId()}, nil
}

func (c *EngineClient) GetContactAbout(
	ctx context.Context,
	org string,
	session string,
	jid string,
) (string, error) {
	query, conn, cancel, err := c.readQuery(ctx, org, session)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, rpcErr := gatewayv1.NewGatewayEngineServiceClient(conn).GetContactAbout(ctx, &gatewayv1.GetContactAboutRequest{
		Target:          sessionTargetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		Jid:             jid,
	})
	c.record(query.GatewayID, query.GRPCEndpoint, rpcErr)
	if rpcErr != nil {
		return "", mapEngineError(rpcErr)
	}
	return response.GetAbout(), nil
}

// SetBlocked applies one block/unblock command. The engine mints its own stable
// command id from the request's ULID; the ledger replays it on retry.
func (c *EngineClient) SetBlocked(ctx context.Context, org, session, jid string, blocked bool) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).SetBlocked(ctx, &gatewayv1.SetBlockedRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       domain.NewULID(),
		Jid:             jid,
		Blocked:         blocked,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// MutateGroup executes one durable group command (create/settings/participants/
// leave) behind the scheduler's command id; the raw group metadata in create
// results feeds the API's own projection writes.
func (c *EngineClient) MutateGroup(
	ctx context.Context,
	command application.GroupMutationCommand,
) (application.GroupCreateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	if command.CommandID == "" {
		return application.GroupCreateResult{}, domain.ErrValidation("group command id is required")
	}
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, command.OrganizationID, command.SessionID)
	if err != nil {
		return application.GroupCreateResult{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.GroupCreateResult{}, err
	}
	client := gatewayv1.NewGatewayEngineServiceClient(conn)
	base := application.MutationResult{
		CommandID: command.CommandID, OrganizationID: target.OrganizationID,
		SessionID: target.SessionID, GatewayID: target.GatewayID, AssignmentEpoch: target.AssignmentEpoch,
	}
	targetProto := sessionTargetProto(target)
	var result application.GroupCreateResult
	switch command.Kind {
	case application.GroupOpCreate:
		response, rpcErr := client.CreateGroup(ctx, &gatewayv1.CreateGroupRequest{
			Target:          targetProto,
			AssignmentEpoch: target.AssignmentEpoch,
			CommandId:       command.CommandID,
			Name:            command.Name,
			Participants:    command.Participants,
		})
		c.record(target.GatewayID, target.GRPCEndpoint, rpcErr)
		if rpcErr != nil {
			return application.GroupCreateResult{}, mapEngineError(rpcErr)
		}
		result = application.GroupCreateResult{
			MutationOnlyResult: application.MutationOnlyResult{MutationResult: base},
			CreatedGroup:       protoGroupInfo(response.GetGroup()),
		}
	case application.GroupOpUpdateSettings:
		settings := command.Settings
		_, rpcErr := client.UpdateGroupSettings(ctx, &gatewayv1.UpdateGroupSettingsRequest{
			Target:          targetProto,
			AssignmentEpoch: target.AssignmentEpoch,
			CommandId:       command.CommandID,
			GroupJid:        command.GroupJID,
			Subject:         settings.Subject,
			Description:     settings.Description,
			Announce:        settings.Announce,
			Locked:          settings.Locked,
		})
		c.record(target.GatewayID, target.GRPCEndpoint, rpcErr)
		if rpcErr != nil {
			return application.GroupCreateResult{}, mapEngineError(rpcErr)
		}
		result = application.GroupCreateResult{MutationOnlyResult: application.MutationOnlyResult{MutationResult: base}}
	case application.GroupOpUpdateParticipants:
		action, validAction := participantChangeProto(command.Action)
		if !validAction {
			return application.GroupCreateResult{}, domain.ErrValidation("invalid participant action")
		}
		_, rpcErr := client.UpdateGroupParticipants(ctx, &gatewayv1.UpdateGroupParticipantsRequest{
			Target:          targetProto,
			AssignmentEpoch: target.AssignmentEpoch,
			CommandId:       command.CommandID,
			GroupJid:        command.GroupJID,
			Participants:    command.Participants,
			Action:          action,
		})
		c.record(target.GatewayID, target.GRPCEndpoint, rpcErr)
		if rpcErr != nil {
			return application.GroupCreateResult{}, mapEngineError(rpcErr)
		}
		result = application.GroupCreateResult{MutationOnlyResult: application.MutationOnlyResult{MutationResult: base}}
	case application.GroupOpLeave:
		_, rpcErr := client.LeaveGroup(ctx, &gatewayv1.LeaveGroupRequest{
			Target:          targetProto,
			AssignmentEpoch: target.AssignmentEpoch,
			CommandId:       command.CommandID,
			GroupJid:        command.GroupJID,
		})
		c.record(target.GatewayID, target.GRPCEndpoint, rpcErr)
		if rpcErr != nil {
			return application.GroupCreateResult{}, mapEngineError(rpcErr)
		}
		result = application.GroupCreateResult{MutationOnlyResult: application.MutationOnlyResult{MutationResult: base}}
	default:
		return application.GroupCreateResult{}, domain.ErrValidation("invalid group operation")
	}
	return result, nil
}

// GetGroupInviteLink reads (reset=false) or resets (reset=true) a group invite
// link through the assigned engine.
func (c *EngineClient) GetGroupInviteLink(
	ctx context.Context,
	org string,
	session string,
	groupJID string,
	reset bool,
) (string, error) {
	query, conn, cancel, err := c.readQuery(ctx, org, session)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, rpcErr := gatewayv1.NewGatewayEngineServiceClient(conn).GetGroupInviteLink(ctx, &gatewayv1.GetGroupInviteLinkRequest{
		Target:          sessionTargetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		GroupJid:        groupJID,
		Reset_:          reset,
	})
	c.record(query.GatewayID, query.GRPCEndpoint, rpcErr)
	if rpcErr != nil {
		return "", mapEngineError(rpcErr)
	}
	return response.GetLink(), nil
}

// JoinGroup joins a group from an invite code/link through the assigned engine.
func (c *EngineClient) JoinGroup(
	ctx context.Context,
	org string,
	session string,
	invite string,
) (string, error) {
	query, conn, cancel, err := c.readQuery(ctx, org, session)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, rpcErr := gatewayv1.NewGatewayEngineServiceClient(conn).JoinGroup(ctx, &gatewayv1.JoinGroupRequest{
		Target:          sessionTargetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		Invite:          invite,
	})
	c.record(query.GatewayID, query.GRPCEndpoint, rpcErr)
	if rpcErr != nil {
		return "", mapEngineError(rpcErr)
	}
	return response.GetGroupJid(), nil
}

// GetChatPresence subscribes to a contact's presence updates and returns the
// unknown snapshot.
func (c *EngineClient) GetChatPresence(
	ctx context.Context,
	org string,
	session string,
	chatJID string,
) (domain.PresenceStatus, error) {
	query, conn, cancel, err := c.readQuery(ctx, org, session)
	if err != nil {
		return domain.PresenceStatus{}, err
	}
	defer cancel()
	response, rpcErr := gatewayv1.NewGatewayEngineServiceClient(conn).GetChatPresence(ctx, &gatewayv1.GetChatPresenceRequest{
		Target:          sessionTargetProto(query),
		AssignmentEpoch: query.AssignmentEpoch,
		ChatJid:         chatJID,
	})
	c.record(query.GatewayID, query.GRPCEndpoint, rpcErr)
	if rpcErr != nil {
		return domain.PresenceStatus{}, mapEngineError(rpcErr)
	}
	p := response.GetPresence()
	return domain.PresenceStatus{
		ChatJID: p.GetChatJid(), From: p.GetFrom(), State: p.GetState(), Media: p.GetMedia(),
		Unavailable: p.GetUnavailable(), LastSeen: p.GetLastSeenUnixMs(),
	}, nil
}

// SetChatPresence sends per-chat typing state through the assigned engine.
func (c *EngineClient) SetChatPresence(
	ctx context.Context,
	org string,
	session string,
	chatJID string,
	state string,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).SetChatPresence(ctx, &gatewayv1.SetChatPresenceRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		ChatJid:         chatJID,
		State:           state,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// BackfillSession pulls the session's direct-API snapshot. Slow read: the send
// deadline applies because a large history pull can outlast the unary budget.
func (c *EngineClient) BackfillSession(
	ctx context.Context,
	org string,
	session string,
) (domain.BackfillSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return domain.BackfillSnapshot{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return domain.BackfillSnapshot{}, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).BackfillSession(ctx, &gatewayv1.BackfillSessionRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return domain.BackfillSnapshot{}, mapEngineError(err)
	}
	snapshot := response.GetSnapshot()
	out := domain.BackfillSnapshot{
		Contacts: make([]domain.BackfillContact, 0, len(snapshot.Contacts)),
		Groups:   make([]domain.BackfillGroup, 0, len(snapshot.Groups)),
	}
	for _, ct := range snapshot.Contacts {
		out.Contacts = append(out.Contacts, domain.BackfillContact{
			LID: ct.GetLid(), PhoneJID: ct.GetPhoneJid(), PhoneNumber: ct.GetPhoneNumber(),
			Name: ct.GetName(), BusinessName: ct.GetBusinessName(),
		})
	}
	for _, g := range snapshot.Groups {
		group := domain.BackfillGroup{
			GroupJID: g.GetGroupJid(), Subject: g.GetSubject(), Description: g.GetDescription(),
			OwnerJID: g.GetOwnerJid(), Participants: int(g.GetParticipants()), IsAnnounce: g.GetIsAnnounce(),
			IsLocked: g.GetIsLocked(), CreatedAtWA: g.GetCreatedAtWaUnixMs(),
			Members: make([]domain.BackfillMember, 0, len(g.Members)),
		}
		for _, m := range g.Members {
			group.Members = append(group.Members, domain.BackfillMember{
				LID: m.GetLid(), JID: m.GetJid(), PhoneNumber: m.GetPhoneNumber(),
				Tag: m.GetTag(), Name: m.GetName(), Role: domain.GroupRole(m.GetRole()),
			})
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

// PrepareSession materializes the gateway-local pairing substrate for an
// API-created session row. Quick idempotent call: unary deadline.
func (c *EngineClient) PrepareSession(
	ctx context.Context,
	org string,
	session string,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).PrepareSession(ctx, &gatewayv1.PrepareSessionRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// BeginPairing starts QR pairing and returns the current snapshot code. The
// first code usually arrives asynchronously over the auth.qr event stream, so
// an empty snapshot is a normal outcome rather than an error.
func (c *EngineClient) BeginPairing(
	ctx context.Context,
	org string,
	session string,
) (application.PairingSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return application.PairingSnapshot{}, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return application.PairingSnapshot{}, err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).BeginPairing(ctx, &gatewayv1.BeginPairingRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return application.PairingSnapshot{}, mapEngineError(err)
	}
	return application.PairingSnapshot{Code: response.GetQrCode(), ExpiresAt: response.GetQrExpiresAtUnixMs()}, nil
}

// PairPhone requests a phone-number pairing code through the assigned engine.
// The gateway connects and negotiates the code with WhatsApp before answering,
// so the send deadline applies like the other slow operations.
func (c *EngineClient) PairPhone(
	ctx context.Context,
	org string,
	session string,
	phone string,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return "", err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return "", err
	}
	response, err := gatewayv1.NewGatewayEngineServiceClient(conn).PairPhone(ctx, &gatewayv1.PairPhoneRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		Phone:           phone,
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	if err != nil {
		return "", mapEngineError(err)
	}
	return response.GetPairingCode(), nil
}

// LogoutSession unlinks the device as a durable command; the engine mints its
// own stable command id from the request's ULID and the ledger replays it on
// retry. Destructive but quick: unary deadline.
func (c *EngineClient) LogoutSession(
	ctx context.Context,
	org string,
	session string,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).LogoutSession(ctx, &gatewayv1.LogoutSessionRequest{
		Target:          sessionTargetProto(target),
		AssignmentEpoch: target.AssignmentEpoch,
		CommandId:       domain.NewULID(),
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// ForgetSession drops the session's in-memory runtime on its assigned engine.
// Idempotent by contract: forgetting an unknown session succeeds.
func (c *EngineClient) ForgetSession(
	ctx context.Context,
	org string,
	session string,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return err
	}
	_, err = gatewayv1.NewGatewayEngineServiceClient(conn).ForgetSession(ctx, &gatewayv1.ForgetSessionRequest{
		Target: sessionTargetProto(target),
	})
	c.record(target.GatewayID, target.GRPCEndpoint, err)
	return mapEngineError(err)
}

// readQuery resolves a read target and pooled connection under the unary
// deadline; callers close the returned cancel.
func (c *EngineClient) readQuery(
	ctx context.Context,
	org string,
	session string,
) (domain.SessionEngineTarget, *grpc.ClientConn, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		cancel()
		return domain.SessionEngineTarget{}, nil, nil, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		cancel()
		return domain.SessionEngineTarget{}, nil, nil, err
	}
	return target, conn, cancel, nil
}

func sessionTargetProto(query domain.SessionEngineTarget) *gatewayv1.SessionTarget {
	return &gatewayv1.SessionTarget{
		OrganizationId: query.OrganizationID,
		SessionId:      query.SessionID,
		GatewayId:      query.GatewayID,
	}
}

func protoGroupInfo(info *gatewayv1.GroupInfo) application.GroupInfoResult {
	if info == nil {
		return application.GroupInfoResult{}
	}
	return application.GroupInfoResult{
		GroupJID: info.GetGroupJid(), Subject: info.GetSubject(), Description: info.GetDescription(),
		OwnerJID: info.GetOwnerJid(), Participants: info.GetParticipants(),
		IsAnnounce: info.GetIsAnnounce(), IsLocked: info.GetIsLocked(),
	}
}

func participantChangeProto(action application.GroupParticipantChange) (gatewayv1.GroupParticipantChange, bool) {
	switch action {
	case application.GroupChangeAdd:
		return gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_ADD, true
	case application.GroupChangeRemove:
		return gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_REMOVE, true
	case application.GroupChangePromote:
		return gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_PROMOTE, true
	case application.GroupChangeDemote:
		return gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_DEMOTE, true
	default:
		return gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_UNSPECIFIED, false
	}
}

func mapEngineError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound:
		return domain.ErrNotFound("gateway session not found")
	case codes.FailedPrecondition:
		return domain.ErrConflict("gateway assignment changed")
	case codes.Unavailable:
		return domain.ErrUnavailable("gateway unavailable")
	case codes.InvalidArgument:
		return domain.ErrValidation("gateway rejected request")
	default:
		return fmt.Errorf("gateway engine: %w", err)
	}
}

func sessionStatus(value gatewayv1.GatewaySessionStatus) (domain.SessionStatus, error) {
	switch value {
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STOPPED:
		return domain.SessionStopped, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STARTING:
		return domain.SessionStarting, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_SCAN_QR:
		return domain.SessionScanQR, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_WORKING:
		return domain.SessionWorking, nil
	case gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_LOGGED_OUT:
		return domain.SessionLoggedOut, nil
	default:
		return "", domain.ErrValidation("gateway returned unknown session status")
	}
}

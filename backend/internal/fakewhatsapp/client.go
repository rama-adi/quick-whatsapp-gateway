package fakewhatsapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

// Client implements the gateway's WhatsApp boundary over the development fake
// server. Its whatsmeow client is only used for local protobuf construction and
// device-store helpers; no method invokes its network transport.
type Client struct {
	device          *store.Device
	local           *whatsmeow.Client
	baseURL         string
	key             string
	httpClient      *http.Client
	mu              sync.Mutex
	connected       bool
	pairedPersisted bool
	handler         whatsmeow.EventHandler
	qr              chan whatsmeow.QRChannelItem
	cancel          context.CancelFunc
	generation      uint64
	after           uint64
}

func NewClient(device *store.Device, baseURL string) *Client {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		panic(err)
	}
	return &Client{device: device, local: whatsmeow.NewClient(device, nil), baseURL: strings.TrimRight(baseURL, "/"), key: hex.EncodeToString(keyBytes), httpClient: &http.Client{}, pairedPersisted: device != nil && device.ID != nil}
}

func (c *Client) LocalClient() *whatsmeow.Client        { return c.local }
func (c *Client) Transport() outbound.WhatsAppTransport { return c }

func (c *Client) endpoint(suffix string) string {
	return c.baseURL + "/v1/sessions/" + url.PathEscape(c.key) + suffix
}

func (c *Client) request(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fake WhatsApp HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(output)
}

func (c *Client) Connect() error {
	if c.device == nil {
		return errors.New("fake WhatsApp device missing")
	}
	var number string
	if c.device.ID != nil {
		number = c.device.ID.User
		if !strings.HasPrefix(number, "555") {
			return fmt.Errorf("fake WhatsApp refuses non-fake paired identity %q", number)
		}
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.generation++
	generation := c.generation
	c.mu.Unlock()
	var response ConnectResponse
	if err := c.request(ctx, http.MethodPost, c.endpoint("/connect"), ConnectRequest{Number: number}, &response); err != nil {
		cancel()
		return err
	}
	if response.Error != "" {
		cancel()
		return errors.New(response.Error)
	}
	if !response.Connected {
		cancel()
		return errors.New("fake WhatsApp server declined connection")
	}
	c.mu.Lock()
	if generation != c.generation || ctx.Err() != nil {
		c.mu.Unlock()
		return context.Canceled
	}
	c.connected = true
	c.mu.Unlock()
	go c.events(ctx, generation)
	return nil
}

func (c *Client) Disconnect() {
	c.mu.Lock()
	c.connected = false
	c.generation++
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
	// Gateway manager shutdown is bounded to ten seconds. Use that existing
	// budget so a stopped development server cannot hold shutdown indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.request(ctx, http.MethodPost, c.endpoint("/disconnect"), struct{}{}, nil)
}

func (c *Client) IsConnected() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.connected }
func (c *Client) IsLoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pairedPersisted && c.connected
}
func (c *Client) Logout(ctx context.Context) error { c.Disconnect(); return nil }
func (c *Client) AddEventHandler(handler whatsmeow.EventHandler) uint32 {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
	return 1
}

func (c *Client) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	c.mu.Lock()
	c.qr = make(chan whatsmeow.QRChannelItem, 1)
	ch := c.qr
	c.mu.Unlock()
	var response PairResponse
	if err := c.request(ctx, http.MethodPost, c.endpoint("/pair"), PairRequest{Method: "qr"}, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	// The server queues the code as a QR event. The event loop starts after
	// Connect, matching whatsmeow's QR-channel-before-connect ordering.
	return ch, nil
}

func (c *Client) PairPhone(ctx context.Context, phone string, _ bool, _ whatsmeow.PairClientType, _ string) (string, error) {
	if !strings.HasPrefix(phone, "555") {
		return "", fmt.Errorf("fake WhatsApp requires a 555 phone number")
	}
	var response PairResponse
	if err := c.request(ctx, http.MethodPost, c.endpoint("/pair"), PairRequest{Number: phone, Method: "code"}, &response); err != nil {
		return "", err
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	return response.Code, nil
}

func (c *Client) events(ctx context.Context, generation uint64) {
	for ctx.Err() == nil {
		c.mu.Lock()
		after := c.after
		active := c.generation == generation
		c.mu.Unlock()
		if !active {
			return
		}
		var response EventsResponse
		endpoint := fmt.Sprintf("%s?after=%d", c.endpoint("/events"), after)
		if err := c.request(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			active := c.generation == generation
			if active {
				c.connected = false
			}
			c.mu.Unlock()
			if active {
				c.emit(&events.Disconnected{})
			}
			return
		}
		for _, event := range response.Events {
			if ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			active := c.generation == generation
			last := c.after
			if active && event.Sequence > last {
				c.after = event.Sequence
			}
			c.mu.Unlock()
			if !active {
				return
			}
			if event.Sequence <= last {
				continue
			}
			c.handleEvent(ctx, event)
		}
	}
}

func (c *Client) emit(event any) {
	c.mu.Lock()
	handler := c.handler
	c.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

func (c *Client) handleEvent(ctx context.Context, event Event) {
	switch event.Kind {
	case "paired":
		if !strings.HasPrefix(event.Number, "555") {
			return
		}
		jid := types.NewJID(event.Number, types.DefaultUserServer)
		oldID, oldLID, oldAccount := c.device.ID, c.device.LID, c.device.Account
		c.device.ID = &jid
		c.device.LID = types.NewJID(event.Number, types.HiddenUserServer)
		if c.device.Account == nil {
			c.device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, ed25519.SignatureSize), AccountSignatureKey: make([]byte, ed25519.PublicKeySize), DeviceSignature: make([]byte, ed25519.SignatureSize)}
		}
		if err := c.device.Save(ctx); err != nil {
			c.device.ID, c.device.LID, c.device.Account = oldID, oldLID, oldAccount
			c.emit(&events.PairError{ID: jid, Error: err})
			return
		}
		c.mu.Lock()
		c.pairedPersisted = true
		c.mu.Unlock()
		c.emit(&events.PairSuccess{ID: jid})
	case "connected":
		c.mu.Lock()
		if !c.pairedPersisted {
			c.mu.Unlock()
			return
		}
		c.connected = true
		c.mu.Unlock()
		c.emit(&events.Connected{})
	case "disconnected":
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.emit(&events.Disconnected{})
	case "logged_out":
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.emit(&events.LoggedOut{})
	case "stream_replaced":
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.emit(&events.StreamReplaced{})
	case "temporary_ban":
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.emit(&events.TemporaryBan{})
	case "outdated", "client_outdated":
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.emit(&events.ClientOutdated{})
	case "receipt":
		var data struct {
			Type       string            `json:"type"`
			MessageIDs []types.MessageID `json:"message_ids"`
			Chat       string            `json:"chat"`
			Sender     string            `json:"sender"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return
		}
		chat, err := types.ParseJID(data.Chat)
		if err != nil {
			return
		}
		sender, err := types.ParseJID(data.Sender)
		if err != nil {
			return
		}
		timestamp := time.Now()
		if event.Timestamp != 0 {
			timestamp = time.UnixMilli(event.Timestamp)
		}
		c.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer}, MessageIDs: data.MessageIDs, Type: types.ReceiptType(data.Type), Timestamp: timestamp})
	case "joined_group":
		var info types.GroupInfo
		if err := json.Unmarshal(event.Data, &info); err != nil {
			return
		}
		c.emit(&events.JoinedGroup{GroupInfo: info, Type: "new"})
	case "group_info":
		var info events.GroupInfo
		if err := json.Unmarshal(event.Data, &info); err != nil {
			return
		}
		c.emit(&info)
	case "qr":
		c.mu.Lock()
		qr := c.qr
		c.mu.Unlock()
		if qr != nil {
			select {
			case qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: event.Code, Timeout: time.Minute}:
			default:
			}
		}
	case "message":
		var message waE2E.Message
		if err := protojson.Unmarshal(event.Message, &message); err != nil {
			return
		}
		chat, err := types.ParseJID(event.Chat)
		if err != nil {
			return
		}
		sender, err := types.ParseJID(event.Sender)
		if err != nil {
			return
		}
		senderAlt, _ := types.ParseJID(event.SenderAlt)
		c.emit(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: senderAlt, IsFromMe: event.FromMe, IsGroup: chat.Server == types.GroupServer}, ID: event.ID, PushName: event.PushName, Timestamp: time.UnixMilli(event.Timestamp)}, Message: &message})
	}
}

func (c *Client) operation(ctx context.Context, request OperationRequest) (OperationResponse, error) {
	if c.device == nil || c.device.ID == nil || !strings.HasPrefix(c.device.ID.User, "555") {
		return OperationResponse{}, errors.New("fake WhatsApp session is not paired to a fake number")
	}
	var response OperationResponse
	if err := c.request(ctx, http.MethodPost, c.endpoint("/operations"), request, &response); err != nil {
		return response, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (c *Client) SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	raw, err := protojson.Marshal(message)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	request := OperationRequest{Kind: "send", To: to.String(), Message: raw}
	if len(extra) > 0 {
		request.ID = string(extra[0].ID)
	}
	response, err := c.operation(ctx, request)
	if response.Status != 0 {
		return whatsmeow.SendResponse{}, fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, response.Status)
	}
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if secret := message.GetMessageContextInfo().GetMessageSecret(); len(secret) > 0 && c.device != nil && c.device.ID != nil && c.device.MsgSecrets != nil {
		sender := c.device.ID.ToNonAD()
		if to.Server == types.GroupServer || to.Server == types.HiddenUserServer {
			sender = c.device.LID
		}
		if err := c.device.MsgSecrets.PutMessageSecret(ctx, to, sender, response.ID, secret); err != nil {
			return whatsmeow.SendResponse{}, fmt.Errorf("persist outgoing message secret: %w", err)
		}
	}
	return whatsmeow.SendResponse{ID: response.ID, Timestamp: time.Now()}, nil
}

func (c *Client) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	response, err := c.operation(ctx, OperationRequest{Kind: "upload", To: string(mediaType), DataBase64: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	if response.Upload == nil {
		return whatsmeow.UploadResponse{}, errors.New("fake WhatsApp upload result missing")
	}
	decode := func(value string) ([]byte, error) { return base64.StdEncoding.DecodeString(value) }
	sha, err := decode(response.Upload.FileSHA256)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	enc, err := decode(response.Upload.FileEncSHA256)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	key, err := decode(response.Upload.MediaKey)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	return whatsmeow.UploadResponse{URL: response.Upload.URL, DirectPath: response.Upload.DirectPath, FileLength: response.Upload.FileLength, FileSHA256: sha, FileEncSHA256: enc, MediaKey: key}, nil
}

# whatsmeow API Recon Cheat-Sheet

> Verified against source in `$GOMODCACHE/go.mau.fi/whatsmeow@v0.0.0-20260622185415-5f04eac6dbbb`
> (resolved by `go get go.mau.fi/whatsmeow@latest` on 2026-06-26).
> Companion deps: `go.mau.fi/util v0.9.10`, `go.mau.fi/libsignal v0.2.2`, `google.golang.org/protobuf v1.36.11`.
>
> Everything below is read from real source. Signatures are exact. Items I could not confirm are marked **UNVERIFIED**.
> NOTE: the API is heavily `context.Context`-first now. Nearly every network/store method takes `ctx` as the first arg.

---

## 1. Import paths (exact)

```go
import (
    "go.mau.fi/whatsmeow"                      // Client, NewClient, SendResponse, Build* helpers
    "go.mau.fi/whatsmeow/store"                // Device, DeviceContainer, per-device store interfaces
    "go.mau.fi/whatsmeow/store/sqlstore"       // Container, New, NewWithDB, NewWithWrappedDB
    "go.mau.fi/whatsmeow/types"                // JID, MessageInfo, MessageID, server consts
    "go.mau.fi/whatsmeow/types/events"         // event structs (Message, Connected, ...)
    "go.mau.fi/whatsmeow/proto/waE2E"          // *waE2E.Message and all message protos
    "go.mau.fi/whatsmeow/proto/waCommon"       // waCommon.MessageKey (used by reactions etc.)
    waLog "go.mau.fi/whatsmeow/util/log"       // Logger, waLog.Stdout(...), waLog.Noop
    "go.mau.fi/util/dbutil"                    // dbutil.Database, NewWithDB, Dialect (for custom backends)
    "google.golang.org/protobuf/proto"         // proto.String, proto.Int64, proto.Bool, etc.
)
```

- **The protobuf message package is `go.mau.fi/whatsmeow/proto/waE2E`** (type `*waE2E.Message`). The old `binary/proto` path is NOT the one used in the current API; `send.go` imports `go.mau.fi/whatsmeow/proto/waE2E` and `go.mau.fi/whatsmeow/proto/waCommon`.
- Logger: `waLog.Stdout(module, minLevel string, color bool) Logger`. There is also a package var `waLog.Noop` (a no-op Logger). Passing `nil` to `NewClient` / `sqlstore.New` defaults to no-op. (Other constructors like `Zerolog` are **UNVERIFIED** — only `Stdout` is a top-level `func`.)

---

## 2. Client lifecycle

```go
func whatsmeow.NewClient(deviceStore *store.Device, log waLog.Logger) *Client
```

`Client` (selected exported fields):
```go
type Client struct {
    Store *store.Device
    Log   waLog.Logger
    EnableAutoReconnect  bool
    InitialAutoReconnect bool
    AutoReconnectHook    func(error) bool
    PrePairCallback      func(jid types.JID, platform, businessName string) bool
    QRClientType         PairClientType
    AutoTrustIdentity    bool
    GetMessageForRetry   func(requester, to types.JID, id types.MessageID) *waE2E.Message
    // ... many more
}
```

Methods:
```go
func (cli *Client) Connect() error
func (cli *Client) ConnectContext(ctx context.Context) error
func (cli *Client) Disconnect()                                   // no return
func (cli *Client) Logout(ctx context.Context) error
func (cli *Client) IsConnected() bool
func (cli *Client) IsLoggedIn() bool
func (cli *Client) WaitForConnection(timeout time.Duration) bool
func (cli *Client) ResetConnection()

func (cli *Client) AddEventHandler(handler EventHandler) uint32   // returns handler id
func (cli *Client) RemoveEventHandler(id uint32)                  // (present; remove by returned id)
type EventHandler func(evt any)
```

Lifecycle notes:
- `cli.Store` is the `*store.Device` you passed to `NewClient`.
- `cli.Store.ID == nil` means **not yet logged in** (need QR/pair). After pairing, `ID` is populated.
- After `PairSuccess` the lib reconnects; wait for `events.Connected` before sending.

---

## 3. Pairing

### QR pairing
```go
func (cli *Client) GetQRChannel(ctx context.Context) (<-chan QRChannelItem, error)
```
- **Must be called BEFORE `Connect()`.**
- `ctx` controls the lifetime of the channel.

```go
type QRChannelItem struct {
    Event   string         // "code", "error", "success", "timeout", ...
    Error   error          // set when Event == "error"
    Code    string         // the QR string to render, when Event == "code"
    Timeout time.Duration  // time until the next code
}
```

Event constants / sentinel values (`qrchan.go`):
```go
const QRChannelEventCode  = "code"
const QRChannelEventError = "error"
var QRChannelSuccess                  = QRChannelItem{Event: "success"}
var QRChannelTimeout                  = QRChannelItem{Event: "timeout"}
var QRChannelErrUnexpectedEvent       = QRChannelItem{Event: "err-unexpected-state"}
var QRChannelClientOutdated           = QRChannelItem{Event: "err-client-outdated"}
var QRChannelScannedWithoutMultidevice= QRChannelItem{Event: "err-scanned-without-multidevice"}
```
Usage:
```go
qrChan, _ := cli.GetQRChannel(context.Background())
go func() {
    for item := range qrChan {
        switch item.Event {
        case whatsmeow.QRChannelEventCode: // render item.Code (e.g. qrterminal)
        case "success":                    // paired
        case "timeout":                    // codes ran out
        }
    }
}()
cli.Connect()
```

### Phone-number (code) pairing
```go
func (cli *Client) PairPhone(ctx context.Context, phone string, showPushNotification bool,
    clientType PairClientType, clientDisplayName string) (string, error)
```
- Returns the pairing code string (e.g. "ABCD-1234").
- Call **after** `Connect()` and after the first QR item / a short sleep (connection must be established).
- `phone` is full international number, digits only (no `+`).
- `clientDisplayName` must be formatted `"Browser (OS)"`, e.g. `"Chrome (Linux)"` — server validates and 400s otherwise.

`PairClientType` constants (`pair-code.go`):
```go
type PairClientType string
const (
    PairClientUnknown        PairClientType = "0"
    PairClientChrome         PairClientType = "1"
    PairClientEdge           PairClientType = "2"
    PairClientFirefox        PairClientType = "3"
    PairClientIE             PairClientType = "4"
    PairClientOpera          PairClientType = "5"
    PairClientSafari         PairClientType = "6"
    PairClientElectron       PairClientType = "7"
    PairClientUWP            PairClientType = "8"
    PairClientOtherWebClient PairClientType = "9"
    PairClientMacOS          PairClientType = "c"
    PairClientAndroid        PairClientType = "e"
)
```

---

## 4. Store / persistence

### 4a. RECOMMENDED PATH (v1): use `sqlstore`, NOT a hand-written backend

`sqlstore.Container` already implements `store.DeviceContainer` AND wires up every per-device store interface for you. **For v1, prefer `sqlstore` over hand-implementing the interfaces.**

```go
func sqlstore.New(ctx context.Context, dialect, address string, log waLog.Logger) (*Container, error)
func sqlstore.NewWithDB(db *sql.DB, dialect string, log waLog.Logger) *Container
func sqlstore.NewWithWrappedDB(wrapped *dbutil.Database, log waLog.Logger) *Container

func (c *Container) Upgrade(ctx context.Context) error     // run migrations (New does it; NewWithDB does NOT)
func (c *Container) GetFirstDevice(ctx context.Context) (*store.Device, error)
func (c *Container) GetAllDevices(ctx context.Context) ([]*store.Device, error)
func (c *Container) GetDevice(ctx context.Context, jid types.JID) (*store.Device, error)
func (c *Container) NewDevice() *store.Device
func (c *Container) PutDevice(ctx context.Context, device *store.Device) error
func (c *Container) DeleteDevice(ctx context.Context, store *store.Device) error
func (c *Container) Close() error
```

Example:
```go
container, err := sqlstore.New(ctx, "sqlite3", "file:wa.db?_foreign_keys=on", waLog.Stdout("DB","INFO",true))
device, err := container.GetFirstDevice(ctx)   // returns a fresh NewDevice() if DB empty (NOT an error)
cli := whatsmeow.NewClient(device, nil)
```

- **`GetFirstDevice` returns `container.NewDevice()` (never nil, no error) when the DB has no devices.** So after first run, `device.ID == nil` -> trigger QR/pair flow.
- `New` calls `sql.Open(dialect, address)` then `NewWithDB`, then runs `Upgrade`. `NewWithDB` does NOT upgrade — call `Upgrade` yourself.
- `PutDevice` returns `ErrDeviceIDMustBeSet` ("device JID must be known before accessing database") if `device.ID` is nil.

### 4b. dbutil dialects — CRITICAL CONSTRAINT FOR "MySQL store"

`dbutil.Dialect` (from `go.mau.fi/util/dbutil`, `database.go`):
```go
type Dialect int
const (
    DialectUnknown Dialect = iota
    Postgres
    SQLite
)
func dbutil.ParseDialect(engine string) (Dialect, error)
func dbutil.NewWithDB(db *sql.DB, rawDialect string) (*Database, error)
```

- **`dbutil` ONLY supports Postgres and SQLite. There is NO MySQL dialect.** `ParseDialect` only recognizes postgres/sqlite engine strings; anything else -> `DialectUnknown` / error.
- `sqlstore` itself documents: *"Only SQLite and Postgres are currently fully supported."* (`container.go`). It branches on `dbutil.SQLite` for SQLite-specific SQL and otherwise emits Postgres-style positional params.
- **Therefore you CANNOT get a MySQL backend by passing `"mysql"` to `sqlstore.New`/`NewWithDB`** — dbutil will reject the dialect.

**Implication for a "custom MySQL store":** you must hand-implement `store.DeviceContainer` + the per-device store interfaces (Section 4c) backed by your own MySQL code, and assign them onto the `*store.Device` via `device.SetAllStores(...)` (see 4d). dbutil/sqlstore will not do MySQL for you.

> RECOMMENDATION for v1: ship with `sqlstore` + SQLite (single-process) or Postgres (multi-process). Only build the custom MySQL backend if MySQL is a hard product requirement — it is a large surface (12+ interfaces) and not supported out of the box.

### 4c. Interfaces a custom backend MUST satisfy (exact, from `store/store.go`)

A `*store.Device` holds these fields; a custom backend supplies an implementation for each:

```go
type Device struct {
    // identity/keys (plain fields, persisted via DeviceContainer.PutDevice):
    NoiseKey, IdentityKey *keys.KeyPair
    SignedPreKey          *keys.PreKey
    RegistrationID        uint32
    AdvSecretKey          []byte
    ID  *types.JID         // nil until logged in
    LID types.JID
    Account *waAdv.ADVSignedDeviceIdentity
    Platform, BusinessName, PushName string
    FacebookUUID uuid.UUID
    Initialized bool

    // pluggable stores (the interfaces you implement for MySQL):
    Identities    IdentityStore
    Sessions      SessionStore
    PreKeys       PreKeyStore
    SenderKeys    SenderKeyStore
    AppStateKeys  AppStateSyncKeyStore
    AppState      AppStateStore
    Contacts      ContactStore
    ChatSettings  ChatSettingsStore
    MsgSecrets    MsgSecretStore
    PrivacyTokens PrivacyTokenStore
    NCTSalt       NCTSaltStore
    EventBuffer   EventBuffer
    LIDs          LIDStore
    Container     DeviceContainer
}
```

Interface method sets (verbatim signatures):

```go
type DeviceContainer interface {
    PutDevice(ctx context.Context, store *Device) error
    DeleteDevice(ctx context.Context, store *Device) error
}

type IdentityStore interface {
    PutIdentity(ctx context.Context, address string, key [32]byte) error
    DeleteAllIdentities(ctx context.Context, phone string) error
    DeleteIdentity(ctx context.Context, address string) error
    IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error)
}

type SessionStore interface {
    GetSession(ctx context.Context, address string) ([]byte, error)
    HasSession(ctx context.Context, address string) (bool, error)
    GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error)
    PutSession(ctx context.Context, address string, session []byte) error
    PutManySessions(ctx context.Context, sessions map[string][]byte) error
    DeleteAllSessions(ctx context.Context, phone string) error
    DeleteSession(ctx context.Context, address string) error
    MigratePNToLID(ctx context.Context, pn, lid types.JID) error
}

type PreKeyStore interface {
    GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error)
    GenOnePreKey(ctx context.Context) (*keys.PreKey, error)
    GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error)
    RemovePreKey(ctx context.Context, id uint32) error
    MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error
    UploadedPreKeyCount(ctx context.Context) (int, error)
}

type SenderKeyStore interface {
    PutSenderKey(ctx context.Context, group, user string, session []byte) error
    GetSenderKey(ctx context.Context, group, user string) ([]byte, error)
}

type AppStateSyncKeyStore interface {
    PutAppStateSyncKey(ctx context.Context, id []byte, key AppStateSyncKey) error
    GetAppStateSyncKey(ctx context.Context, id []byte) (*AppStateSyncKey, error)
    GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error)
    GetAllAppStateSyncKeys(ctx context.Context) ([]*AppStateSyncKey, error)
}
// type AppStateSyncKey struct { Data, Fingerprint []byte; Timestamp int64 }

type AppStateStore interface {
    PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error
    GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error)
    DeleteAppStateVersion(ctx context.Context, name string) error
    PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []AppStateMutationMAC) error
    DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error
    GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) (valueMAC []byte, err error)
}
// type AppStateMutationMAC struct { IndexMAC, ValueMAC []byte }

type ContactStore interface {
    PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error)
    PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error)
    PutContactName(ctx context.Context, user types.JID, fullName, firstName string) error
    PutAllContactNames(ctx context.Context, contacts []ContactEntry) error
    PutManyRedactedPhones(ctx context.Context, entries []RedactedPhoneEntry) error
    GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error)
    GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error)
}
// type ContactEntry struct { JID types.JID; FirstName, FullName string }
// type RedactedPhoneEntry struct { JID types.JID; RedactedPhone string }

type ChatSettingsStore interface {
    PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error
    PutPinned(ctx context.Context, chat types.JID, pinned bool) error
    PutArchived(ctx context.Context, chat types.JID, archived bool) error
    GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error)
}

type MsgSecretStore interface {
    PutMessageSecrets(ctx context.Context, inserts []MessageSecretInsert) error
    PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error
    GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error)
}
// type MessageSecretInsert struct { Chat, Sender types.JID; ID types.MessageID; Secret []byte }

type PrivacyTokenStore interface {
    PutPrivacyTokens(ctx context.Context, tokens ...PrivacyToken) error
    GetPrivacyToken(ctx context.Context, user types.JID) (*PrivacyToken, error)
    DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error)
}
// type PrivacyToken struct { User types.JID; Token []byte; Timestamp, SenderTimestamp time.Time }

type NCTSaltStore interface {
    PutNCTSalt(ctx context.Context, salt []byte) error
    GetNCTSalt(ctx context.Context) ([]byte, error)
    DeleteNCTSalt(ctx context.Context) error
}

type EventBuffer interface {
    GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*BufferedEvent, error)
    PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error
    DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error
    ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error
    DeleteOldBufferedHashes(ctx context.Context) error
    GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error)
    AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error
    DeleteOldOutgoingEvents(ctx context.Context) error
}
// type BufferedEvent struct { Plaintext []byte; InsertTime, ServerTime time.Time }

// LID mapping (phone-number <-> LID identity). This is a GLOBAL store, not per-device.
type LIDStore interface {
    PutManyLIDMappings(ctx context.Context, mappings []LIDMapping) error
    PutLIDMapping(ctx context.Context, lid, jid types.JID) error
    GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error)
    GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
    GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error)
}
// type LIDMapping struct { LID, PN types.JID }
```

Convenience aggregate interfaces (store.go):
```go
type AllSessionSpecificStores interface { // per-device set
    IdentityStore; SessionStore; PreKeyStore; SenderKeyStore
    AppStateSyncKeyStore; AppStateStore; ContactStore; ChatSettingsStore
    MsgSecretStore; PrivacyTokenStore; NCTSaltStore; EventBuffer
}
type AllGlobalStores interface { LIDStore }
type AllStores interface { AllSessionSpecificStores; AllGlobalStores }
```

### 4d. Wiring a custom backend onto a Device

```go
func (device *Device) SetAllStores(store AllSessionSpecificStores)
```
- `SetAllStores` assigns one object implementing all per-device interfaces to the corresponding `Device` fields.
- Set `device.Container = yourContainer` (a `DeviceContainer`) and `device.LIDs = yourLIDStore` separately (LIDStore is global, not part of `AllSessionSpecificStores`).
- The exact field-assignment behavior of `SetAllStores` beyond the per-device fields is **UNVERIFIED** (read `store/store.go` impl before relying on it for LIDs).

> Bottom line: a custom MySQL backend must implement all 12 per-device interfaces + DeviceContainer + LIDStore. This is what `sqlstore` does internally. Reusing `sqlstore` (SQLite/Postgres) avoids all of it.

---

## 5. Events (`go.mau.fi/whatsmeow/types/events`)

Register: `id := cli.AddEventHandler(func(evt any){ switch e := evt.(type) { case *events.Message: ... } })`.
Handlers receive **pointers** to event structs (e.g. `*events.Message`).

Event struct names present in the package (verified):
`Message`, `FBMessage`, `UndecryptableMessage`, `Receipt`, `Connected`, `Disconnected`,
`LoggedOut`, `StreamReplaced`, `KeepAliveTimeout`, `KeepAliveRestored`, `ManualLoginReconnect`,
`TemporaryBan`, `ConnectFailure`, `ClientOutdated`, `CATRefreshError`, `StreamError`,
`QR`, `PairSuccess`, `PairError`, `QRScannedWithoutMultidevice`,
`Presence`, `ChatPresence`, `JoinedGroup`, `GroupInfo`, `Picture`, `IdentityChange`,
`PrivacySettings`, `Contact`, `PushName`, `BusinessName`, `Pin`, `Star`, `Mute`, `Archive`,
`MarkChatAsRead`, `ClearChat`, `DeleteChat`, `DeleteForMe`, `LabelEdit`, `LabelAssociationChat`,
`LabelAssociationMessage`, `AppState`, `AppStateSyncComplete`, `AppStateSyncError`,
`HistorySync`, `OfflineSyncPreview`, `OfflineSyncCompleted`, `MediaRetry`, `MediaRetryError`,
`Blocklist`, `BlocklistChange`,
`CallOffer`, `CallAccept`, `CallPreAccept`, `CallTransport`, `CallOfferNotice`,
`CallRelayLatency`, `CallTerminate`, `CallReject`, `UnknownCallEvent`,
`NewsletterJoin`, `NewsletterLeave`, `NewsletterMuteChange`, `NewsletterLiveUpdate`,
`UserAbout`, `UserStatusMute`, `TemporaryBan`, `NotifyAccountReachoutTimelock`.

Key structs:
```go
type QR struct { Codes []string }              // raw QR strings (when not using GetQRChannel)
type PairSuccess struct { ID, LID types.JID; BusinessName, Platform string }
type PairError   struct { ID, LID types.JID; BusinessName, Platform string; Error error }
type Connected struct{}
type Disconnected struct{}
type StreamReplaced struct{}
type LoggedOut struct { OnConnect bool; Reason ConnectFailureReason } // permanent disconnect
type Presence struct { From types.JID; Unavailable bool; LastSeen time.Time }
type ChatPresence struct { types.MessageSource; State types.ChatPresence; Media types.ChatPresenceMedia }
type Receipt struct {
    types.MessageSource
    MessageIDs []types.MessageID
    Timestamp  time.Time
    Type       types.ReceiptType   // ReceiptTypeDelivered/Read/ReadSelf/Played/Sender/Retry
    MessageSender types.JID
}
type GroupInfo struct {
    JID types.JID; Notify string; Sender, SenderPN *types.JID; Timestamp time.Time
    Name *types.GroupName; Topic *types.GroupTopic; Locked *types.GroupLocked
    Announce *types.GroupAnnounce; Ephemeral *types.GroupEphemeral
    Join, Leave, Promote, Demote []types.JID     // participant changes (UNVERIFIED exact field names beyond Join/Leave region)
    NewInviteLink *string
}
type JoinedGroup struct { Reason, Type string; CreateKey types.MessageID; /* embeds types.GroupInfo */ }
type Contact struct { JID types.JID; Timestamp time.Time; Action *waSyncAction.ContactAction; FromFullSync bool }
type CallOffer struct { /* see types/events/call.go: types.BasicCallMeta + CallRemoteMeta + Data *waBinary.Node */ } // fields UNVERIFIED in detail
type NewsletterJoin struct { /* embeds types.NewsletterMetadata */ } // exact fields UNVERIFIED
```

### `events.Message` — field paths

```go
type Message struct {
    Info       types.MessageInfo  // chat/sender/id/timestamp
    Message    *waE2E.Message     // unwrapped message content (use THIS)
    RawMessage *waE2E.Message     // raw, possibly wrapped (Ephemeral/ViewOnce/DeviceSent/Edited)
    IsEphemeral, IsViewOnce, IsEdit bool
    // ...
}
```

- **Text:** plain text is `e.Message.GetConversation()`. Rich/quoted text is `e.Message.GetExtendedTextMessage().GetText()`. Always check both:
  ```go
  text := m.Message.GetConversation()
  if text == "" { text = m.Message.GetExtendedTextMessage().GetText() }
  ```
- **Quoted/reply context:** via `ContextInfo`, found on `ExtendedTextMessage` and every media message:
  - `ctx := m.Message.GetExtendedTextMessage().GetContextInfo()`
  - `ctx.GetStanzaID()` = quoted message ID, `ctx.GetParticipant()` = quoted sender JID string,
    `ctx.GetQuotedMessage()` = the quoted `*waE2E.Message`, `ctx.GetRemoteJID()`.
- **Mentions:** `ctx.GetMentionedJID() []string` (JID strings).
- **Reaction:** event's `m.Message.GetReactionMessage()` → `.GetText()` (emoji, empty = removed), `.GetKey()` (`*waCommon.MessageKey`: `RemoteJID`, `FromMe`, `ID`, `Participant`).
- **Poll vote (incoming):** `cli.DecryptPollVote(ctx, m) (*waE2E.PollUpdateMessage..., error)` — see signature in §7. The raw poll-update arrives as `m.Message.GetPollUpdateMessage()`.
- **Edit:** when `m.IsEdit` is true the content is the edited message (lib unwraps `ProtocolMessage{Type:EDIT}` / `EditedMessage`). The edited target key is in `m.Message.GetProtocolMessage().GetKey()`; type `waE2E.ProtocolMessage_EDIT`.
- **Revoke (delete):** arrives as `m.Message.GetProtocolMessage()` with `GetType() == waE2E.ProtocolMessage_REVOKE`; deleted message id in `.GetKey().GetID()`.

`types.MessageInfo`:
```go
type MessageInfo struct {
    MessageSource                 // embedded: Chat, Sender, IsFromMe, IsGroup JIDs
    ID        MessageID           // = string
    ServerID  MessageServerID     // = int (newsletters only)
    Type      string
    PushName  string
    Timestamp time.Time
    Category  string
    Edit      EditAttribute
    // ...
}
type MessageSource struct {
    Chat, Sender types.JID
    IsFromMe, IsGroup bool
    AddressingMode AddressingMode
    SenderAlt, RecipientAlt types.JID
}
```

---

## 6. Types: JID etc. (`go.mau.fi/whatsmeow/types`)

```go
type JID struct {
    User       string
    RawAgent   uint8
    Device     uint16
    Integrator uint16
    Server     string
}
func types.NewJID(user, server string) JID
func types.NewADJID(user string, agent, device uint8) JID
func types.ParseJID(jid string) (JID, error)
func (jid JID) String() string
func (jid JID) ToNonAD() JID
func (jid JID) IsEmpty() bool
// JID implements sql Scan/Value and Marshal/UnmarshalText.

type MessageID       = string   // alias
type MessageServerID = int       // alias
```

Server constants (`types/jid.go`):
```go
const (
    DefaultUserServer = "s.whatsapp.net"   // individual users
    GroupServer       = "g.us"             // groups
    LegacyUserServer  = "c.us"
    BroadcastServer   = "broadcast"
    HiddenUserServer  = "lid"              // LID identities
    MessengerServer   = "msgr"
    InteropServer     = "interop"
    NewsletterServer  = "newsletter"
    HostedServer      = "hosted"
    HostedLIDServer   = "hosted.lid"
    BotServer         = "bot"
)
var (
    EmptyJID           = JID{}
    StatusBroadcastJID = NewJID("status", BroadcastServer)
)
```
- Build a user JID from a phone number: `types.NewJID("628123456789", types.DefaultUserServer)` (digits only, no `+`).
- Group JID: server is `types.GroupServer`.

---

## 7. Sending messages

```go
func (cli *Client) SendMessage(ctx context.Context, to types.JID, message *waE2E.Message,
    extra ...SendRequestExtra) (resp SendResponse, err error)

type SendResponse struct {
    Timestamp time.Time             // server timestamp
    ID        types.MessageID       // message id (string)
    ServerID  types.MessageServerID // int, newsletters only
    Sender    types.JID             // identity used (LID/PN), not always reliable
    DebugTimings MessageDebugTimings
}

type SendRequestExtra struct {     // pass at most ONE; multiple => error
    ID          types.MessageID    // custom message id (default: random)
    InlineBotJID types.JID
    Peer        bool
    Timeout     time.Duration      // default 75s; negative disables
    MediaHandle string             // for newsletter media (from Upload .Handle)
}
```

### Build helpers (all return `*waE2E.Message`, no network unless noted)

```go
func (cli *Client) BuildPollCreation(name string, optionNames []string, selectableOptionCount int) *waE2E.Message
func (cli *Client) BuildReaction(chat, sender types.JID, id types.MessageID, reaction string) *waE2E.Message
func (cli *Client) BuildEdit(chat types.JID, id types.MessageID, newContent *waE2E.Message) *waE2E.Message
func (cli *Client) BuildRevoke(chat, sender types.JID, id types.MessageID) *waE2E.Message
func (cli *Client) BuildPollVote(ctx context.Context, pollInfo *types.MessageInfo, optionNames []string) (*waE2E.Message, error)
func (cli *Client) EncryptPollVote(ctx context.Context, pollInfo *types.MessageInfo, vote *waE2E.PollVoteMessage) (*waE2E.PollUpdateMessage, error)
func (cli *Client) DecryptPollVote(ctx context.Context, vote *events.Message) (*waE2E.PollVoteMessage, error)
```
- `BuildReaction` internally builds `ReactionMessage{Key: cli.BuildMessageKey(chat,sender,id), Text: reaction, SenderTimestampMS: now}`. Empty `reaction` string removes the reaction.
- `BuildPollVote` is the high-level helper (handles encryption) — prefer it over `EncryptPollVote`.
- These build helpers return a `*waE2E.Message` you then pass to `SendMessage`.

### Message constructors by type (build by hand)

```go
// Text:
&waE2E.Message{ Conversation: proto.String("Hello") }

// Text with mentions / reply (use ExtendedTextMessage so you can attach ContextInfo):
&waE2E.Message{ ExtendedTextMessage: &waE2E.ExtendedTextMessage{
    Text: proto.String("hi @user"),
    ContextInfo: &waE2E.ContextInfo{
        StanzaID:     proto.String(quotedID),      // reply target id
        Participant:  proto.String(quotedSenderJID.String()),
        QuotedMessage: quotedMsg,                   // *waE2E.Message
        MentionedJID: []string{ "628123@s.whatsapp.net" },
    },
}}

// Location:
&waE2E.Message{ LocationMessage: &waE2E.LocationMessage{
    DegreesLatitude:  proto.Float64(-6.2),
    DegreesLongitude: proto.Float64(106.8),
    Name:             proto.String("Jakarta"),
    Address:          proto.String("..."),
}}

// Poll create:  cli.BuildPollCreation("Q?", []string{"A","B"}, 1)
// Reaction:      cli.BuildReaction(chat, sender, msgID, "👍")
// Edit:          cli.BuildEdit(chat, msgID, &waE2E.Message{Conversation: proto.String("new")})
// Revoke:        cli.BuildRevoke(chat, sender, msgID)   // sender = JID of original sender; for your own msg pass types.EmptyJID (UNVERIFIED — confirm against BuildRevoke impl)
```

### Media (image/video/audio/doc/sticker)
```go
func (cli *Client) Upload(ctx context.Context, plaintext []byte, appInfo MediaType) (resp UploadResponse, err error)
type UploadResponse struct {
    URL, DirectPath, Handle, ObjectID string
    MediaKey, FileEncSHA256, FileSHA256 []byte
    FileLength uint64
}
type MediaType string
const ( MediaImage MediaType = "WhatsApp Image Keys"; /* MediaVideo, MediaAudio, MediaDocument, ... */ )
```
Flow: `Upload` -> copy URL/DirectPath/MediaKey/FileEncSHA256/FileSHA256/FileLength into the matching `*waE2E.ImageMessage` (etc.) fields -> `SendMessage`.

---

## 8. Groups

```go
func (cli *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
func (cli *Client) CreateGroup(ctx context.Context, req ReqCreateGroup) (*types.GroupInfo, error)
func (cli *Client) UpdateGroupParticipants(ctx context.Context, jid types.JID,
    participantChanges []types.JID, action ParticipantChange) ([]types.GroupParticipant, error)
func (cli *Client) SetGroupName(ctx context.Context, jid types.JID, name string) error
func (cli *Client) GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error)
func (cli *Client) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
func (cli *Client) LeaveGroup(ctx context.Context, jid types.JID) error
// also: GetGroupInfoFromLink(ctx, code), GetGroupInfoFromInvite(ctx, jid, inviter, code, expiration)

type ReqCreateGroup struct {
    Name         string          // <=25 chars
    Participants []types.JID      // don't include yourself
    CreateKey    types.MessageID
    // plus embedded GroupEphemeral, GroupParent (community), GroupLinkedParent, etc.
}

type ParticipantChange string
const (
    ParticipantChangeAdd     ParticipantChange = "add"
    ParticipantChangeRemove  ParticipantChange = "remove"
    ParticipantChangePromote ParticipantChange = "promote"
    ParticipantChangeDemote  ParticipantChange = "demote"
)
```
- `JoinGroupWithLink` takes the invite **code** (the part after `chat.whatsapp.com/`), not the full URL — pass just the code. (Confirm by passing the trailing code; full-URL handling is **UNVERIFIED**.)
- `GetGroupInviteLink(ctx, jid, reset)`: `reset=true` revokes the old link and returns a new one. Returns the code/URL string.

---

## 9. Contacts / users

```go
func (cli *Client) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
func (cli *Client) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
func (cli *Client) GetProfilePictureInfo(ctx context.Context, jid types.JID, params *GetProfilePictureParams) (*types.ProfilePictureInfo, error)

type IsOnWhatsAppResponse struct {
    Query string            // the input phone string
    JID   types.JID         // canonical user JID
    IsIn  bool              // registered or not
    VerifiedName *types.VerifiedName
}
type UserInfo struct {
    VerifiedName *types.VerifiedName
    Status    string
    PictureID string
    Devices   []types.JID
    LID       types.JID
}
type ProfilePictureInfo struct {
    URL, ID, Type, DirectPath string
    Hash []byte
}
type GetProfilePictureParams struct {
    Preview bool; ExistingID string; IsCommunity bool
    CommonGID types.JID; InviteCode string; PersonaID string
}
```
- `IsOnWhatsApp` input: phone numbers as strings, **with** leading `+` is accepted (e.g. `"+628123456789"`); returns canonical JID in `.JID`.
- `GetProfilePictureInfo` may return `(nil, nil)` if the user has no picture / privacy hides it — handle nil. (Exact nil-vs-error behavior **UNVERIFIED** — guard for both.)

---

## 10. Gotchas / cross-cutting

- **Context everywhere:** `Logout`, `SendMessage`, all group/contact calls, all store methods take `ctx`. `Connect()` is the legacy no-ctx variant; `ConnectContext(ctx)` exists. `GetQRChannel(ctx)` and `PairPhone(ctx,...)` take ctx.
- **`Disconnect()` returns nothing.** Don't assign its result.
- **`go doc` examples in `NewClient`/`sqlstore` headers are slightly stale** (some show `container.GetFirstDevice()` without ctx); the real signatures take `ctx` — trust the signatures in this doc.
- **MySQL is not supported by sqlstore/dbutil.** See §4b. For v1, use SQLite or Postgres via `sqlstore`.
- Proto field accessors are generated getters (`GetX()`), nil-safe. Prefer them over direct field access when reading incoming messages.
- Set fields with `proto.String/Int64/Bool/Float64(...)` (pointers) when building messages.
```

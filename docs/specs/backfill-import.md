# Backfill Import — user-uploaded WhatsApp backup (crypt15)

Status: implemented.

Packages: `internal/backup` (decrypt + SQLite read), `internal/service` (`BackupImportService`),
`internal/store` (`BackfillImportRepo`), `internal/http/handlers` (`backup.go`). Frontend:
`web/app/routes/_app/user/-components/backup-import-card.tsx` + `web/app/lib/api/hooks/import.ts`.

## Why

Inbound capture only records messages from the moment a session is connected, and the admin
live-data backfill ([`resources.md`](resources.md) / `AdminService`) can pull contacts and group
metadata but **not message history** — WhatsApp exposes no "fetch all messages" API. A user who
already has an end-to-end-encrypted WhatsApp backup, however, holds their entire `msgstore.db`. This
feature lets an org user upload that `msgstore.db.crypt15` plus its key from the session dashboard;
the gateway decrypts it and upserts the full history into the session's tables.

This is distinct from, and coexists with, the admin live backfill at
`POST /admin/sessions/{session}:backfill`.

## Flow

```
POST /sessions/{session}/backfill         (multipart: file=*.crypt15, key=<hex>)
  → decrypt INLINE (internal/backup.DecryptMsgstore)   — bad key/file ⇒ 400 fail-fast
  → ownership + concurrency + quota checks
  → stage plaintext to a temp file, insert a 'running' backfill_imports row
  → return 202 { BackfillImport, status:"running" }
  → background goroutine: open SQLite, upsert chats/messages/identities/groups/members,
    record counts + fingerprint, mark succeeded/failed, delete temp file
GET  /sessions/{session}/backfill         → latest BackfillImport (poll while running)
```

Both routes are in the **sessions / `manage`** group ([`http-foundation.md`](http-foundation.md));
`super_admin` passes the capability gate as usual.

## Decryption (`internal/backup/crypt15.go`)

Pure stdlib (CGO-free): `LoadRootKey` accepts a 64-char hex key, a raw 32-byte key, or a serialized
`encrypted_backup.key`; `DeriveAESKey` runs WhatsApp's HKDF-SHA256 chain (zero salt, info
`"backup encryption"` + the `0x01` block counter — equivalent to wa-crypt-tools' `encryptionloop`).
The file is parsed faithfully to the crypt15 layout (per `ElDavoo/wa-crypt-tools`): a 1-byte protobuf
header size, an optional `0x01` msgstore feature-table flag, then the `BackupPrefix` protobuf — the
16-byte AES-GCM **IV is read from `c15_iv.IV` (field 3 → field 1)**, not a fixed offset. AES-GCM
(16-byte nonce) then decrypts the payload with the trailing 16-byte checksum stripped (single-file),
falling back to treating the last 16 bytes as the tag (multifile). `Decompress` zlib-inflates
(passing through already-raw SQLite/ZIP). `DecryptMsgstore` chains them and verifies the
`SQLite format 3` magic. Any failure is a plain error → the service maps it to `validation_error`.

## SQLite reading (`internal/backup/msgstore.go` + `readers.go`)

`modernc.org/sqlite` opened read-only (`?mode=ro&immutable=1`). **Capability detection, not
version-gating** — WhatsApp has no stable schema version (`PRAGMA user_version` is always 1;
`props.schema-maintainer/previous-run-build-id` is an opaque build id). On open the reader probes
`sqlite_master` + `PRAGMA table_info(...)` and requires only the core `message`/`chat`/`jid` trio;
every optional join (`message_media`, `message_location`, `message_quoted`, `message_mentions`,
group participants) is added only when present and degrades to `NULL`/`0` otherwise, so a renamed or
absent table drops just that enrichment. A `Fingerprint()` (build id + user_version + a hash of the
detected table set) is recorded on the job row for observability.

Mapping (every JID is the `jid.raw_string`; `message.timestamp` is already epoch-ms):

| Backup | Gateway |
|---|---|
| `chat` ⋈ `jid` | `chats` (chat_jid, type from server, name=subject, last_message_at=sort_timestamp) |
| `message` ⋈ chat/jid/media/location/quoted | `messages` (wa_message_id=key_id, direction from from_me, body=text/caption/place, type from `message_type`, quoted, mentions, media_meta) |
| `jid` (lid + s.whatsapp.net) | `whatsapp_identities` (best-effort name from `message_mentions.display_name`) |
| `chat` on `g.us` | `whatsapp_groups` (subject) |
| `group_participant_user` | `whatsapp_group_members` (rank→role, label→tag) |

`message_type` maps to a coarse type (0 text; 1/42 image; 3/43 video; 13 gif; 2 audio; 9 document;
15/20 sticker; 4 contact; 5/16 location). Rows with no recognized content type **and** no body **and**
no media (system/placeholder) are skipped. All upserts go through the existing repos and are
idempotent by `(session_id, wa_message_id)` — re-running an import merges rather than duplicates and
reconciles with live capture.

## Quota & concurrency (`backfill_imports`, [`store.md`](store.md))

The `backfill_imports` table is both the job-status surface and the durable quota source of truth.

- **Concurrency:** a `running` row created within the last hour blocks a new import (`409`); an older
  one is presumed crashed and no longer holds the lock.
- **Quota:** non-`super_admin` callers are limited to one **successful** import per 24h per session
  (`429`). Failed/running attempts don't burn the quota. `super_admin` bypasses both the quota and
  the org-ownership check (may import into any session). Quota is gated on the last *success*, so a
  decrypt-or-parse failure costs nothing.

## Limits / out of scope

- Media is metadata-only (mimetype/size/filename captured into `media_meta`; files are not
  downloaded, consistent with v2).
- `poll_votes` are not imported.
- Upload cap ~256 MiB (`maxBackupUpload`); decryption holds the file in memory, the parse streams.

## How it's tested

- `internal/backup`: crypt15 encrypt→decrypt round-trip (no fixture file needed), key-format
  parsing, type/server classification, and a skipped-unless-present integration test that reads the
  dev `web/msgstore.db`.
- `internal/store`: `go-sqlmock` over every `BackfillImportRepo` method.
- `internal/service`: ownership/decrypt/concurrency/quota branches + `importAll` upsert mapping with
  a fake reader.
- `internal/http/handlers`: multipart upload happy-path, missing key/file, 401, 429, status.

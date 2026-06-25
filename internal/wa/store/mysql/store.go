package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// mysqlStore implements every per-device whatsmeow store interface against MySQL.
// One instance exists per device JID; the JID is the row key (our_jid / jid).
type mysqlStore struct {
	db  *sql.DB
	log waLog.Logger
	jid string // device JID string, used as the per-row owner key

	preKeyLock   sync.Mutex
	contactLock  sync.Mutex
	contactCache map[types.JID]*types.ContactInfo
}

func newMysqlStore(db *sql.DB, log waLog.Logger, jid types.JID) *mysqlStore {
	return &mysqlStore{
		db:           db,
		log:          log,
		jid:          jid.String(),
		contactCache: make(map[types.JID]*types.ContactInfo),
	}
}

// Compile-time assertions: mysqlStore must satisfy every per-device interface.
var (
	_ store.IdentityStore            = (*mysqlStore)(nil)
	_ store.SessionStore             = (*mysqlStore)(nil)
	_ store.PreKeyStore              = (*mysqlStore)(nil)
	_ store.SenderKeyStore           = (*mysqlStore)(nil)
	_ store.AppStateSyncKeyStore     = (*mysqlStore)(nil)
	_ store.AppStateStore            = (*mysqlStore)(nil)
	_ store.ContactStore             = (*mysqlStore)(nil)
	_ store.ChatSettingsStore        = (*mysqlStore)(nil)
	_ store.MsgSecretStore           = (*mysqlStore)(nil)
	_ store.PrivacyTokenStore        = (*mysqlStore)(nil)
	_ store.NCTSaltStore             = (*mysqlStore)(nil)
	_ store.EventBuffer              = (*mysqlStore)(nil)
	_ store.AllSessionSpecificStores = (*mysqlStore)(nil)
)

// ---------------------------------------------------------------------------
// IdentityStore
// ---------------------------------------------------------------------------

const (
	putIdentityQuery = `
		INSERT INTO wmstore_identity_keys (our_jid, their_id, identity) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE identity=VALUES(identity)
	`
	deleteAllIdentitiesQuery = `DELETE FROM wmstore_identity_keys WHERE our_jid=? AND their_id LIKE ?`
	deleteIdentityQuery      = `DELETE FROM wmstore_identity_keys WHERE our_jid=? AND their_id=?`
	getIdentityQuery         = `SELECT identity FROM wmstore_identity_keys WHERE our_jid=? AND their_id=?`
)

func (s *mysqlStore) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	_, err := s.db.ExecContext(ctx, putIdentityQuery, s.jid, address, key[:])
	return err
}

func (s *mysqlStore) DeleteAllIdentities(ctx context.Context, phone string) error {
	_, err := s.db.ExecContext(ctx, deleteAllIdentitiesQuery, s.jid, phone+":%")
	return err
}

func (s *mysqlStore) DeleteIdentity(ctx context.Context, address string) error {
	_, err := s.db.ExecContext(ctx, deleteIdentityQuery, s.jid, address)
	return err
}

func (s *mysqlStore) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	var existing []byte
	err := s.db.QueryRowContext(ctx, getIdentityQuery, s.jid, address).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown identities are trusted; they get saved automatically afterwards.
		return true, nil
	} else if err != nil {
		return false, err
	} else if len(existing) != 32 {
		return false, ErrInvalidLength
	}
	return *(*[32]byte)(existing) == key, nil
}

// ---------------------------------------------------------------------------
// SessionStore
// ---------------------------------------------------------------------------

const (
	getSessionQuery = `SELECT session FROM wmstore_sessions WHERE our_jid=? AND their_id=?`
	hasSessionQuery = `SELECT true FROM wmstore_sessions WHERE our_jid=? AND their_id=?`
	putSessionQuery = `
		INSERT INTO wmstore_sessions (our_jid, their_id, session) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE session=VALUES(session)
	`
	deleteAllSessionsQuery = `DELETE FROM wmstore_sessions WHERE our_jid=? AND their_id LIKE ?`
	deleteSessionQuery     = `DELETE FROM wmstore_sessions WHERE our_jid=? AND their_id=?`
)

func (s *mysqlStore) GetSession(ctx context.Context, address string) (session []byte, err error) {
	err = s.db.QueryRowContext(ctx, getSessionQuery, s.jid, address).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *mysqlStore) HasSession(ctx context.Context, address string) (has bool, err error) {
	err = s.db.QueryRowContext(ctx, hasSessionQuery, s.jid, address).Scan(&has)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *mysqlStore) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	args := make([]any, len(addresses)+1)
	placeholders := make([]string, len(addresses))
	args[0] = s.jid
	for i, addr := range addresses {
		args[i+1] = addr
		placeholders[i] = "?"
	}
	query := fmt.Sprintf(
		`SELECT their_id, session FROM wmstore_sessions WHERE our_jid=? AND their_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	result := make(map[string][]byte, len(addresses))
	for _, addr := range addresses {
		result[addr] = nil
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var addr string
		var sess []byte
		if err := rows.Scan(&addr, &sess); err != nil {
			return nil, err
		}
		result[addr] = sess
	}
	return result, rows.Err()
}

func (s *mysqlStore) PutSession(ctx context.Context, address string, session []byte) error {
	_, err := s.db.ExecContext(ctx, putSessionQuery, s.jid, address, session)
	return err
}

func (s *mysqlStore) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	return s.inTxn(ctx, func(tx *sql.Tx) error {
		for addr, sess := range sessions {
			if _, err := tx.ExecContext(ctx, putSessionQuery, s.jid, addr, sess); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *mysqlStore) DeleteAllSessions(ctx context.Context, phone string) error {
	_, err := s.db.ExecContext(ctx, deleteAllSessionsQuery, s.jid, phone+":%")
	return err
}

func (s *mysqlStore) DeleteSession(ctx context.Context, address string) error {
	_, err := s.db.ExecContext(ctx, deleteSessionQuery, s.jid, address)
	return err
}

const (
	migratePNToLIDSessionsQuery = `
		INSERT INTO wmstore_sessions (our_jid, their_id, session)
		SELECT our_jid, REPLACE(their_id, ?, ?), session
		FROM wmstore_sessions
		WHERE our_jid=? AND their_id LIKE CONCAT(?, ':%')
		ON DUPLICATE KEY UPDATE session=VALUES(session)
	`
	migratePNToLIDIdentityKeysQuery = `
		INSERT INTO wmstore_identity_keys (our_jid, their_id, identity)
		SELECT our_jid, REPLACE(their_id, ?, ?), identity
		FROM wmstore_identity_keys
		WHERE our_jid=? AND their_id LIKE CONCAT(?, ':%')
		ON DUPLICATE KEY UPDATE identity=VALUES(identity)
	`
	migratePNToLIDSenderKeysQuery = `
		INSERT INTO wmstore_sender_keys (our_jid, chat_id, sender_id, sender_key)
		SELECT our_jid, chat_id, REPLACE(sender_id, ?, ?), sender_key
		FROM wmstore_sender_keys
		WHERE our_jid=? AND sender_id LIKE CONCAT(?, ':%')
		ON DUPLICATE KEY UPDATE sender_key=VALUES(sender_key)
	`
	deleteAllSenderKeysQuery = `DELETE FROM wmstore_sender_keys WHERE our_jid=? AND sender_id LIKE ?`
)

// MigratePNToLID rewrites all signal addresses for a phone number to the new LID
// identity (sessions, identity keys, sender keys), then drops the stale PN rows.
func (s *mysqlStore) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	pnSignal := pn.SignalAddressUser()
	lidSignal := lid.SignalAddressUser()
	return s.inTxn(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, migratePNToLIDSessionsQuery, pnSignal, lidSignal, s.jid, pnSignal); err != nil {
			return fmt.Errorf("failed to migrate sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, deleteAllSessionsQuery, s.jid, pnSignal+":%"); err != nil {
			return fmt.Errorf("failed to delete extra sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, migratePNToLIDIdentityKeysQuery, pnSignal, lidSignal, s.jid, pnSignal); err != nil {
			return fmt.Errorf("failed to migrate identity keys: %w", err)
		}
		if _, err := tx.ExecContext(ctx, deleteAllIdentitiesQuery, s.jid, pnSignal+":%"); err != nil {
			return fmt.Errorf("failed to delete extra identity keys: %w", err)
		}
		if _, err := tx.ExecContext(ctx, migratePNToLIDSenderKeysQuery, pnSignal, lidSignal, s.jid, pnSignal); err != nil {
			return fmt.Errorf("failed to migrate sender keys: %w", err)
		}
		if _, err := tx.ExecContext(ctx, deleteAllSenderKeysQuery, s.jid, pnSignal+":%"); err != nil {
			return fmt.Errorf("failed to delete extra sender keys: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// PreKeyStore
// ---------------------------------------------------------------------------

const (
	getLastPreKeyIDQuery        = `SELECT MAX(key_id) FROM wmstore_pre_keys WHERE jid=?`
	insertPreKeyQuery           = `INSERT INTO wmstore_pre_keys (jid, key_id, ` + "`key`" + `, uploaded) VALUES (?, ?, ?, ?)`
	getUnuploadedPreKeysQuery   = `SELECT key_id, ` + "`key`" + ` FROM wmstore_pre_keys WHERE jid=? AND uploaded=false ORDER BY key_id LIMIT ?`
	getPreKeyQuery              = `SELECT key_id, ` + "`key`" + ` FROM wmstore_pre_keys WHERE jid=? AND key_id=?`
	deletePreKeyQuery           = `DELETE FROM wmstore_pre_keys WHERE jid=? AND key_id=?`
	markPreKeysAsUploadedQuery  = `UPDATE wmstore_pre_keys SET uploaded=true WHERE jid=? AND key_id<=?`
	getUploadedPreKeyCountQuery = `SELECT COUNT(*) FROM wmstore_pre_keys WHERE jid=? AND uploaded=true`
)

func (s *mysqlStore) genOnePreKey(ctx context.Context, q querier, id uint32, markUploaded bool) (*keys.PreKey, error) {
	key := keys.NewPreKey(id)
	_, err := q.ExecContext(ctx, insertPreKeyQuery, s.jid, key.KeyID, key.Priv[:], markUploaded)
	return key, err
}

func (s *mysqlStore) getNextPreKeyID(ctx context.Context, q querier) (uint32, error) {
	var lastKeyID sql.NullInt64
	err := q.QueryRowContext(ctx, getLastPreKeyIDQuery, s.jid).Scan(&lastKeyID)
	if err != nil {
		return 0, fmt.Errorf("failed to query next prekey ID: %w", err)
	}
	return uint32(lastKeyID.Int64) + 1, nil
}

func (s *mysqlStore) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	s.preKeyLock.Lock()
	defer s.preKeyLock.Unlock()
	nextID, err := s.getNextPreKeyID(ctx, s.db)
	if err != nil {
		return nil, err
	}
	return s.genOnePreKey(ctx, s.db, nextID, true)
}

func (s *mysqlStore) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	s.preKeyLock.Lock()
	defer s.preKeyLock.Unlock()

	rows, err := s.db.QueryContext(ctx, getUnuploadedPreKeysQuery, s.jid, count)
	if err != nil {
		return nil, fmt.Errorf("failed to query existing prekeys: %w", err)
	}
	var newKeys []*keys.PreKey
	for rows.Next() {
		k, serr := scanPreKey(rows)
		if serr != nil {
			rows.Close()
			return nil, serr
		}
		newKeys = append(newKeys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	already := uint32(len(newKeys))
	if count > already {
		nextID, err := s.getNextPreKeyID(ctx, s.db)
		if err != nil {
			return nil, err
		}
		for i := already; i < count; i++ {
			k, gerr := s.genOnePreKey(ctx, s.db, nextID, false)
			if gerr != nil {
				return nil, fmt.Errorf("failed to generate prekey: %w", gerr)
			}
			newKeys = append(newKeys, k)
			nextID++
		}
	}
	return newKeys, nil
}

func scanPreKey(row scannable) (*keys.PreKey, error) {
	var priv []byte
	var id uint32
	err := row.Scan(&id, &priv)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	} else if len(priv) != 32 {
		return nil, ErrInvalidLength
	}
	return &keys.PreKey{
		KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(priv)),
		KeyID:   id,
	}, nil
}

func (s *mysqlStore) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	k, err := scanPreKey(s.db.QueryRowContext(ctx, getPreKeyQuery, s.jid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (s *mysqlStore) RemovePreKey(ctx context.Context, id uint32) error {
	_, err := s.db.ExecContext(ctx, deletePreKeyQuery, s.jid, id)
	return err
}

func (s *mysqlStore) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	_, err := s.db.ExecContext(ctx, markPreKeysAsUploadedQuery, s.jid, upToID)
	return err
}

func (s *mysqlStore) UploadedPreKeyCount(ctx context.Context) (count int, err error) {
	err = s.db.QueryRowContext(ctx, getUploadedPreKeyCountQuery, s.jid).Scan(&count)
	return
}

// ---------------------------------------------------------------------------
// SenderKeyStore
// ---------------------------------------------------------------------------

const (
	getSenderKeyQuery = `SELECT sender_key FROM wmstore_sender_keys WHERE our_jid=? AND chat_id=? AND sender_id=?`
	putSenderKeyQuery = `
		INSERT INTO wmstore_sender_keys (our_jid, chat_id, sender_id, sender_key) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE sender_key=VALUES(sender_key)
	`
)

func (s *mysqlStore) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	_, err := s.db.ExecContext(ctx, putSenderKeyQuery, s.jid, group, user, session)
	return err
}

func (s *mysqlStore) GetSenderKey(ctx context.Context, group, user string) (key []byte, err error) {
	err = s.db.QueryRowContext(ctx, getSenderKeyQuery, s.jid, group, user).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

// ---------------------------------------------------------------------------
// AppStateSyncKeyStore
// ---------------------------------------------------------------------------

const (
	// The timestamp guard keeps the newest key when the same id arrives twice.
	putAppStateSyncKeyQuery = `
		INSERT INTO wmstore_app_state_sync_keys (jid, key_id, key_data, timestamp, fingerprint) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			key_data=IF(VALUES(timestamp) > timestamp, VALUES(key_data), key_data),
			fingerprint=IF(VALUES(timestamp) > timestamp, VALUES(fingerprint), fingerprint),
			timestamp=IF(VALUES(timestamp) > timestamp, VALUES(timestamp), timestamp)
	`
	getAllAppStateSyncKeysQuery     = `SELECT key_data, timestamp, fingerprint FROM wmstore_app_state_sync_keys WHERE jid=? ORDER BY timestamp DESC`
	getAppStateSyncKeyQuery         = `SELECT key_data, timestamp, fingerprint FROM wmstore_app_state_sync_keys WHERE jid=? AND key_id=?`
	getLatestAppStateSyncKeyIDQuery = `SELECT key_id FROM wmstore_app_state_sync_keys WHERE jid=? ORDER BY timestamp DESC LIMIT 1`
)

func (s *mysqlStore) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	_, err := s.db.ExecContext(ctx, putAppStateSyncKeyQuery, s.jid, id, key.Data, key.Timestamp, key.Fingerprint)
	return err
}

func scanAppStateSyncKey(row scannable) (*store.AppStateSyncKey, error) {
	var item store.AppStateSyncKey
	if err := row.Scan(&item.Data, &item.Timestamp, &item.Fingerprint); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *mysqlStore) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	rows, err := s.db.QueryContext(ctx, getAllAppStateSyncKeysQuery, s.jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.AppStateSyncKey
	for rows.Next() {
		k, serr := scanAppStateSyncKey(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *mysqlStore) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	k, err := scanAppStateSyncKey(s.db.QueryRowContext(ctx, getAppStateSyncKeyQuery, s.jid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (s *mysqlStore) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	var keyID []byte
	err := s.db.QueryRowContext(ctx, getLatestAppStateSyncKeyIDQuery, s.jid).Scan(&keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return keyID, err
}

// ---------------------------------------------------------------------------
// AppStateStore
// ---------------------------------------------------------------------------

const (
	putAppStateVersionQuery = `
		INSERT INTO wmstore_app_state_version (jid, name, version, hash) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE version=VALUES(version), hash=VALUES(hash)
	`
	getAppStateVersionQuery    = `SELECT version, hash FROM wmstore_app_state_version WHERE jid=? AND name=?`
	deleteAppStateVersionQuery = `DELETE FROM wmstore_app_state_version WHERE jid=? AND name=?`

	putAppStateMutationMACQuery = `
		INSERT INTO wmstore_app_state_mutation_macs (jid, name, version, index_mac, value_mac) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE value_mac=VALUES(value_mac)
	`
	getAppStateMutationMACQuery = `SELECT value_mac FROM wmstore_app_state_mutation_macs WHERE jid=? AND name=? AND index_mac=? ORDER BY version DESC LIMIT 1`
)

func (s *mysqlStore) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	_, err := s.db.ExecContext(ctx, putAppStateVersionQuery, s.jid, name, version, hash[:])
	return err
}

func (s *mysqlStore) GetAppStateVersion(ctx context.Context, name string) (version uint64, hash [128]byte, err error) {
	var raw []byte
	err = s.db.QueryRowContext(ctx, getAppStateVersionQuery, s.jid, name).Scan(&version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		// 0 version + empty hash is the correct "not yet synced" state.
		err = nil
	} else if err != nil {
		// return as-is
	} else if len(raw) != 128 {
		err = ErrInvalidLength
	} else if version == 0 {
		err = fmt.Errorf("invalid saved app state version 0 for name %s (hash %x)", name, raw)
	} else {
		hash = *(*[128]byte)(raw)
	}
	return
}

func (s *mysqlStore) DeleteAppStateVersion(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, deleteAppStateVersionQuery, s.jid, name)
	return err
}

func (s *mysqlStore) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	if len(mutations) == 0 {
		return nil
	}
	return s.inTxn(ctx, func(tx *sql.Tx) error {
		for _, m := range mutations {
			if _, err := tx.ExecContext(ctx, putAppStateMutationMACQuery, s.jid, name, version, m.IndexMAC, m.ValueMAC); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *mysqlStore) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	if len(indexMACs) == 0 {
		return nil
	}
	args := make([]any, 2+len(indexMACs))
	args[0] = s.jid
	args[1] = name
	placeholders := make([]string, len(indexMACs))
	for i, im := range indexMACs {
		args[2+i] = im
		placeholders[i] = "?"
	}
	query := fmt.Sprintf(
		`DELETE FROM wmstore_app_state_mutation_macs WHERE jid=? AND name=? AND index_mac IN (%s)`,
		strings.Join(placeholders, ","),
	)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *mysqlStore) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) (valueMAC []byte, err error) {
	err = s.db.QueryRowContext(ctx, getAppStateMutationMACQuery, s.jid, name, indexMAC).Scan(&valueMAC)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

// ---------------------------------------------------------------------------
// ContactStore
// ---------------------------------------------------------------------------

const (
	putContactNameQuery = `
		INSERT INTO wmstore_contacts (our_jid, their_jid, first_name, full_name) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE first_name=VALUES(first_name), full_name=VALUES(full_name)
	`
	putRedactedPhoneQuery = `
		INSERT INTO wmstore_contacts (our_jid, their_jid, redacted_phone) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE redacted_phone=VALUES(redacted_phone)
	`
	putPushNameQuery = `
		INSERT INTO wmstore_contacts (our_jid, their_jid, push_name) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE push_name=VALUES(push_name)
	`
	putBusinessNameQuery = `
		INSERT INTO wmstore_contacts (our_jid, their_jid, business_name) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE business_name=VALUES(business_name)
	`
	getContactQuery     = `SELECT first_name, full_name, push_name, business_name, redacted_phone FROM wmstore_contacts WHERE our_jid=? AND their_jid=?`
	getAllContactsQuery = `SELECT their_jid, first_name, full_name, push_name, business_name, redacted_phone FROM wmstore_contacts WHERE our_jid=?`
)

func (s *mysqlStore) PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error) {
	s.contactLock.Lock()
	defer s.contactLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return false, "", err
	}
	if cached.PushName != pushName {
		if _, err = s.db.ExecContext(ctx, putPushNameQuery, s.jid, user, pushName); err != nil {
			return false, "", err
		}
		prev := cached.PushName
		cached.PushName = pushName
		cached.Found = true
		return true, prev, nil
	}
	return false, "", nil
}

func (s *mysqlStore) PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error) {
	s.contactLock.Lock()
	defer s.contactLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return false, "", err
	}
	if cached.BusinessName != businessName {
		if _, err = s.db.ExecContext(ctx, putBusinessNameQuery, s.jid, user, businessName); err != nil {
			return false, "", err
		}
		prev := cached.BusinessName
		cached.BusinessName = businessName
		cached.Found = true
		return true, prev, nil
	}
	return false, "", nil
}

func (s *mysqlStore) PutContactName(ctx context.Context, user types.JID, fullName, firstName string) error {
	s.contactLock.Lock()
	defer s.contactLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return err
	}
	if cached.FirstName != firstName || cached.FullName != fullName {
		if _, err = s.db.ExecContext(ctx, putContactNameQuery, s.jid, user, firstName, fullName); err != nil {
			return err
		}
		cached.FirstName = firstName
		cached.FullName = fullName
		cached.Found = true
	}
	return nil
}

func (s *mysqlStore) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	if len(contacts) == 0 {
		return nil
	}
	err := s.inTxn(ctx, func(tx *sql.Tx) error {
		for _, c := range contacts {
			if _, err := tx.ExecContext(ctx, putContactNameQuery, s.jid, c.JID, c.FirstName, c.FullName); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.contactLock.Lock()
	s.contactCache = make(map[types.JID]*types.ContactInfo)
	s.contactLock.Unlock()
	return nil
}

func (s *mysqlStore) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	if len(entries) == 0 {
		return nil
	}
	err := s.inTxn(ctx, func(tx *sql.Tx) error {
		for _, e := range entries {
			if _, err := tx.ExecContext(ctx, putRedactedPhoneQuery, s.jid, e.JID, e.RedactedPhone); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.contactLock.Lock()
	for _, e := range entries {
		delete(s.contactCache, e.JID)
	}
	s.contactLock.Unlock()
	return nil
}

// getContact must be called with contactLock held.
func (s *mysqlStore) getContact(ctx context.Context, user types.JID) (*types.ContactInfo, error) {
	if cached, ok := s.contactCache[user]; ok {
		return cached, nil
	}
	var first, full, push, business, redacted sql.NullString
	err := s.db.QueryRowContext(ctx, getContactQuery, s.jid, user).Scan(&first, &full, &push, &business, &redacted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	info := &types.ContactInfo{
		Found:         err == nil,
		FirstName:     first.String,
		FullName:      full.String,
		PushName:      push.String,
		BusinessName:  business.String,
		RedactedPhone: redacted.String,
	}
	s.contactCache[user] = info
	return info, nil
}

func (s *mysqlStore) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	s.contactLock.Lock()
	info, err := s.getContact(ctx, user)
	s.contactLock.Unlock()
	if err != nil {
		return types.ContactInfo{}, err
	}
	return *info, nil
}

func (s *mysqlStore) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	s.contactLock.Lock()
	defer s.contactLock.Unlock()
	rows, err := s.db.QueryContext(ctx, getAllContactsQuery, s.jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[types.JID]types.ContactInfo)
	for rows.Next() {
		var jid types.JID
		var first, full, push, business, redacted sql.NullString
		if err := rows.Scan(&jid, &first, &full, &push, &business, &redacted); err != nil {
			return nil, err
		}
		info := &types.ContactInfo{
			Found:         true,
			FirstName:     first.String,
			FullName:      full.String,
			PushName:      push.String,
			BusinessName:  business.String,
			RedactedPhone: redacted.String,
		}
		out[jid] = *info
		s.contactCache[jid] = info
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// ChatSettingsStore
// ---------------------------------------------------------------------------

const getChatSettingsQuery = `SELECT muted_until, pinned, archived FROM wmstore_chat_settings WHERE our_jid=? AND chat_jid=?`

// putChatSettingQuery has a single mutable column injected via fmt; the column
// name is one of a fixed allowlist (muted_until/pinned/archived), never user input.
const putChatSettingQuery = `
	INSERT INTO wmstore_chat_settings (our_jid, chat_jid, %[1]s) VALUES (?, ?, ?)
	ON DUPLICATE KEY UPDATE %[1]s=VALUES(%[1]s)
`

func (s *mysqlStore) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	var val int64
	if mutedUntil == store.MutedForever {
		val = -1
	} else if !mutedUntil.IsZero() {
		val = mutedUntil.Unix()
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(putChatSettingQuery, "muted_until"), s.jid, chat, val)
	return err
}

func (s *mysqlStore) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(putChatSettingQuery, "pinned"), s.jid, chat, pinned)
	return err
}

func (s *mysqlStore) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(putChatSettingQuery, "archived"), s.jid, chat, archived)
	return err
}

func (s *mysqlStore) GetChatSettings(ctx context.Context, chat types.JID) (settings types.LocalChatSettings, err error) {
	var mutedUntil int64
	err = s.db.QueryRowContext(ctx, getChatSettingsQuery, s.jid, chat).Scan(&mutedUntil, &settings.Pinned, &settings.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err != nil {
		return
	} else {
		settings.Found = true
	}
	if mutedUntil < 0 {
		settings.MutedUntil = store.MutedForever
	} else if mutedUntil > 0 {
		settings.MutedUntil = time.Unix(mutedUntil, 0)
	}
	return
}

// ---------------------------------------------------------------------------
// MsgSecretStore
// ---------------------------------------------------------------------------

const (
	putMsgSecretQuery = `
		INSERT INTO wmstore_message_secrets (our_jid, chat_jid, sender_jid, message_id, ` + "`key`" + `)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE message_id=message_id
	`
	// Resolve chat/sender JIDs across PN<->LID via the lid map, mirroring sqlstore.
	getMsgSecretQuery = `
		SELECT ` + "`key`" + `, sender_jid
		FROM wmstore_message_secrets
		WHERE our_jid=? AND (chat_jid=? OR chat_jid=(
			CASE
				WHEN ? LIKE '%@lid'
					THEN (SELECT CONCAT(pn, '@s.whatsapp.net') FROM wmstore_lid_map WHERE lid=REPLACE(?, '@lid', ''))
				WHEN ? LIKE '%@s.whatsapp.net'
					THEN (SELECT CONCAT(lid, '@lid') FROM wmstore_lid_map WHERE pn=REPLACE(?, '@s.whatsapp.net', ''))
			END
		)) AND message_id=? AND (sender_jid=? OR sender_jid=(
			CASE
				WHEN ? LIKE '%@lid'
					THEN (SELECT CONCAT(pn, '@s.whatsapp.net') FROM wmstore_lid_map WHERE lid=REPLACE(?, '@lid', ''))
				WHEN ? LIKE '%@s.whatsapp.net'
					THEN (SELECT CONCAT(lid, '@lid') FROM wmstore_lid_map WHERE pn=REPLACE(?, '@s.whatsapp.net', ''))
			END
		))
	`
)

func (s *mysqlStore) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	if len(inserts) == 0 {
		return nil
	}
	return s.inTxn(ctx, func(tx *sql.Tx) error {
		for _, ins := range inserts {
			if _, err := tx.ExecContext(ctx, putMsgSecretQuery, s.jid, ins.Chat.ToNonAD(), ins.Sender.ToNonAD(), ins.ID, ins.Secret); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *mysqlStore) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	_, err := s.db.ExecContext(ctx, putMsgSecretQuery, s.jid, chat.ToNonAD(), sender.ToNonAD(), id, secret)
	return err
}

func (s *mysqlStore) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) (secret []byte, realSender types.JID, err error) {
	chatStr := chat.ToNonAD().String()
	senderStr := sender.ToNonAD().String()
	err = s.db.QueryRowContext(ctx, getMsgSecretQuery,
		s.jid,
		chatStr, chatStr, chatStr, chatStr, chatStr,
		id,
		senderStr, senderStr, senderStr, senderStr, senderStr,
	).Scan(&secret, &realSender)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

// ---------------------------------------------------------------------------
// PrivacyTokenStore
// ---------------------------------------------------------------------------

const (
	putPrivacyTokenQuery = `
		INSERT INTO wmstore_privacy_tokens (our_jid, their_jid, token, timestamp, sender_timestamp)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			token=IF(VALUES(timestamp) >= timestamp, VALUES(token), token),
			sender_timestamp=IF(VALUES(timestamp) >= timestamp, COALESCE(VALUES(sender_timestamp), sender_timestamp), sender_timestamp),
			timestamp=IF(VALUES(timestamp) >= timestamp, VALUES(timestamp), timestamp)
	`
	getPrivacyTokenQuery = `
		SELECT token, timestamp, sender_timestamp FROM wmstore_privacy_tokens WHERE our_jid=? AND (their_jid=? OR their_jid=(
			CASE
				WHEN ? LIKE '%@lid'
					THEN (SELECT CONCAT(pn, '@s.whatsapp.net') FROM wmstore_lid_map WHERE lid=REPLACE(?, '@lid', ''))
				WHEN ? LIKE '%@s.whatsapp.net'
					THEN (SELECT CONCAT(lid, '@lid') FROM wmstore_lid_map WHERE pn=REPLACE(?, '@s.whatsapp.net', ''))
				ELSE ?
			END
		))
		ORDER BY timestamp DESC LIMIT 1
	`
	deleteExpiredPrivacyTokensQuery = `DELETE FROM wmstore_privacy_tokens WHERE our_jid=? AND timestamp < ?`
)

func (s *mysqlStore) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	if len(tokens) == 0 {
		return nil
	}
	return s.inTxn(ctx, func(tx *sql.Tx) error {
		for _, t := range tokens {
			var senderTS any
			if !t.SenderTimestamp.IsZero() {
				senderTS = t.SenderTimestamp.Unix()
			}
			if _, err := tx.ExecContext(ctx, putPrivacyTokenQuery, s.jid, t.User.ToNonAD().String(), t.Token, t.Timestamp.Unix(), senderTS); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *mysqlStore) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	token := store.PrivacyToken{User: user.ToNonAD()}
	userStr := token.User.String()
	var ts int64
	var senderTS sql.NullInt64
	err := s.db.QueryRowContext(ctx, getPrivacyTokenQuery,
		s.jid, userStr, userStr, userStr, userStr, userStr, userStr,
	).Scan(&token.Token, &ts, &senderTS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	token.Timestamp = time.Unix(ts, 0)
	if senderTS.Valid {
		token.SenderTimestamp = time.Unix(senderTS.Int64, 0)
	}
	return &token, nil
}

func (s *mysqlStore) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, deleteExpiredPrivacyTokensQuery, s.jid, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// NCTSaltStore
// ---------------------------------------------------------------------------

const (
	putNCTSaltQuery = `
		INSERT INTO wmstore_nct_salt (our_jid, salt) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE salt=VALUES(salt)
	`
	getNCTSaltQuery    = `SELECT salt FROM wmstore_nct_salt WHERE our_jid=?`
	deleteNCTSaltQuery = `DELETE FROM wmstore_nct_salt WHERE our_jid=?`
)

func (s *mysqlStore) PutNCTSalt(ctx context.Context, salt []byte) error {
	_, err := s.db.ExecContext(ctx, putNCTSaltQuery, s.jid, salt)
	return err
}

func (s *mysqlStore) GetNCTSalt(ctx context.Context) ([]byte, error) {
	var salt []byte
	err := s.db.QueryRowContext(ctx, getNCTSaltQuery, s.jid).Scan(&salt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return salt, nil
}

func (s *mysqlStore) DeleteNCTSalt(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, deleteNCTSaltQuery, s.jid)
	return err
}

// ---------------------------------------------------------------------------
// EventBuffer
// ---------------------------------------------------------------------------

const (
	getBufferedEventQuery            = `SELECT plaintext, server_timestamp, insert_timestamp FROM wmstore_event_buffer WHERE our_jid=? AND ciphertext_hash=?`
	putBufferedEventQuery            = `INSERT INTO wmstore_event_buffer (our_jid, ciphertext_hash, plaintext, server_timestamp, insert_timestamp) VALUES (?, ?, ?, ?, ?)`
	clearBufferedEventPlaintextQuery = `UPDATE wmstore_event_buffer SET plaintext=NULL WHERE our_jid=? AND ciphertext_hash=?`
	deleteOldBufferedHashesQuery     = `DELETE FROM wmstore_event_buffer WHERE insert_timestamp < ?`

	getOutgoingEventQuery = `SELECT format, plaintext FROM wmstore_retry_buffer WHERE our_jid=? AND (chat_jid=? OR chat_jid=?) AND message_id=?`
	addOutgoingEventQuery = `
		INSERT INTO wmstore_retry_buffer (our_jid, chat_jid, message_id, format, plaintext, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE format=VALUES(format), plaintext=VALUES(plaintext), timestamp=VALUES(timestamp)
	`
	deleteOldOutgoingEventsQuery = `DELETE FROM wmstore_retry_buffer WHERE our_jid=? AND timestamp < ?`
)

func (s *mysqlStore) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	var insertTimeMS, serverTimeSeconds int64
	var buf store.BufferedEvent
	err := s.db.QueryRowContext(ctx, getBufferedEventQuery, s.jid, ciphertextHash[:]).Scan(&buf.Plaintext, &serverTimeSeconds, &insertTimeMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	buf.ServerTime = time.Unix(serverTimeSeconds, 0)
	buf.InsertTime = time.UnixMilli(insertTimeMS)
	return &buf, nil
}

func (s *mysqlStore) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	_, err := s.db.ExecContext(ctx, putBufferedEventQuery, s.jid, ciphertextHash[:], plaintext, serverTimestamp.Unix(), time.Now().UnixMilli())
	return err
}

// DoDecryptionTxn runs fn inside a transaction. whatsmeow uses this to make a
// decrypt+buffer-update atomic; our database/sql txn is not propagated through
// ctx, so fn runs against the same DB (best-effort atomicity at the store level).
func (s *mysqlStore) DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error {
	return s.inTxn(ctx, func(*sql.Tx) error {
		return fn(ctx)
	})
}

func (s *mysqlStore) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	_, err := s.db.ExecContext(ctx, clearBufferedEventPlaintextQuery, s.jid, ciphertextHash[:])
	return err
}

func (s *mysqlStore) DeleteOldBufferedHashes(ctx context.Context) error {
	// WhatsApp buffers events for ~14 days, so anything older is safe to drop.
	_, err := s.db.ExecContext(ctx, deleteOldBufferedHashesQuery, time.Now().Add(-14*24*time.Hour).UnixMilli())
	return err
}

func (s *mysqlStore) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (format string, result []byte, err error) {
	err = s.db.QueryRowContext(ctx, getOutgoingEventQuery, s.jid, chatJID, altChatJID, id).Scan(&format, &result)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *mysqlStore) AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error {
	_, err := s.db.ExecContext(ctx, addOutgoingEventQuery, s.jid, chatJID, id, format, plaintext, time.Now().UnixMilli())
	return err
}

func (s *mysqlStore) DeleteOldOutgoingEvents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, deleteOldOutgoingEventsQuery, s.jid, time.Now().Add(-7*24*time.Hour).UnixMilli())
	return err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// querier is the subset of *sql.DB / *sql.Tx used by the prekey helpers.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTxn runs fn inside a transaction, rolling back on error.
func (s *mysqlStore) inTxn(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

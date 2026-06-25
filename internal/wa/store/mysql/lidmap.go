package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// lidMap is the process-wide phone-number <-> LID identity map. It is a GLOBAL
// store (one row per (lid, pn) user pair, no per-device ownership), mirroring
// sqlstore.CachedLIDMap. Rows store the bare User parts (no server suffix); the
// server is reattached when constructing the returned JID.
type lidMap struct {
	db *sql.DB

	lock    sync.RWMutex
	pnToLID map[string]string
	lidToPN map[string]string
}

var _ store.LIDStore = (*lidMap)(nil)

func newLIDMap(db *sql.DB) *lidMap {
	return &lidMap{
		db:      db,
		pnToLID: make(map[string]string),
		lidToPN: make(map[string]string),
	}
}

const (
	deleteExistingLIDMappingQuery = `DELETE FROM wmstore_lid_map WHERE lid<>? AND pn=?`
	putLIDMappingQuery            = `
		INSERT INTO wmstore_lid_map (lid, pn) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE pn=IF(pn<>VALUES(pn), VALUES(pn), pn)
	`
	getLIDForPNQuery = `SELECT lid FROM wmstore_lid_map WHERE pn=?`
	getPNForLIDQuery = `SELECT pn FROM wmstore_lid_map WHERE lid=?`
)

func (m *lidMap) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if pn.Server != types.DefaultUserServer {
		return types.JID{}, fmt.Errorf("invalid GetLIDForPN call with non-PN JID %s", pn)
	}
	return m.lookup(ctx, pn, types.HiddenUserServer, getLIDForPNQuery, m.pnToLID, m.lidToPN)
}

func (m *lidMap) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if lid.Server != types.HiddenUserServer {
		return types.JID{}, fmt.Errorf("invalid GetPNForLID call with non-LID JID %s", lid)
	}
	return m.lookup(ctx, lid, types.DefaultUserServer, getPNForLIDQuery, m.lidToPN, m.pnToLID)
}

func (m *lidMap) lookup(ctx context.Context, source types.JID, targetServer, query string, srcToTgt, tgtToSrc map[string]string) (types.JID, error) {
	m.lock.RLock()
	target, ok := srcToTgt[source.User]
	m.lock.RUnlock()
	if ok {
		if target == "" {
			return types.JID{}, nil
		}
		return types.JID{User: target, Device: source.Device, Server: targetServer}, nil
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	err := m.db.QueryRowContext(ctx, query, source.User).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		// leave target empty; cache the negative result
	} else if err != nil {
		return types.JID{}, err
	}
	srcToTgt[source.User] = target
	if target != "" {
		tgtToSrc[target] = source.User
		return types.JID{User: target, Device: source.Device, Server: targetServer}, nil
	}
	return types.JID{}, nil
}

func (m *lidMap) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	if len(pns) == 0 {
		return nil, nil
	}
	result := make(map[types.JID]types.JID, len(pns))

	m.lock.RLock()
	missing := make([]string, 0, len(pns))
	missingDevices := make(map[string][]types.JID)
	for _, pn := range pns {
		if pn.Server != types.DefaultUserServer {
			continue
		}
		if lidUser, ok := m.pnToLID[pn.User]; ok && lidUser != "" {
			result[pn] = types.JID{User: lidUser, Device: pn.Device, Server: types.HiddenUserServer}
		} else if !ok {
			missing = append(missing, pn.User)
			missingDevices[pn.User] = append(missingDevices[pn.User], pn)
		}
	}
	m.lock.RUnlock()

	if len(missing) == 0 {
		return result, nil
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	placeholders := make([]string, len(missing))
	args := make([]any, len(missing))
	for i, u := range missing {
		placeholders[i] = "?"
		args[i] = u
	}
	query := fmt.Sprintf(`SELECT lid, pn FROM wmstore_lid_map WHERE pn IN (%s)`, strings.Join(placeholders, ","))
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lidUser, pnUser string
		if err := rows.Scan(&lidUser, &pnUser); err != nil {
			return nil, err
		}
		m.pnToLID[pnUser] = lidUser
		m.lidToPN[lidUser] = pnUser
		for _, dev := range missingDevices[pnUser] {
			result[dev] = types.JID{User: lidUser, Device: dev.Device, Server: types.HiddenUserServer}.ToNonAD()
		}
	}
	return result, rows.Err()
}

func (m *lidMap) PutLIDMapping(ctx context.Context, lid, pn types.JID) error {
	if lid.Server != types.HiddenUserServer || pn.Server != types.DefaultUserServer {
		return fmt.Errorf("invalid PutLIDMapping call %s/%s", lid, pn)
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if cached, ok := m.pnToLID[pn.User]; ok && cached == lid.User {
		return nil
	}
	return m.inTxn(ctx, func(tx *sql.Tx) error {
		return m.unlockedPut(ctx, tx, lid, pn)
	})
}

func (m *lidMap) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	filtered := mappings[:0]
	seen := make(map[string]struct{}, len(mappings))
	for _, mp := range mappings {
		if mp.LID.Server != types.HiddenUserServer || mp.PN.Server != types.DefaultUserServer {
			continue
		}
		if cached, ok := m.pnToLID[mp.PN.User]; ok && cached == mp.LID.User {
			continue
		}
		if _, dup := seen[mp.PN.User]; dup {
			continue
		}
		seen[mp.PN.User] = struct{}{}
		filtered = append(filtered, mp)
	}
	if len(filtered) == 0 {
		return nil
	}
	return m.inTxn(ctx, func(tx *sql.Tx) error {
		for _, mp := range filtered {
			if err := m.unlockedPut(ctx, tx, mp.LID, mp.PN); err != nil {
				return err
			}
		}
		return nil
	})
}

// unlockedPut must be called with the write lock held.
func (m *lidMap) unlockedPut(ctx context.Context, tx *sql.Tx, lid, pn types.JID) error {
	if _, err := tx.ExecContext(ctx, deleteExistingLIDMappingQuery, lid.User, pn.User); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, putLIDMappingQuery, lid.User, pn.User); err != nil {
		return err
	}
	oldLID := m.pnToLID[pn.User]
	oldPN := m.lidToPN[lid.User]
	m.pnToLID[pn.User] = lid.User
	m.lidToPN[lid.User] = pn.User
	if oldPN != "" && oldPN != pn.User && m.pnToLID[oldPN] == lid.User {
		delete(m.pnToLID, oldPN)
	}
	if oldLID != "" && oldLID != lid.User && m.lidToPN[oldLID] == pn.User {
		delete(m.lidToPN, oldLID)
	}
	return nil
}

func (m *lidMap) inTxn(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
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

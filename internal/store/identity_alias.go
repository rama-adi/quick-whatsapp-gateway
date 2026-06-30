package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

func canonicalDMChatJID(ctx context.Context, db dbExecQuerier, chatJID string) (string, error) {
	if !strings.HasSuffix(chatJID, "@s.whatsapp.net") {
		return chatJID, nil
	}
	lids, err := storedb.New(db).ResolveCanonicalDMChatJID(ctx, storedb.ResolveCanonicalDMChatJIDParams{
		PhoneJid: sqlString(chatJID),
	})
	if err != nil {
		return "", fmt.Errorf("store: resolve chat alias: %w", err)
	}
	if len(lids) != 1 {
		return chatJID, nil
	}
	return lids[0], nil
}

func mergeDMChatAlias(ctx context.Context, db dbExecQuerier, sessionID, lid, phoneJID string) error {
	if lid == "" || phoneJID == "" || lid == phoneJID {
		return nil
	}
	if !strings.HasSuffix(lid, "@lid") || !strings.HasSuffix(phoneJID, "@s.whatsapp.net") {
		return nil
	}
	q := storedb.New(db)
	if sessionID == "" {
		if err := q.MergeExistingDMChatAliases(ctx, storedb.MergeExistingDMChatAliasesParams{ChatJid: phoneJID, ChatJid_2: lid}); err != nil {
			return fmt.Errorf("store: merge chat aliases: %w", err)
		}
		if err := q.DeleteMergedDMChatAliases(ctx, storedb.DeleteMergedDMChatAliasesParams{ChatJid: lid, ChatJid_2: phoneJID}); err != nil {
			return fmt.Errorf("store: delete merged chat aliases: %w", err)
		}
		if err := q.RenameDMChatAliasesWithoutCanonical(ctx, storedb.RenameDMChatAliasesWithoutCanonicalParams{ChatJid: lid, ChatJid_2: phoneJID, ChatJid_3: lid}); err != nil {
			return fmt.Errorf("store: rename chat aliases: %w", err)
		}
		if err := q.UpdateMessageDMChatAliases(ctx, storedb.UpdateMessageDMChatAliasesParams{ChatJid: lid, ChatJid_2: phoneJID}); err != nil {
			return fmt.Errorf("store: update message chat aliases: %w", err)
		}
		if err := q.UpdatePollDMChatAliases(ctx, storedb.UpdatePollDMChatAliasesParams{ChatJid: lid, ChatJid_2: phoneJID}); err != nil {
			return fmt.Errorf("store: update poll chat aliases: %w", err)
		}
		return nil
	}

	_, err := q.GetCanonicalChatIDForAliasMerge(ctx, storedb.GetCanonicalChatIDForAliasMergeParams{SessionID: sessionID, ChatJid: lid})
	switch {
	case err == nil:
		if err := q.MergeSessionDMChatAlias(ctx, storedb.MergeSessionDMChatAliasParams{ChatJid: phoneJID, SessionID: sessionID, ChatJid_2: lid}); err != nil {
			return fmt.Errorf("store: merge chat alias: %w", err)
		}
		if err := q.DeleteSessionDMChatAlias(ctx, storedb.DeleteSessionDMChatAliasParams{SessionID: sessionID, ChatJid: phoneJID}); err != nil {
			return fmt.Errorf("store: delete chat alias: %w", err)
		}
	case err == sql.ErrNoRows:
		if err := q.RenameSessionDMChatAlias(ctx, storedb.RenameSessionDMChatAliasParams{ChatJid: lid, SessionID: sessionID, ChatJid_2: phoneJID}); err != nil {
			return fmt.Errorf("store: rename chat alias: %w", err)
		}
	default:
		return fmt.Errorf("store: check chat alias: %w", err)
	}

	if err := q.UpdateSessionMessageDMChatAliases(ctx, storedb.UpdateSessionMessageDMChatAliasesParams{ChatJid: lid, SessionID: sessionID, ChatJid_2: phoneJID}); err != nil {
		return fmt.Errorf("store: update message chat alias: %w", err)
	}
	if err := q.UpdateSessionPollDMChatAliases(ctx, storedb.UpdateSessionPollDMChatAliasesParams{ChatJid: lid, SessionID: sessionID, ChatJid_2: phoneJID}); err != nil {
		return fmt.Errorf("store: update poll chat alias: %w", err)
	}
	return nil
}

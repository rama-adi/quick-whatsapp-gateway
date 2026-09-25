package outbound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"

	"go.mau.fi/whatsmeow/types"
)

type commandMessageIDKey struct{}

type commandMessageIDSequence struct {
	commandID string
	next      atomic.Uint32
}

// WithCommandMessageIDs binds WhatsApp message IDs to a durable gateway command.
// Replaying the command after a process crash uses the same IDs, including each
// child of a multi-message operation such as an album.
func WithCommandMessageIDs(ctx context.Context, commandID string) context.Context {
	if commandID == "" {
		return ctx
	}
	return context.WithValue(ctx, commandMessageIDKey{}, &commandMessageIDSequence{commandID: commandID})
}

func nextCommandMessageID(ctx context.Context) types.MessageID {
	sequence, ok := ctx.Value(commandMessageIDKey{}).(*commandMessageIDSequence)
	if !ok {
		return ""
	}
	ordinal := sequence.next.Add(1) - 1
	digest := sha256.Sum256([]byte(sequence.commandID + "/" + strconv.FormatUint(uint64(ordinal), 10)))
	return types.MessageID("3EB0" + strings.ToUpper(hex.EncodeToString(digest[:9])))
}

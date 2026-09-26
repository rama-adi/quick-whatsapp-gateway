package outbound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"

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
	return commandMessageID(sequence.commandID, ordinal)
}

func commandMessageID(commandID string, ordinal uint32) types.MessageID {
	digest := sha256.Sum256([]byte(commandID + "/" + strconv.FormatUint(uint64(ordinal), 10)))
	return types.MessageID("3EB0" + strings.ToUpper(hex.EncodeToString(digest[:9])))
}

// ReservedMessageIDs names the wire messages reserved by a durable send command.
// The list is a reservation, not a delivery receipt: an uncertain album may
// have delivered only some children. Keep this beside the actual ID allocator.
func ReservedMessageIDs(commandID string, req domain.SendRequest) []string {
	count := 1
	if req.Type == domain.SendTypeAlbum {
		count += len(req.Medias)
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = string(commandMessageID(commandID, uint32(i)))
	}
	return ids
}

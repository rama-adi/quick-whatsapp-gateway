package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// defaultLimit / maxLimit bound cursor pagination so a missing or absurd ?limit
// can't pull an unbounded result set.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// notFound maps a database/sql ErrNoRows into a domain not_found APIError so the
// HTTP edge can render the §11 error envelope. Any other error is wrapped with
// %w and returned unchanged. resource names the missing thing for the message.
func notFound(err error, resource string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound(resource + " not found")
	}
	return err
}

// normLimit clamps a caller-supplied page size into [1, maxLimit], defaulting to
// defaultLimit when zero/negative.
func normLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

// parseCursor decodes an opaque list cursor into the numeric id it wraps. The
// cursor is just the decimal id as a string (opaque to callers, who must not
// parse it themselves). Empty cursor means "from the start". A malformed cursor
// is a client error → validation_error.
func parseCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, domain.ErrValidation("invalid cursor")
	}
	return id, nil
}

// encodeCursor produces the opaque cursor for a given id. It is the inverse of
// parseCursor. Returns "" for id 0 (no next page).
func encodeCursor(id uint64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

// parseStringCursor accepts an opaque sortable string cursor. Empty cursor means
// "from the start"; cursors with whitespace are rejected as malformed.
func parseStringCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if strings.TrimSpace(cursor) != cursor {
		return "", domain.ErrValidation("invalid cursor")
	}
	return cursor, nil
}

// Page is the generic result of a cursor-paginated list query: the items plus
// the cursor to pass as ?cursor= for the next page ("" when exhausted).
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// pageFrom builds a Page from a fetched slice. The cursor is the id of the last
// item when the page filled to `limit` (signaling a possible next page) and ""
// otherwise. idOf extracts the surrogate id used for the opaque cursor.
func pageFrom[T any](items []T, limit int, idOf func(T) uint64) Page[T] {
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = encodeCursor(idOf(items[len(items)-1]))
	}
	return Page[T]{Items: items, NextCursor: next}
}

// pageFromString is pageFrom for string primary keys such as ULID cursors.
func pageFromString[T any](items []T, limit int, idOf func(T) string) Page[T] {
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = idOf(items[len(items)-1])
	}
	return Page[T]{Items: items, NextCursor: next}
}

// prefixCols qualifies a comma-separated column list with a table alias, e.g.
// prefixCols("c", "id, lid") => "c.id, c.lid". Used when a list query joins and
// the bare column names would be ambiguous. It tolerates the multi-line, tab/
// newline-formatted column constants used in this package.
func prefixCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// scanErr wraps a row-scan failure with the table/op context for clearer logs.
func scanErr(table string, err error) error {
	return fmt.Errorf("store: scan %s: %w", table, err)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so a single scan<T>
// helper can serve QueryRow lookups and Query loops.
type rowScanner interface {
	Scan(dest ...any) error
}

// nullableJSON returns nil (→ SQL NULL) for an empty JSON byte slice, otherwise
// the bytes themselves. Use for nullable JSON columns so an absent value round-
// trips as NULL rather than an empty string the driver might reject.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// affectedOrNotFound turns an UPDATE/DELETE that matched zero rows into a domain
// not_found, so callers get a consistent error for "the row wasn't there".
func affectedOrNotFound(res sql.Result, resource string) error {
	n, err := res.RowsAffected()
	if err != nil {
		// Driver doesn't report affected rows; treat the exec as succeeded
		// rather than guessing not_found.
		return nil
	}
	if n == 0 {
		return domain.ErrNotFound(resource + " not found")
	}
	return nil
}

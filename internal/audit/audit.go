// Package audit writes the audit trail (audit_log).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// Entry is one audit record. Action names are dotted, for example
// auth.login, setup.admin_created or listing.created.
type Entry struct {
	ActorID *int64    // nil for the CLI and the system
	Action  string    //
	Target  string    // what was acted on, "" when nothing
	Details any       // marshalled to JSON; nil means {}
	At      time.Time // zero means now
}

// Write appends e. Pass a Queries bound to a transaction to make the entry
// part of it.
func Write(ctx context.Context, q *dbq.Queries, e Entry) error {
	details := []byte("{}")
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("audit: details: %w", err)
		}
		details = b
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	var actor sql.NullInt64
	if e.ActorID != nil {
		actor = sql.NullInt64{Int64: *e.ActorID, Valid: true}
	}
	err := q.InsertAudit(ctx, dbq.InsertAuditParams{
		Ts: at.Unix(), ActorID: actor, Action: e.Action, Target: e.Target, Details: string(details),
	})
	if err != nil {
		return fmt.Errorf("audit: write %s: %w", e.Action, err)
	}
	return nil
}

package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

func TestWrite(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	q := dbq.New(d)
	actor := int64(7)
	at := time.Unix(1_700_000_000, 0)
	if err := Write(ctx, q, Entry{ActorID: &actor, Action: "auth.login", Target: "user:7", Details: map[string]string{"ip": "1.2.3.4"}, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := Write(ctx, q, Entry{Action: "user.created_cli", At: at}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`SELECT ts, actor_id, action, target, details FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var ts int64
		var actorID *int64
		var action, target, details string
		if err := rows.Scan(&ts, &actorID, &action, &target, &details); err != nil {
			t.Fatal(err)
		}
		if !json.Valid([]byte(details)) || ts != at.Unix() {
			t.Fatalf("row %q: ts %d details %q", action, ts, details)
		}
		got = append(got, action+"|"+target+"|"+details)
		if action == "user.created_cli" && actorID != nil {
			t.Fatal("CLI entry has an actor")
		}
	}
	want := []string{`auth.login|user:7|{"ip":"1.2.3.4"}`, `user.created_cli||{}`}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows %q, want %q", got, want)
	}
}

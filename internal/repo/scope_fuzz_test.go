package repo

import (
	"slices"
	"strconv"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/pathx"
)

var fuzzNames = []string{"a", "b", "é", "\xff", "x y"}

// FuzzScope compares Node against an oracle: a directory is visible to a user
// iff one of the user's paths is a segment prefix of the directory's path;
// admins see everything.
func FuzzScope(f *testing.F) {
	f.Add([]byte{3, 0, 1, 0, 2, 1, 1, 2, 1, 0, 1, 0})
	f.Add([]byte{})
	f.Add([]byte{9, 9, 9, 9, 9, 9, 9, 9, 3, 1, 1, 1, 1, 1, 1})
	f.Add([]byte{1, 4, 4, 4, 4, 4, 4, 4, 2, 2, 0, 0, 0, 0})
	f.Add([]byte{5, 0, 0, 1, 1, 2, 2, 3, 3, 2, 0, 1, 1, 0})
	f.Add([]byte{7, 1, 2, 3, 4, 0, 1, 2, 1, 4, 3, 2, 1, 0, 0, 0})
	f.Add([]byte{20, 0, 1, 2, 3, 4, 0, 1, 2, 3, 4, 3, 3, 3, 3, 3, 3, 3, 3, 3})
	f.Add([]byte{2, 3, 3, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		next := func() int {
			if len(data) == 0 {
				return 0
			}
			v := int(data[0])
			data = data[1:]
			return v
		}
		// Tree: up to 60 dirs, each below an earlier one.
		rels := []string{""}
		seen := map[string]bool{"": true}
		for range next() % 60 {
			parent := rels[next()%len(rels)]
			rel := fuzzNames[next()%len(fuzzNames)]
			if parent != "" {
				rel = parent + "/" + rel
			}
			if !seen[rel] {
				seen[rel] = true
				rels = append(rels, rel)
			}
		}
		var dirs []gentest.Dir
		for _, r := range rels[1:] {
			dirs = append(dirs, gentest.Dir{Rel: r, Files: 1, BytesAlloc: 1})
		}
		fx := newFixture(t)
		fx.scan(1, dirs...)

		// Users with 0..3 paths each; some paths do not exist in the tree.
		type account struct {
			id    int64
			paths [][]string
		}
		var users []account
		for range 1 + next()%4 {
			u := account{id: fx.user("u" + strconv.Itoa(len(users)))}
			for range next() % 4 {
				var rel string
				if i := next() % (len(rels) + 1); i < len(rels) {
					rel = rels[i]
				} else {
					rel = "zz/" + fuzzNames[next()%len(fuzzNames)]
				}
				res, err := fx.db.Exec(`INSERT OR IGNORE INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, ?, 1)`, u.id, []byte(rel))
				if err != nil {
					t.Fatal(err)
				}
				if n, _ := res.RowsAffected(); n == 1 {
					u.paths = append(u.paths, pathx.Segments(rel))
				}
			}
			users = append(users, u)
		}

		for _, u := range users {
			s := fx.scope(u.id, 1)
			for _, rel := range rels {
				want := oracleVisible(u.paths, pathx.Segments(rel))
				_, err := fx.repo.Node(t.Context(), s, fx.ref(1, rel))
				if want != (err == nil) || (err != nil && err != ErrNotFound) {
					t.Fatalf("user paths %q, node %q: visible %v, err %v", u.paths, rel, want, err)
				}
			}
		}
		for _, rel := range rels {
			if _, err := fx.repo.Node(t.Context(), ScopeAll(), fx.ref(1, rel)); err != nil {
				t.Fatalf("admin cannot see %q: %v", rel, err)
			}
		}
	})
}

func oracleVisible(paths [][]string, node []string) bool {
	for _, p := range paths {
		if len(p) <= len(node) && slices.Equal(p, node[:len(p)]) {
			return true
		}
	}
	return false
}

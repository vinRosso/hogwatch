package web

import (
	"context"
	"net/http"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// errLastAdmin is the problem of every change that would leave nobody able to
// administer the instance.
var errLastAdmin = &apiError{status: http.StatusConflict, detail: "that would leave no enabled administrator"}

// enabledAdmins counts the enabled admins who can sign in. Without OIDC an
// admin_oidc flag grants nothing, so it counts only when oidcOn; a
// local admin without a password counts only when OIDC is on and it is linked.
// Call it after the mutation, inside its transaction: 0 means refuse.
func enabledAdmins(ctx context.Context, q *dbq.Queries, oidcOn bool) (int64, error) {
	var on int64
	if oidcOn {
		on = 1
	}
	return q.CountLoginAdmins(ctx, on)
}

// requireAdminLeft returns errLastAdmin when no enabled administrator is left.
func (a *server) requireAdminLeft(ctx context.Context, q *dbq.Queries) error {
	n, err := enabledAdmins(ctx, q, a.oidcEnabled())
	if err != nil {
		return err
	}
	if n == 0 {
		return errLastAdmin
	}
	return nil
}

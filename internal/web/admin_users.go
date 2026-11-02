package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const (
	usersPageSize   = 100
	maxEmailBytes   = 320
	maxDisplayBytes = 256
)

// idCursor is the position after a page of rows ordered by id.
type idCursor struct {
	V  int   `json:"v"`
	ID int64 `json:"id"`
}

func adminUserOf(r dbq.GetAdminUserRow) oapi.AdminUser {
	u := oapi.AdminUser{
		Id: r.ID, Username: r.Username, DisplayName: r.DisplayName,
		AdminLocal: r.AdminLocal != 0, AdminOidc: r.AdminOidc != 0, CanListFiles: r.CanListFiles != 0,
		QuotaBasis: oapi.AdminUserQuotaBasis(r.QuotaBasis), Disabled: r.Disabled != 0,
		OidcLinked: r.OidcLinked != 0, HasPassword: r.HasPassword != 0, CreatedAt: r.CreatedAt,
	}
	if r.Email.Valid {
		u.Email = &r.Email.String
	}
	if r.QuotaBytes.Valid {
		u.QuotaBytes = &r.QuotaBytes.Int64
	}
	if r.LastLoginAt.Valid {
		u.LastLoginAt = &r.LastLoginAt.Int64
	}
	return u
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// parseEmail trims s and checks it. "" means no email.
func parseEmail(s string) (sql.NullString, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}, nil
	}
	at := strings.LastIndex(s, "@")
	if len(s) > maxEmailBytes || at < 1 || at == len(s)-1 || strings.Count(s, "@") != 1 || !utf8.ValidString(s) ||
		strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return sql.NullString{}, badRequest("invalid email")
	}
	return sql.NullString{String: s, Valid: true}, nil
}

func checkDisplay(s string) error {
	if len(s) > maxDisplayBytes || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return badRequest("invalid display name")
	}
	return nil
}

func checkQuota(b *int64, basis string) error {
	if b != nil && *b < 0 {
		return badRequest("quota_bytes must not be negative")
	}
	if basis != "allocated" && basis != "logical" {
		return badRequest("quota_basis must be allocated or logical")
	}
	return nil
}

// conflictOnUnique turns a users unique violation into a 409.
func conflictOnUnique(err error) error {
	switch {
	case uniqueOn(err, "users.username"):
		return &apiError{status: http.StatusConflict, detail: "username already exists"}
	case uniqueOn(err, "users.email"):
		return &apiError{status: http.StatusConflict, detail: "email already in use"}
	}
	return err
}

func (a *server) ListUsers(ctx context.Context, req oapi.ListUsersRequestObject) (oapi.ListUsersResponseObject, error) {
	var after int64
	if req.Params.Cursor != nil {
		var c idCursor
		if decodeCursor(*req.Params.Cursor, &c) != nil || c.V != cursorVersion || c.ID < 0 {
			return nil, badRequest("invalid cursor")
		}
		after = c.ID
	}
	rows, err := a.q.ListUsersPage(ctx, dbq.ListUsersPageParams{AfterID: after, Lim: usersPageSize + 1})
	if err != nil {
		return nil, err
	}
	out := oapi.ListUsers200JSONResponse{Items: []oapi.AdminUser{}}
	for i, r := range rows {
		if i == usersPageSize {
			next := encodeCursor(idCursor{V: cursorVersion, ID: rows[i-1].ID})
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, adminUserOf(dbq.GetAdminUserRow(r)))
	}
	return out, nil
}

func (a *server) GetUser(ctx context.Context, req oapi.GetUserRequestObject) (oapi.GetUserResponseObject, error) {
	r, err := a.q.GetAdminUser(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	return oapi.GetUser200JSONResponse(adminUserOf(r)), nil
}

func (a *server) CreateUser(ctx context.Context, req oapi.CreateUserRequestObject) (oapi.CreateUserResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("invalid request")
	}
	name := normUsername(b.Username)
	if !local.ValidUsername(name) {
		return nil, badRequest("invalid username")
	}
	if err := a.checkNewPassword(b.Password); err != nil {
		return nil, err
	}
	display := name
	if b.DisplayName != nil && *b.DisplayName != "" {
		display = *b.DisplayName
	}
	if err := checkDisplay(display); err != nil {
		return nil, err
	}
	var email sql.NullString
	if b.Email != nil {
		var err error
		if email, err = parseEmail(*b.Email); err != nil {
			return nil, err
		}
	}
	basis := "allocated"
	if b.QuotaBasis != nil {
		basis = string(*b.QuotaBasis)
	}
	if err := checkQuota(b.QuotaBytes, basis); err != nil {
		return nil, err
	}
	adminLocal := b.AdminLocal != nil && *b.AdminLocal
	canList := b.CanListFiles != nil && *b.CanListFiles
	hash, err := local.Hash(b.Password)
	if err != nil {
		return nil, err
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	id, err := q.InsertUser(ctx, dbq.InsertUserParams{
		Username: name, DisplayName: display, Email: email, PasswordHash: sql.NullString{String: hash, Valid: true},
		AdminLocal: b2i(adminLocal), CanListFiles: b2i(canList), CreatedAt: a.now().Unix(),
	})
	if err != nil {
		return nil, conflictOnUnique(err)
	}
	if b.QuotaBytes != nil || basis != "allocated" {
		quota := sql.NullInt64{}
		if b.QuotaBytes != nil {
			quota = sql.NullInt64{Int64: *b.QuotaBytes, Valid: true}
		}
		if _, err := q.UpdateUserAdmin(ctx, dbq.UpdateUserAdminParams{
			DisplayName: display, Email: email, AdminLocal: b2i(adminLocal), CanListFiles: b2i(canList),
			QuotaBytes: quota, QuotaBasis: basis, ID: id,
		}); err != nil {
			return nil, err
		}
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "user.created", Target: name,
		Details: map[string]any{"admin_local": adminLocal, "can_list_files": canList}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	row, err := q.GetAdminUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.CreateUser201JSONResponse(adminUserOf(row)), nil
}

func (a *server) UpdateUser(ctx context.Context, req oapi.UpdateUserRequestObject) (oapi.UpdateUserResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("invalid request")
	}
	emailPresent, emailNull := patchField(ctx, "email")
	quotaPresent, quotaNull := patchField(ctx, "quota_bytes")

	var newHash string
	if b.Password != nil {
		if err := a.checkNewPassword(*b.Password); err != nil {
			return nil, err
		}
		h, err := local.Hash(*b.Password)
		if err != nil {
			return nil, err
		}
		newHash = h
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	cur, err := q.GetAdminUser(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}

	p := dbq.UpdateUserAdminParams{
		DisplayName: cur.DisplayName, Email: cur.Email, AdminLocal: cur.AdminLocal,
		CanListFiles: cur.CanListFiles, QuotaBytes: cur.QuotaBytes, QuotaBasis: cur.QuotaBasis,
		Disabled: cur.Disabled, ID: cur.ID,
	}
	var fields []string
	revoke := false
	if b.DisplayName != nil {
		if err := checkDisplay(*b.DisplayName); err != nil {
			return nil, err
		}
		p.DisplayName = strings.TrimSpace(*b.DisplayName)
		if p.DisplayName == "" {
			p.DisplayName = cur.Username
		}
		fields = append(fields, "display_name")
	}
	if emailPresent {
		p.Email = sql.NullString{}
		if !emailNull && b.Email != nil {
			var err error
			if p.Email, err = parseEmail(*b.Email); err != nil {
				return nil, err
			}
		}
		fields = append(fields, "email")
	}
	if b.AdminLocal != nil {
		if b2i(*b.AdminLocal) != cur.AdminLocal {
			if cur.ID == admin.ID {
				return nil, &apiError{status: http.StatusConflict, detail: "cannot change your own role"}
			}
			revoke = true
		}
		p.AdminLocal = b2i(*b.AdminLocal)
		fields = append(fields, "admin_local")
	}
	if b.CanListFiles != nil {
		p.CanListFiles = b2i(*b.CanListFiles)
		fields = append(fields, "can_list_files")
	}
	if quotaPresent {
		p.QuotaBytes = sql.NullInt64{}
		if !quotaNull && b.QuotaBytes != nil {
			p.QuotaBytes = sql.NullInt64{Int64: *b.QuotaBytes, Valid: true}
		}
		fields = append(fields, "quota_bytes")
	}
	if b.QuotaBasis != nil {
		p.QuotaBasis = string(*b.QuotaBasis)
		fields = append(fields, "quota_basis")
	}
	if b.Disabled != nil {
		if *b.Disabled {
			if cur.ID == admin.ID {
				return nil, &apiError{status: http.StatusConflict, detail: "cannot disable your own account"}
			}
			revoke = true
		}
		p.Disabled = b2i(*b.Disabled)
		fields = append(fields, "disabled")
	}
	if b.Password != nil {
		revoke = true
		fields = append(fields, "password")
	}
	if len(fields) == 0 {
		return nil, badRequest("no fields to change")
	}
	var quotaVal *int64
	if p.QuotaBytes.Valid {
		quotaVal = &p.QuotaBytes.Int64
	}
	if err := checkQuota(quotaVal, p.QuotaBasis); err != nil {
		return nil, err
	}

	if _, err := q.UpdateUserAdmin(ctx, p); err != nil {
		return nil, conflictOnUnique(err)
	}
	if b.Password != nil {
		if _, err := q.SetPassword(ctx, dbq.SetPasswordParams{PasswordHash: sql.NullString{String: newHash, Valid: true}, ID: cur.ID}); err != nil {
			return nil, err
		}
	}
	if revoke {
		if _, err := q.DeleteUserSessions(ctx, cur.ID); err != nil {
			return nil, err
		}
	}
	if err := a.requireAdminLeft(ctx, q); err != nil {
		return nil, err
	}
	slices.Sort(fields)
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "user.updated", Target: cur.Username,
		Details: map[string]any{"fields": fields}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	row, err := q.GetAdminUser(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.UpdateUser200JSONResponse(adminUserOf(row)), nil
}

func (a *server) DeleteUser(ctx context.Context, req oapi.DeleteUserRequestObject) (oapi.DeleteUserResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Id == admin.ID {
		return nil, &apiError{status: http.StatusConflict, detail: "cannot delete your own account"}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	cur, err := q.GetAdminUser(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	// Sessions, paths and path_stats cascade; usage has no foreign key.
	if err := q.DeleteUsageForUserAll(ctx, cur.ID); err != nil {
		return nil, err
	}
	if _, err := q.DeleteUser(ctx, cur.ID); err != nil {
		return nil, err
	}
	if err := a.requireAdminLeft(ctx, q); err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{ActorID: &admin.ID, Action: "user.deleted", Target: cur.Username, At: a.now()})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.DeleteUser204Response{}, nil
}

func (a *server) RevokeUserSessions(ctx context.Context, req oapi.RevokeUserSessionsRequestObject) (oapi.RevokeUserSessionsResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	cur, err := q.GetAdminUser(ctx, req.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	n, err := q.DeleteUserSessions(ctx, cur.ID)
	if err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "user.sessions_revoked", Target: cur.Username,
		Details: map[string]any{"sessions": n}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.RevokeUserSessions204Response{}, nil
}

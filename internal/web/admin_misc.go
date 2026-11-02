package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/settings"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

const auditPageSize = 100

var extPattern = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

func (a *server) ListCategories(ctx context.Context, _ oapi.ListCategoriesRequestObject) (oapi.ListCategoriesResponseObject, error) {
	rows, err := a.q.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	out := oapi.ListCategories200JSONResponse{Items: make([]oapi.Category, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, oapi.Category{Ext: r.Ext, Category: oapi.CategoryName(r.Category)})
	}
	return out, nil
}

func (a *server) PutCategory(ctx context.Context, req oapi.PutCategoryRequestObject) (oapi.PutCategoryResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if !extPattern.MatchString(req.Ext) {
		return nil, badRequest("invalid extension")
	}
	if req.Body == nil || !oapi.CategoryName(req.Body.Category).Valid() {
		return nil, badRequest("invalid category")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	if err := q.UpsertCategory(ctx, dbq.UpsertCategoryParams{Ext: req.Ext, Category: string(req.Body.Category)}); err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "category.put", Target: req.Ext,
		Details: map[string]any{"category": req.Body.Category}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.PutCategory200JSONResponse{Ext: req.Ext, Category: req.Body.Category}, nil
}

func (a *server) DeleteCategory(ctx context.Context, req oapi.DeleteCategoryRequestObject) (oapi.DeleteCategoryResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if !extPattern.MatchString(req.Ext) {
		return nil, badRequest("invalid extension")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	n, err := q.DeleteCategory(ctx, req.Ext)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, &apiError{status: http.StatusNotFound}
	}
	err = audit.Write(ctx, q, audit.Entry{ActorID: &admin.ID, Action: "category.deleted", Target: req.Ext, At: a.now()})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.DeleteCategory204Response{}, nil
}

func storedSettings(ctx context.Context, q *dbq.Queries) (map[string]string, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

func (a *server) GetSettings(ctx context.Context, _ oapi.GetSettingsRequestObject) (oapi.GetSettingsResponseObject, error) {
	m, err := storedSettings(ctx, a.q)
	if err != nil {
		return nil, err
	}
	return oapi.GetSettings200JSONResponse(settings.Effective(m)), nil
}

// UpdateSettings replaces the settings: a key absent from the body is deleted,
// which means its default. Nothing is written unless every key is valid.
func (a *server) UpdateSettings(ctx context.Context, req oapi.UpdateSettingsRequestObject) (oapi.UpdateSettingsResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Body == nil {
		return nil, badRequest("invalid request")
	}
	body := map[string]string(*req.Body)
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d, known := settings.Lookup(k)
		if !known {
			return nil, &apiError{status: http.StatusUnprocessableEntity, detail: "unknown setting " + k}
		}
		if err := d.Validate(body[k]); err != nil {
			return nil, &apiError{status: http.StatusUnprocessableEntity, detail: err.Error()}
		}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	stored, err := storedSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	before := settings.Effective(stored)
	for k := range stored {
		if _, keep := body[k]; !keep {
			if err := q.DeleteSetting(ctx, k); err != nil {
				return nil, err
			}
		}
	}
	for _, k := range keys {
		if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: k, Value: body[k]}); err != nil {
			return nil, err
		}
	}
	after := map[string]string{}
	for k, v := range body {
		after[k] = v
	}
	after = settings.Effective(after)
	changed := map[string][2]string{}
	for k, v := range after {
		if before[k] != v {
			changed[k] = [2]string{before[k], v}
		}
	}
	if len(changed) > 0 {
		err = audit.Write(ctx, q, audit.Entry{ActorID: &admin.ID, Action: "settings.updated", Target: "settings", Details: changed, At: a.now()})
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.UpdateSettings200JSONResponse(after), nil
}

func (a *server) ListAudit(ctx context.Context, req oapi.ListAuditRequestObject) (oapi.ListAuditResponseObject, error) {
	p := dbq.ListAuditPageParams{Lim: auditPageSize + 1}
	if req.Params.Cursor != nil {
		var c idCursor
		if decodeCursor(*req.Params.Cursor, &c) != nil || c.V != cursorVersion || c.ID < 1 {
			return nil, badRequest("invalid cursor")
		}
		p.BeforeID = c.ID
	}
	rows, err := a.q.ListAuditPage(ctx, p)
	if err != nil {
		return nil, err
	}
	var out oapi.AuditPage
	out.Items = make([]struct {
		Action  string                 `json:"action"`
		ActorId *int64                 `json:"actor_id"`
		Details map[string]interface{} `json:"details"`
		Id      int64                  `json:"id"`
		Target  string                 `json:"target"`
		Ts      int64                  `json:"ts"`
	}, 0, len(rows))
	for i, r := range rows {
		if i == auditPageSize {
			next := encodeCursor(idCursor{V: cursorVersion, ID: rows[i-1].ID})
			out.NextCursor = &next
			break
		}
		d := map[string]interface{}{}
		if json.Unmarshal([]byte(r.Details), &d) != nil || d == nil {
			d = map[string]interface{}{}
		}
		var actor *int64
		if r.ActorID != (sql.NullInt64{}) && r.ActorID.Valid {
			v := r.ActorID.Int64
			actor = &v
		}
		out.Items = append(out.Items, struct {
			Action  string                 `json:"action"`
			ActorId *int64                 `json:"actor_id"`
			Details map[string]interface{} `json:"details"`
			Id      int64                  `json:"id"`
			Target  string                 `json:"target"`
			Ts      int64                  `json:"ts"`
		}{Action: r.Action, ActorId: actor, Details: d, Id: r.ID, Target: r.Target, Ts: r.Ts})
	}
	return oapi.ListAudit200JSONResponse(out), nil
}

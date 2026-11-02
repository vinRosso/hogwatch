package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/fname"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/repo"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

var extRE = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

const (
	maxExts       = 20
	maxMinAgeDays = 36500
)

func badRequest(detail string) error { return &apiError{status: http.StatusBadRequest, detail: detail} }

func (a *server) CreateListing(ctx context.Context, req oapi.CreateListingRequestObject) (oapi.CreateListingResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("invalid request")
	}
	if !b.Mode.Valid() {
		return nil, badRequest("invalid mode")
	}
	p := db.ListingParams{Mode: string(b.Mode)}
	if b.Exts != nil {
		if len(*b.Exts) > maxExts {
			return nil, badRequest("too many extensions")
		}
		for _, e := range *b.Exts {
			if !extRE.MatchString(e) {
				return nil, badRequest("invalid extension")
			}
		}
		p.Exts = *b.Exts
	}
	if b.MinSize != nil {
		if *b.MinSize < 0 {
			return nil, badRequest("invalid min_size")
		}
		p.MinSize = *b.MinSize
	}
	if b.MinAgeDays != nil {
		if *b.MinAgeDays < 0 || *b.MinAgeDays > maxMinAgeDays {
			return nil, badRequest("invalid min_age_days")
		}
		p.MinAgeDays = *b.MinAgeDays
	}
	ref, err := noderef.Parse(b.Ref)
	if err != nil {
		return nil, badRequest("invalid ref")
	}
	scope, err := a.scopeFor(ctx, u, ref.ScanID)
	if err != nil {
		return nil, err
	}
	id, err := a.repo.CreateListing(ctx, scope, u.ID, ref, p, a.now())
	switch {
	case errors.Is(err, repo.ErrTooManyListings):
		return nil, &apiError{status: http.StatusTooManyRequests, detail: "too many active listings"}
	case err != nil:
		return nil, err
	}
	if err := audit.Write(ctx, a.q, audit.Entry{ActorID: &u.ID, Action: "listing.created", Target: ref.String(), Details: p, At: a.now()}); err != nil {
		a.log.Error("audit", "err", err)
	}
	return oapi.CreateListing202JSONResponse(oapi.ListingCreated{Id: id, Status: oapi.ListingCreatedStatusQueued}), nil
}

type listingItem = struct {
	Alloc int64            `json:"alloc"`
	Mtime int64            `json:"mtime"`
	Name  oapi.EscapedName `json:"name"`
	Size  int64            `json:"size"`
	Type  string           `json:"type"`
	Uid   int64            `json:"uid"`
}

func (a *server) GetListing(ctx context.Context, req oapi.GetListingRequestObject) (oapi.GetListingResponseObject, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	var after int64
	if req.Params.Cursor != nil {
		var cur seqCursor
		if err := decodeCursor(string(*req.Params.Cursor), &cur); err != nil || cur.V != cursorVersion || cur.Job != req.Id || cur.Seq < 0 {
			return nil, badRequest("invalid cursor")
		}
		after = cur.Seq
	}
	p, err := a.repo.Listing(ctx, c, req.Id, after, repo.MaxListingPage, a.now())
	if err != nil {
		return nil, err
	}
	res := oapi.Listing{
		Id: p.ID, Status: oapi.ListingStatus(p.Status), Truncated: p.Truncated, Error: p.Error,
		Items: make([]listingItem, 0, len(p.Items)),
	}
	for _, x := range p.Items {
		res.Items = append(res.Items, listingItem{Alloc: x.Alloc, Mtime: x.Mtime, Name: fname.Escape(x.Name), Size: x.Size, Type: x.Type, Uid: x.UID})
	}
	if p.Next != 0 {
		s := encodeCursor(seqCursor{V: cursorVersion, Job: p.ID, Seq: p.Next})
		res.NextCursor = &s
	}
	return oapi.GetListing200JSONResponse(res), nil
}

func (a *server) CancelListing(ctx context.Context, req oapi.CancelListingRequestObject) (oapi.CancelListingResponseObject, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	inserted, err := a.repo.CancelListing(ctx, c, req.Id, a.now())
	if err != nil {
		return nil, err
	}
	if inserted {
		if err := audit.Write(ctx, a.q, audit.Entry{ActorID: &c.UserID, Action: "listing.cancelled", Target: strconv.FormatInt(req.Id, 10), Details: map[string]int64{"job_id": req.Id}, At: a.now()}); err != nil {
			return nil, err
		}
	}
	return oapi.CancelListing204Response{}, nil
}

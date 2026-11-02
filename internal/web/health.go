package web

import (
	"context"
	"errors"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// Healthz reports liveness: the database answers. The body carries no details.
func (s *server) Healthz(ctx context.Context, _ oapi.HealthzRequestObject) (oapi.HealthzResponseObject, error) {
	if err := s.db.PingContext(ctx); err != nil {
		s.log.Warn("healthz", "err", err)
		return oapi.Healthz503TextResponse("unavailable"), nil
	}
	return oapi.Healthz200TextResponse("ok"), nil
}

// Readyz reports readiness: no migration is pending and the scanner has
// written a heartbeat in the last minute.
func (s *server) Readyz(ctx context.Context, _ oapi.ReadyzRequestObject) (oapi.ReadyzResponseObject, error) {
	if err := s.ready(ctx); err != nil {
		s.log.Warn("readyz", "err", err)
		return oapi.Readyz503TextResponse("unavailable"), nil
	}
	return oapi.Readyz200TextResponse("ok"), nil
}

func (s *server) ready(ctx context.Context) error {
	pending, err := db.Pending(ctx, s.db)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("migrations pending")
	}
	st, err := s.q.GetScannerStatus(ctx)
	if err != nil {
		return err
	}
	if age := s.now().Sub(time.Unix(st.HeartbeatAt, 0)); age >= db.HeartbeatMaxAge {
		return errors.New("scanner heartbeat is stale")
	}
	return nil
}

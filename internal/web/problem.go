package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vinRosso/hogwatch/internal/repo"
)

// ErrNotImplemented marks an operation of a later milestone.
var ErrNotImplemented = errors.New("web: not implemented")

// apiError is an error a handler wants shown to the client. detail is
// optional and must never carry a host path, SQL error or stack trace.
type apiError struct {
	status     int
	detail     string
	retryAfter time.Duration
}

func (e *apiError) Error() string { return http.StatusText(e.status) + ": " + e.detail }

// titles are fixed per status so a body never varies with the cause.
var titles = map[int]string{
	http.StatusBadRequest:            "Bad Request",
	http.StatusUnauthorized:          "Unauthorized",
	http.StatusForbidden:             "Forbidden",
	http.StatusNotFound:              "Not Found",
	http.StatusMethodNotAllowed:      "Method Not Allowed",
	http.StatusRequestEntityTooLarge: "Content Too Large",
	http.StatusUnsupportedMediaType:  "Unsupported Media Type",
	http.StatusConflict:              "Conflict",
	http.StatusUnprocessableEntity:   "Unprocessable Content",
	http.StatusTooManyRequests:       "Too Many Requests",
	http.StatusInternalServerError:   "Internal Server Error",
	http.StatusNotImplemented:        "Not Implemented",
	http.StatusServiceUnavailable:    "Service Unavailable",
}

type problemBody struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	title, ok := titles[status]
	if !ok {
		title = http.StatusText(status)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

// responseError maps an error returned by a strict handler to a problem.
func (a *server) responseError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		if ae.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((ae.retryAfter+time.Second-1)/time.Second)))
		}
		writeProblem(w, ae.status, ae.detail)
	case errors.Is(err, repo.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "")
	case errors.Is(err, ErrNotImplemented):
		writeProblem(w, http.StatusNotImplemented, "")
	case r.Context().Err() != nil && errors.Is(err, r.Context().Err()):
		// The client went away; nothing to report and nobody to answer.
		writeProblem(w, 499, "")
	default:
		a.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeProblem(w, http.StatusInternalServerError, "")
	}
}

// requestError handles what the generated wrapper could not bind or decode.
func requestError(w http.ResponseWriter, _ *http.Request, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "")
		return
	}
	writeProblem(w, http.StatusBadRequest, "invalid request")
}

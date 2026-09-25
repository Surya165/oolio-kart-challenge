// Package httpapi exposes the OpenAPI endpoints. Handlers stay thin: decode,
// call a service, map the result to a status code.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"kart/internal/catalog"
	"kart/internal/order"
)

// MaxBodyBytes caps request bodies.
const MaxBodyBytes = 1 << 20

// APIResponse matches the ApiResponse schema and is used for every error.
type APIResponse struct {
	Code    int    `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Server wires handlers to their dependencies.
type Server struct {
	Catalog catalog.Store
	Orders  *order.Service
	APIKey  string
	Log     *slog.Logger
	// Ready reports whether this instance should receive traffic. Nil means
	// always ready.
	Ready func() bool
}

// Handler returns the routed, middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/product", s.listProducts)
	mux.HandleFunc("GET /api/product/{productId}", s.getProduct)
	mux.Handle("POST /api/order", s.requireAPIKey(http.HandlerFunc(s.placeOrder)))
	// Liveness: the process is up. Readiness: it should get traffic (false
	// before startup finishes and once graceful shutdown starts).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.Ready != nil && !s.Ready() {
			writeError(w, http.StatusServiceUnavailable, "not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return s.recoverer(s.logRequests(jsonRouteErrors(mux)))
}

// jsonRouteErrors makes the router's own 404 (no such path) and 405 (path
// exists, wrong method) responses use the ApiResponse JSON shape like every
// other error, instead of net/http's plain-text defaults.
func jsonRouteErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r) // a route matched: normal path
			return
		}
		// No route matched. Run the mux's fallback handler against a
		// throwaway writer to learn the status (and Allow header for 405).
		c := &statusCapture{header: http.Header{}, status: http.StatusOK}
		h.ServeHTTP(c, r)
		if c.status != http.StatusNotFound && c.status != http.StatusMethodNotAllowed {
			h.ServeHTTP(w, r) // e.g. a redirect: pass through untouched
			return
		}
		if allow := c.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		writeError(w, c.status, http.StatusText(c.status))
	})
}

// statusCapture records a status code and headers and discards the body.
type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header         { return c.header }
func (c *statusCapture) Write(b []byte) (int, error) { return len(b), nil }
func (c *statusCapture) WriteHeader(code int)        { c.status = code }

func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	products, err := s.Catalog.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, products)
}

func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	// The spec types productId as int64 in the path, but as a string in the
	// Product schema. Parse to enforce the path type, then look up by the
	// canonical string form (so "007" finds "7").
	id, err := strconv.ParseInt(r.PathValue("productId"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "Invalid ID supplied")
		return
	}
	p, err := s.Catalog.Get(r.Context(), strconv.FormatInt(id, 10))
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Product not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	var req order.PlaceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid input: "+err.Error())
		return
	}
	o, err := s.Orders.Place(r.Context(), req)
	var verr *order.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusUnprocessableEntity, verr.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, o)
	}
}

// requireAPIKey: no key -> 401 (not authenticated); wrong key -> 403.
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("api_key")
		if key == "" {
			writeError(w, http.StatusUnauthorized, "api_key header is required")
			return
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.APIKey)) != 1 {
			writeError(w, http.StatusForbidden, "invalid api_key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errors.New("request body too large")
		}
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return err
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, APIResponse{Code: status, Type: errorType(status), Message: msg})
}

func errorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusServiceUnavailable:
		return "unavailable"
	case http.StatusUnprocessableEntity:
		return "validation_error"
	default:
		return "error"
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("panic", "value", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

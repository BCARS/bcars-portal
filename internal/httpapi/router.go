// Package httpapi builds the HTTP router and registers all Huma operations.
// It is intentionally thin: business logic lives in internal/domain/*.
package httpapi

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/bcars/bcars-portal/internal/obs"
	"github.com/bcars/bcars-portal/internal/web"
)

// Config holds all dependencies needed to assemble the HTTP router.
type Config struct {
	Logger  *slog.Logger
	Version string
	DB      *sql.DB // optional; enables admin UI when set
}

// NewRouter assembles the HTTP handler and Huma API.
//
//   - The raw net/http.ServeMux exposes /healthz and /readyz directly.
//   - The Huma API is mounted at /api/v1 via the humago adapter.
//   - obs.RequestIDMiddleware and IdempotencyMiddleware are applied to all
//     requests; Huma's RFC 9457 error transformer handles response errors.
//
// Callers must invoke RegisterAll(api) and then VerifyAll(api) before
// serving traffic.
func NewRouter(cfg Config) (http.Handler, huma.API) {
	mux := http.NewServeMux()

	// Health checks — no auth, no private data.
	mux.HandleFunc("GET /healthz", handleHealthz)
	// /readyz is a stub; WS8.1 wires in the DB reachability check.
	mux.HandleFunc("GET /readyz", handleReadyz)

	// Huma API at /api/v1/*.
	apiCfg := huma.DefaultConfig("BCARS Portal API", cfg.Version)
	apiCfg.Info.Description = "BCARS Members Portal — officers-only administrative API (Phase 1)."
	api := humago.NewWithPrefix(mux, "/api/v1", apiCfg)

	// Admin UI routes (server-rendered HTML).
	if cfg.DB != nil {
		webHandler, err := web.NewHandler(cfg.DB, cfg.Logger)
		if err != nil {
			cfg.Logger.Error("failed to initialize admin UI", slog.String("error", err.Error()))
		} else {
			webHandler.RegisterRoutes(mux)
		}
	}

	// Middleware chain: request-id → idempotency → mux.
	var handler http.Handler = mux
	handler = IdempotencyMiddleware(handler)
	handler = obs.RequestIDMiddleware(cfg.Logger, false)(handler)

	return handler, api
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func handleReadyz(w http.ResponseWriter, _ *http.Request) {
	// WS8.1: check DB reachability and schema version. For now, always 200.
	w.WriteHeader(http.StatusOK)
}

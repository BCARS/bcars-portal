// Package httpapi builds the HTTP router and registers all Huma operations.
// It is intentionally thin: business logic lives in internal/domain/*.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/obs"
	"github.com/bcars/bcars-portal/internal/web"
)

// ReadyzTimeout is the maximum time readyz waits for the database check.
const ReadyzTimeout = 3 * time.Second

// ExpectedMigrationVersion is the goose migration version the readyz check
// expects. Bump this when adding new migrations.
const ExpectedMigrationVersion = 6

// Config holds all dependencies needed to assemble the HTTP router.
type Config struct {
	Logger  *slog.Logger
	Version string
	DB      *sql.DB // optional; enables admin UI when set

	// Mailer and BaseURL are handed to the admin UI so its recovery and
	// invitation flows can actually deliver mail and generate links that
	// resolve. Leaving Mailer nil disables outbound mail from the UI.
	Mailer  mail.Sender
	BaseURL string

	// AllowInsecureCookies drops the Secure attribute from the admin UI's
	// session cookie. Development only, for serving the portal over
	// plaintext http://localhost; the zero value keeps Secure on.
	AllowInsecureCookies bool
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
	mux.HandleFunc("GET /readyz", makeReadyzHandler(cfg.DB))

	// Huma API at /api/v1/*.
	apiCfg := huma.DefaultConfig("BCARS Portal API", cfg.Version)
	apiCfg.Info.Description = "BCARS Members Portal — officers-only administrative API (Phase 1)."
	api := humago.NewWithPrefix(mux, "/api/v1", apiCfg)

	// Capability enforcement + generic audit. This must be installed before
	// RegisterAll: huma snapshots api.Middlewares() when each operation is
	// registered, so a middleware added afterwards would never run. Installing
	// it here means no assembly can serve traffic without enforcement.
	api.UseMiddleware(AuthzMiddleware(api, audit.NewSQLRecorder(cfg.DB, cfg.Logger)))

	// Admin UI routes (server-rendered HTML).
	if cfg.DB != nil {
		webHandler, err := web.NewHandler(cfg.DB, web.HandlerConfig{
			Logger:               cfg.Logger,
			Mailer:               cfg.Mailer,
			BaseURL:              cfg.BaseURL,
			AllowInsecureCookies: cfg.AllowInsecureCookies,
		})
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

// readyzResult is the JSON body returned by /readyz.
type readyzResult struct {
	Status           string `json:"status"` // "ok" or "unavailable"
	Reason           string `json:"reason,omitempty"`
	ForeignKeys      bool   `json:"foreign_keys"`
	JournalMode      string `json:"journal_mode"`
	MigrationVersion int64  `json:"migration_version"`
}

// makeReadyzHandler returns a handler that checks DB reachability, required
// SQLite pragmas, and the expected goose migration version. If db is nil the
// handler always returns 503.
func makeReadyzHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if db == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyzResult{
				Status: "unavailable",
				Reason: "no database configured",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), ReadyzTimeout)
		defer cancel()

		result := readyzResult{Status: "ok"}

		// Check DB reachability.
		if err := db.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyzResult{
				Status: "unavailable",
				Reason: "database unreachable",
			})
			return
		}

		// Check foreign_keys pragma.
		var fk int
		if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyzResult{
				Status: "unavailable",
				Reason: "cannot read foreign_keys pragma",
			})
			return
		}
		result.ForeignKeys = fk == 1

		// Check journal_mode pragma.
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&result.JournalMode); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyzResult{
				Status: "unavailable",
				Reason: "cannot read journal_mode pragma",
			})
			return
		}

		// Check migration version via goose_db_version table.
		err := db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1",
		).Scan(&result.MigrationVersion)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyzResult{
				Status: "unavailable",
				Reason: "cannot read migration version",
			})
			return
		}

		// Validate expected state.
		if !result.ForeignKeys {
			result.Status = "unavailable"
			result.Reason = "foreign_keys pragma is OFF"
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(result)
			return
		}

		if result.MigrationVersion != int64(ExpectedMigrationVersion) {
			result.Status = "unavailable"
			result.Reason = "schema version mismatch"
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(result)
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(result)
	}
}

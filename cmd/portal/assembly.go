package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/httpapi"
	"github.com/bcars/bcars-portal/internal/mail"
	"github.com/bcars/bcars-portal/internal/web"
)

// assemblyConfig holds everything the production HTTP assembly needs beyond
// the database handle.
type assemblyConfig struct {
	Logger       *slog.Logger
	Version      string
	CookieName   string
	SessionTTL   time.Duration
	BaseURL      string
	EmailLinkTTL time.Duration
	Mailer       mail.Sender

	// Pepper is the secret mixed into every password hash. AllowEmptyPepper
	// must be set explicitly to run without one.
	Pepper           []byte
	AllowEmptyPepper bool

	// AllowInsecureCookies drops the Secure attribute from session cookies
	// on both surfaces. Development only, for plaintext http://localhost.
	AllowInsecureCookies bool
	// TrustedProxyHeader names the forwarding header carrying the real client
	// address. Empty means the transport peer address is the only source; see
	// internal/httpapi/clientip.go for why that is the default.
	TrustedProxyHeader string
}

// buildHandler assembles the production HTTP handler: session store, auth
// service, email-link service, the Huma API with capability enforcement, and
// the authn.Middleware that resolves the session cookie into a Principal.
//
// This is the single definition of the server's wiring. main calls it and so
// does the production-assembly test — a test that rebuilt the chain itself
// would prove nothing about what actually ships, which is exactly how the
// missing authn.Middleware went unnoticed (bcars-portal-fmc.2).
func buildHandler(database *sql.DB, cfg assemblyConfig) (http.Handler, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	sessionStore := authn.NewSessionStore(database, authn.SessionConfig{
		CookieName: cfg.CookieName,
		TTL:        cfg.SessionTTL,
	})
	if err := authn.ValidatePepper(cfg.Pepper, cfg.AllowEmptyPepper); err != nil {
		return nil, err
	}
	// Refuses to start if this database's hashes were made with a different
	// pepper, rather than rejecting every sign-in as a bad password.
	if err := authn.BindPepper(database, cfg.Pepper); err != nil {
		return nil, err
	}
	authService := authn.NewAuthService(database, sessionStore, cfg.Pepper)

	emailLinks := authn.NewEmailLinkService(database, cfg.Mailer, authn.EmailLinkConfig{
		BaseURL: cfg.BaseURL,
		TTL:     cfg.EmailLinkTTL,
		// Wired from the web package's own route constants so an emailed
		// link cannot point at a path the router does not serve.
		RecoveryPath:   web.RouteResetPassword,
		InvitationPath: web.RouteInvitationConsume,
	})

	handler, api := httpapi.NewRouter(httpapi.Config{
		Logger:               cfg.Logger,
		Version:              cfg.Version,
		DB:                   database,
		Mailer:               cfg.Mailer,
		BaseURL:              cfg.BaseURL,
		AllowInsecureCookies: cfg.AllowInsecureCookies,
		Pepper:               cfg.Pepper,
		// The client-address hash key is derived from the password pepper
		// rather than from a secret of its own; the reasoning is recorded in
		// internal/httpapi/clientip.go.
		ClientIP: httpapi.ClientIPConfig{
			TrustedProxyHeader: cfg.TrustedProxyHeader,
			HashKey:            cfg.Pepper,
		},
	})
	httpapi.RegisterAll(api, httpapi.Deps{
		DB:               database,
		AuthService:      authService,
		SessionStore:     sessionStore,
		EmailLinkService: emailLinks,
		CookieName:       cfg.CookieName,
		EmailLinkTTL:     cfg.EmailLinkTTL,

		AllowInsecureCookies: cfg.AllowInsecureCookies,
	})

	// Startup check: every operation must have metadata.
	if err := httpapi.VerifyAll(api); err != nil {
		return nil, fmt.Errorf("startup check failed: %w", err)
	}

	// Resolve the session cookie into a Principal before the API sees the
	// request. Without this the capability middleware finds a nil principal
	// and every authenticated call fails with 401.
	capLoader := &authn.SQLCapabilityLoader{DB: database}
	cookies := authn.SessionCookieConfig{
		Name:          cfg.CookieName,
		AllowInsecure: cfg.AllowInsecureCookies,
	}
	return authn.Middleware(sessionStore, capLoader, cookies)(handler), nil
}

// mailConfig describes the configured outbound mail transport.
type mailConfig struct {
	Transport    string // "filelog" or "smtp"
	FilelogDir   string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPFrom     string
	SMTPPassword string
}

// newMailSender builds the mail sender named by cfg.Transport. The filelog
// transport writes each message as JSON under FilelogDir (created if missing)
// and is the default so a fresh deployment never silently drops recovery mail.
func newMailSender(cfg mailConfig) (mail.Sender, error) {
	switch cfg.Transport {
	case "filelog", "":
		if cfg.FilelogDir == "" {
			return nil, fmt.Errorf("mail-dir must be set when mail-transport=filelog")
		}
		if err := os.MkdirAll(cfg.FilelogDir, 0o750); err != nil {
			return nil, fmt.Errorf("create mail dir %s: %w", cfg.FilelogDir, err)
		}
		return mail.NewFilelogSender(cfg.FilelogDir), nil
	case "smtp":
		if cfg.SMTPHost == "" {
			return nil, fmt.Errorf("smtp-host must be set when mail-transport=smtp")
		}
		if cfg.SMTPFrom == "" {
			return nil, fmt.Errorf("smtp-from must be set when mail-transport=smtp")
		}
		return mail.NewSMTPSender(mail.SMTPConfig{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			User:     cfg.SMTPUser,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
		}), nil
	default:
		return nil, fmt.Errorf("unknown mail-transport %q (want filelog or smtp)", cfg.Transport)
	}
}

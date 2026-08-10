package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/authn"
	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// AuthzMiddleware enforces the RequiredCapability declared by each operation's
// OperationMeta and emits an audit event for every operation that declares an
// AuditAction, plus every denial.
//
// Enforcement lives here rather than in handlers on purpose: handlers that
// each remember to check drift out of sync with their declared metadata, which
// is exactly how every direct-query handler ended up authentication-only. With
// this middleware the declaration in Register IS the enforcement.
//
// It must be installed before any operation is registered — huma resolves
// api.Middlewares() at Register time, not at request time. NewRouter installs
// it so callers cannot forget.
func AuthzMiddleware(api huma.API, rec audit.Recorder) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		opID := ""
		if op != nil {
			opID = op.OperationID
		}

		meta, ok := lookupMeta(opID)
		if !ok {
			// Fail closed. VerifyAll makes this unreachable at startup, but an
			// operation that slipped past it must not become a public one.
			recordEvent(ctx, rec, meta, "operation.unregistered", audit.OutcomeDenied,
				audit.ReasonNoMetadata, 0, http.StatusForbidden)
			_ = huma.WriteErr(api, ctx, http.StatusForbidden, "operation is not authorized")
			return
		}

		principal := authn.PrincipalFrom(ctx.Context())

		if meta.RequiredCapability != PublicCapability {
			// Delegate to the policy layer so the catalog check and the
			// default-deny semantics live in exactly one place.
			var actorID int64
			if principal != nil {
				actorID = principal.UserID
			}
			switch err := authz.Authorize(ctx.Context(), toAuthzPrincipal(principal), meta.RequiredCapability, nil); {
			case errors.Is(err, authz.ErrUnauthenticated):
				recordEvent(ctx, rec, meta, denialAction(meta, opID), audit.OutcomeDenied,
					audit.ReasonUnauthenticated, actorID, http.StatusUnauthorized)
				_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "not authenticated")
				return
			case err != nil:
				recordEvent(ctx, rec, meta, denialAction(meta, opID), audit.OutcomeDenied,
					audit.ReasonMissingCapability, actorID, http.StatusForbidden)
				_ = huma.WriteErr(api, ctx, http.StatusForbidden, "insufficient capabilities")
				return
			}
		}

		// Confirmation, enforced from the same declaration the catalog
		// publishes. Checked after the capability so an unauthorized caller
		// learns nothing from the difference between the two refusals.
		confirmed := requestIsConfirmed(ctx)
		if meta.ConfirmationLevel == ConfirmExplicit && !confirmed {
			var actorID int64
			if principal != nil {
				actorID = principal.UserID
			}
			recordEvent(ctx, rec, meta, denialAction(meta, opID), audit.OutcomeDenied,
				audit.ReasonMissingConfirmation, actorID, confirmationStatus)
			_ = huma.WriteErr(api, ctx, confirmationStatus,
				"this operation requires explicit confirmation; resend with "+ConfirmHeader+": true")
			return
		}

		// Give the handler a slot to name the resource it acts on, for
		// creates where no {id} path parameter exists.
		ctx = huma.WithContext(ctx, withConfirmed(audit.WithResourceSlot(ctx.Context()), confirmed))

		next(ctx)

		if meta.AuditAction == "" {
			return
		}
		var actorID int64
		if principal != nil {
			actorID = principal.UserID
		}
		recordEvent(ctx, rec, meta, meta.AuditAction, audit.OutcomeForStatus(ctx.Status()), "", actorID, ctx.Status())
	}
}

// denialAction names the audit action for a denied request. Operations that
// declare an AuditAction use it so denials and successes group together;
// read-only operations that declare none still produce a denial record.
func denialAction(meta OperationMeta, opID string) string {
	if meta.AuditAction != "" {
		return meta.AuditAction
	}
	if opID != "" {
		return "authz.denied." + opID
	}
	return "authz.denied"
}

// recordEvent writes an audit event describing the current request. Resource
// kind comes from the operation's first tag and resource id from the "id" path
// parameter, both best-effort; detail carries no request body or PII.
func recordEvent(ctx huma.Context, rec audit.Recorder, meta OperationMeta, action, outcome, reason string, actorID int64, status int) {
	if rec == nil || action == "" {
		return
	}

	e := audit.Event{
		Action:      action,
		ActorUserID: actorID,
		Outcome:     outcome,
		ReasonCode:  reason,
	}

	// A handler that stamped its resource wins; otherwise fall back to the
	// operation tag and the {id} path parameter.
	if kind, id, ok := audit.StampedResource(ctx.Context()); ok {
		e.ResourceKind, e.ResourceID = kind, id
	} else {
		if op := ctx.Operation(); op != nil && len(op.Tags) > 0 {
			e.ResourceKind = op.Tags[0]
		}
		if raw := ctx.Param("id"); raw != "" {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
				e.ResourceID = id
			}
		}
	}

	url := ctx.URL()
	e.DetailJSON = fmt.Sprintf(`{"method":%q,"path":%q,"status":%d,"required_capability":%q}`,
		ctx.Method(), url.Path, status, meta.RequiredCapability)

	rec.Record(ctx.Context(), e)
}

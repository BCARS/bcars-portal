package httpapi

import (
	"context"
	"fmt"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/domain/authz"
)

// OperationMeta carries authorization and audit metadata that every registered
// Huma operation must declare. The startup check verifies completeness.
type OperationMeta struct {
	// RequiredCapability is the authz capability code the caller must hold.
	// For unauthenticated public endpoints use PublicCapability.
	RequiredCapability string
	// AuditAction is the dot-separated event name written to the audit log
	// on every call (e.g. "member.list"). Required for write operations;
	// read operations may leave it empty.
	AuditAction string
	// ConfirmationLevel is one of "none", "recent-auth", or "explicit-confirm".
	ConfirmationLevel string
	// AIToolEligibility is one of "never", "read-only", or "curated".
	AIToolEligibility string
}

// PublicCapability is the sentinel value for operations that need no
// authentication (sign-in, recovery, invitation consumption).
const PublicCapability = "*public*"

var (
	metaMu  sync.Mutex
	opsMeta = map[string]OperationMeta{}
)

// Register wraps huma.Register and records capability metadata for the
// operation. It panics immediately if RequiredCapability is empty, catching
// unguarded operations at startup rather than at request time.
func Register[I, O any](api huma.API, op huma.Operation, meta OperationMeta, handler func(context.Context, *I) (*O, error)) {
	if meta.RequiredCapability == "" {
		panic(fmt.Sprintf(
			"httpapi.Register: operation %q has no RequiredCapability — use PublicCapability for unauthenticated endpoints",
			op.OperationID,
		))
	}
	// Validate that the capability code exists in the catalog (unless it is
	// the public sentinel).
	if meta.RequiredCapability != PublicCapability {
		if _, ok := authz.ByCode(meta.RequiredCapability); !ok {
			panic(fmt.Sprintf(
				"httpapi.Register: operation %q declares unknown capability %q",
				op.OperationID, meta.RequiredCapability,
			))
		}
	}
	metaMu.Lock()
	opsMeta[op.OperationID] = meta
	metaMu.Unlock()
	huma.Register(api, op, handler)
}

// lookupMeta returns the metadata registered for an operation ID. The second
// result is false when the operation was never registered through Register,
// which callers must treat as a denial rather than as an absent requirement.
func lookupMeta(opID string) (OperationMeta, bool) {
	metaMu.Lock()
	defer metaMu.Unlock()
	meta, ok := opsMeta[opID]
	return meta, ok
}

// AllMeta returns a snapshot of all registered operation metadata, keyed by
// operation ID. Used for capability-catalog generation (WS2.3).
func AllMeta() map[string]OperationMeta {
	metaMu.Lock()
	defer metaMu.Unlock()
	out := make(map[string]OperationMeta, len(opsMeta))
	for k, v := range opsMeta {
		out[k] = v
	}
	return out
}

// VerifyAll checks that every operation in api.OpenAPI().Paths that carries
// an OperationID was registered through Register (i.e. has metadata). It
// returns a non-nil error if any slip through, which the startup sequence
// treats as fatal.
func VerifyAll(api huma.API) error {
	metaMu.Lock()
	snap := make(map[string]struct{}, len(opsMeta))
	for k := range opsMeta {
		snap[k] = struct{}{}
	}
	metaMu.Unlock()

	paths := api.OpenAPI().Paths
	if len(paths) == 0 {
		return nil
	}

	var missing []string
	for path, item := range paths {
		for _, op := range []*huma.Operation{
			item.Get, item.Post, item.Put, item.Patch,
			item.Delete, item.Head, item.Options, item.Trace,
		} {
			if op == nil || op.OperationID == "" {
				continue // Huma built-in or path-level declaration
			}
			if _, ok := snap[op.OperationID]; !ok {
				missing = append(missing, fmt.Sprintf("%s %s", path, op.OperationID))
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"operations registered without capability metadata (use httpapi.Register): %v",
			missing,
		)
	}
	return nil
}

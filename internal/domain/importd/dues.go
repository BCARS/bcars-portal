package importd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// The import cutover (ADR-0007, pma.13).
//
// Phase 1 normalized the Groups.io `Current Until` column and recognised the
// two known 12/31/2055 lifetime rows, then dropped both on the floor: the
// commit path wrote neither a paid-through value nor an honorary grant. An
// officer who imported a member could not see when that member last paid.
//
// Now that the Phase 2 ledger exists, commit writes those decisions to their
// canonical homes:
//
//   - an ordinary date becomes a coverage event linked to the import run;
//   - a known lifetime row becomes a real honorary grant, and never a
//     fabricated 2055 paid-through, because nobody paid through 2055.
//
// Anything ambiguous stays manual, and the officer's recorded decision decides.

// DecisionPayload is the optional JSON an officer attaches to a manual
// reconciliation decision. It is what lets a human resolve exactly the cases
// normalization refuses to guess at.
type DecisionPayload struct {
	// BaseType overrides the membership base type: "full" or "associate".
	BaseType string `json:"base_type,omitempty"`
	// Honorary is "lifetime" to grant a lifetime honorary waiver, or "none" to
	// state explicitly that this row is not honorary.
	Honorary string `json:"honorary,omitempty"`
	// Coverage is "none" to record no paid-through at all, or "import" to
	// record one. Empty means "use the normalized value".
	Coverage string `json:"coverage,omitempty"`
	// PaidThrough overrides the imported date with an ISO YYYY-MM-DD value.
	PaidThrough string `json:"paid_through,omitempty"`
}

// Decision payload values.
const (
	HonoraryLifetime = "lifetime"
	HonoraryNone     = "none"
	CoverageNone     = "none"
	CoverageImport   = "import"
)

// decisionFor returns the most recent officer decision payload for a staged
// row. An absent or unparseable payload yields the zero value, which means
// "no override" — a malformed payload must never silently change what is
// imported.
func decisionFor(ctx context.Context, qtx *sqlcgen.Queries, rowID int64) (DecisionPayload, error) {
	var payload DecisionPayload

	decisions, err := qtx.ListDecisionsForRow(ctx, rowID)
	if err != nil {
		return payload, fmt.Errorf("list decisions: %w", err)
	}
	for i := len(decisions) - 1; i >= 0; i-- {
		d := decisions[i]
		if !d.PayloadJson.Valid || d.PayloadJson.String == "" {
			continue
		}
		if err := json.Unmarshal([]byte(d.PayloadJson.String), &payload); err != nil {
			return DecisionPayload{}, nil
		}
		return payload, nil
	}
	return payload, nil
}

// baseTypeFor resolves the membership base type, letting an officer's decision
// override what normalization proposed.
func baseTypeFor(norm NormalizedRecord, payload DecisionPayload) string {
	if payload.BaseType != "" {
		return payload.BaseType
	}
	return norm.BaseType
}

// isLifetimeHonorary reports whether this row should produce a lifetime
// honorary grant. Normalization only auto-proposes one for the known external
// IDs; every other lifetime-looking row requires an officer to say so.
func isLifetimeHonorary(norm NormalizedRecord, payload DecisionPayload) bool {
	switch payload.Honorary {
	case HonoraryLifetime:
		return true
	case HonoraryNone:
		return false
	}
	return norm.MembershipType == "Honorary" && norm.CurrentUntilFlag == "lifetime_known"
}

// coverageDateFor resolves the paid-through date this row should record, or ""
// for none.
//
// A lifetime honorary row deliberately records no date: the member owes
// nothing, which is not the same as having paid through the year 2055. Writing
// the sentinel as a real paid-through would be inventing a payment.
func coverageDateFor(norm NormalizedRecord, payload DecisionPayload, lifetime bool) string {
	if payload.Coverage == CoverageNone {
		return ""
	}
	if payload.PaidThrough != "" {
		return payload.PaidThrough
	}
	if lifetime {
		return ""
	}
	// Only an ordinary parsed date carries over. A blank, a sentinel null, and
	// an unconfirmed lifetime-like date all mean "we do not know".
	if norm.CurrentUntilFlag != "" {
		return ""
	}
	return norm.CurrentUntil
}

// isISODate reports whether s is a real YYYY-MM-DD date.
//
// Normalization passes an unparseable Current Until through verbatim rather
// than dropping it, so a source typo like "1/1/900" reaches here as-is. Writing
// it would fail the schema's date check and abort the whole import over one bad
// cell, which is a far worse outcome than not recording a date nobody can read.
// The original text is still preserved in the staged row either way.
func isISODate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// applyDuesDecisions writes the imported paid-through and honorary decisions
// for one membership. It is called for both newly created and matched
// memberships, inside the commit transaction.
func (s *Service) applyDuesDecisions(
	ctx context.Context,
	qtx *sqlcgen.Queries,
	membershipID int64,
	norm NormalizedRecord,
	payload DecisionPayload,
	runID, actorID int64,
	now string,
) error {
	lifetime := isLifetimeHonorary(norm, payload)

	if lifetime {
		if err := s.ensureLifetimeGrant(ctx, qtx, membershipID, actorID, now); err != nil {
			return err
		}
	}

	date := coverageDateFor(norm, payload, lifetime)
	if date == "" || !isISODate(date) {
		return nil
	}
	return s.ensureImportCoverage(ctx, qtx, membershipID, date, runID, actorID, now)
}

// ensureLifetimeGrant creates a lifetime honorary grant unless the membership
// already holds an active one, so re-importing the same roster is harmless.
func (s *Service) ensureLifetimeGrant(ctx context.Context, qtx *sqlcgen.Queries, membershipID, actorID int64, now string) error {
	existing, err := qtx.ListHonoraryGrantsByMembership(ctx, membershipID)
	if err != nil {
		return fmt.Errorf("list honorary grants: %w", err)
	}
	for _, g := range existing {
		if g.IsLifetime == 1 && !g.RevokedAt.Valid {
			return nil
		}
	}

	_, err = qtx.CreateHonoraryGrant(ctx, sqlcgen.CreateHonoraryGrantParams{
		MembershipID: membershipID,
		StartsOn:     now[:10],
		IsLifetime:   1,
		Reason:       "Lifetime honorary member, imported from Groups.io.",
		ApprovedBy:   actorID,
		ApprovedAt:   now,
	})
	if err != nil {
		return fmt.Errorf("create honorary grant: %w", err)
	}
	return nil
}

// ensureImportCoverage appends a source-linked coverage event unless this
// membership already carries an imported decision for the same date.
func (s *Service) ensureImportCoverage(ctx context.Context, qtx *sqlcgen.Queries, membershipID int64, paidThrough string, runID, actorID int64, now string) error {
	_, err := qtx.FindImportCoverageEvent(ctx, sqlcgen.FindImportCoverageEventParams{
		MembershipID: membershipID,
		PaidThrough:  paidThrough,
	})
	switch {
	case err == nil:
		// Already imported. Re-importing unchanged data changes nothing.
		return nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return fmt.Errorf("find import coverage: %w", err)
	}

	// Supersede whatever decision is currently effective, so the history reads
	// as a chain rather than a pile of competing dates.
	var supersedes sql.NullInt64
	current, err := qtx.GetEffectiveCoverageEvent(ctx, membershipID)
	switch {
	case err == nil:
		supersedes = sql.NullInt64{Int64: current.ID, Valid: true}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return fmt.Errorf("effective coverage: %w", err)
	}

	_, err = qtx.CreateCoverageEvent(ctx, sqlcgen.CreateCoverageEventParams{
		MembershipID:      membershipID,
		PaidThrough:       paidThrough,
		ReasonKind:        "import",
		Reason:            sqlNullString("Imported Current Until value from Groups.io."),
		ImportRunID:       sql.NullInt64{Int64: runID, Valid: runID != 0},
		SupersedesEventID: supersedes,
		DecidedBy:         sql.NullInt64{Int64: actorID, Valid: actorID != 0},
		DecidedAt:         now,
	})
	if err != nil {
		return fmt.Errorf("create import coverage: %w", err)
	}
	return nil
}

// membershipForPerson returns the membership an imported row should attach its
// dues decisions to, creating and approving one when a matched person has none.
//
// A matched person with no membership is common on a first import: Phase 1
// created the person from an earlier partial run. Without a membership there is
// nowhere to record what they paid, so the import creates one rather than
// silently dropping the date.
func (s *Service) membershipForPerson(ctx context.Context, qtx *sqlcgen.Queries, personID int64, baseType string, actorID int64, now string) (int64, error) {
	memberships, err := qtx.ListMembershipsByPerson(ctx, personID)
	if err != nil {
		return 0, fmt.Errorf("list memberships: %w", err)
	}
	for _, m := range memberships {
		switch m.Lifecycle {
		case "rejected", "resigned", "deceased":
			continue
		}
		return m.ID, nil
	}

	if baseType == "" {
		// Nothing to create a membership from, and nothing to attach.
		return 0, nil
	}

	m, err := qtx.CreateMembership(ctx, sqlcgen.CreateMembershipParams{
		PersonID: personID,
		BaseType: baseType,
	})
	if err != nil {
		return 0, fmt.Errorf("create membership: %w", err)
	}
	if _, err := qtx.ApproveMembership(ctx, sqlcgen.ApproveMembershipParams{
		BaseType: baseType,
		JoinedOn: sqlNullString(now[:10]),
		ID:       m.ID,
		Version:  m.Version,
	}); err != nil {
		return 0, fmt.Errorf("approve membership: %w", err)
	}
	if _, err := qtx.CreateMembershipApproval(ctx, sqlcgen.CreateMembershipApprovalParams{
		MembershipID: m.ID,
		Decision:     "approved",
		ApprovedType: sqlNullString(baseType),
		DecidedBy:    actorID,
		DecidedAt:    now,
		Reason:       sqlNullString("import from Groups.io"),
	}); err != nil {
		return 0, fmt.Errorf("create approval: %w", err)
	}
	return m.ID, nil
}

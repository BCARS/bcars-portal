package changerequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// Review turns a proposal into canonical data, one item at a time
// (bcars-portal-4ux.3).
//
// The shape of this file is dictated by one rule: an approval either applies
// completely or changes nothing. The decision and the canonical write happen in
// ONE transaction, so a stale target or a refused adapter cannot leave an item
// marked approved with nothing applied, nor canonical data changed with no
// decision recorded.

var (
	// ErrItemNotInRequest is returned when an item id does not belong to the
	// request named in the path.
	ErrItemNotInRequest = errors.New("changerequests: item does not belong to that request")

	// ErrItemDecided is returned when an item already has a decision. Review is
	// once per item; changing your mind is a new request, not a silent
	// overwrite of the record.
	ErrItemDecided = errors.New("changerequests: item has already been decided")

	// ErrNoAdapter is returned when approving an operation that no domain
	// service can apply. It is refused BEFORE the decision is recorded, so an
	// item never sits approved and unapplied.
	ErrNoAdapter = errors.New("changerequests: this operation cannot be applied automatically")

	// ErrSelfReview is returned when the requester is the reviewer on a
	// sensitive item.
	ErrSelfReview = errors.New("changerequests: a sensitive item cannot be approved by the member who requested it")

	// ErrVerificationNoteRequired is returned when a sensitive approval carries
	// no verification note.
	ErrVerificationNoteRequired = errors.New("changerequests: a sensitive approval requires a verification note")

	// ErrReasonRequired is returned when a rejection carries no reason. A
	// member is entitled to know why.
	ErrReasonRequired = errors.New("changerequests: a rejection requires a reason")

	// ErrUnknownDecision is returned for a status outside the review set.
	ErrUnknownDecision = errors.New("changerequests: decision must be approved, rejected, or needs_verification")

	// ErrTargetRequired is returned when applying an operation that edits an
	// existing resource without naming one.
	ErrTargetRequired = errors.New("changerequests: this operation must name the resource it changes")

	// ErrBadValue is returned when a proposed value cannot be parsed for its
	// operation.
	ErrBadValue = errors.New("changerequests: the proposed value is not valid for this operation")
)

// DecideParams is one review decision.
type DecideParams struct {
	// Decision is approved, rejected, or needs_verification.
	Decision string
	// Reason explains a rejection or a needs-verification hold.
	Reason string
	// VerificationNote records how a sensitive approval was verified, e.g.
	// "called the published number back".
	VerificationNote string
	// AmendedValue is the value the REVIEWER decided to apply, when it is not
	// the one the submitter sent (ADR-0014.5). A member writes their new
	// address with one character mistyped; the officer drops it and approves,
	// instead of rejecting and asking them to send the whole thing again.
	//
	// Empty means "apply what was proposed", which is the ordinary case.
	//
	// It never rewrites proposed_value. What the submitter asked for and what
	// the reviewer did are two facts, and applied_value records the second
	// (migration 0016). Ignored for a rejection or a hold, which apply nothing.
	AmendedValue string
}

// Decision is the outcome of reviewing one item.
type Decision struct {
	Request Request
	Item    Item
	// Applied reports whether canonical data changed. False for a rejection, a
	// verification hold, and a replayed approval.
	Applied bool
	// Replay reports that this item was already decided identically, so the
	// recorded outcome was returned rather than repeating the work.
	Replay bool
}

// DecideItem records a decision and, for a supported approval, applies it.
//
// Ordering matters and is deliberate:
//
//  1. load the request and item, and refuse anything already decided;
//  2. check policy — self-review, verification note, reason, adapter;
//  3. record the decision;
//  4. apply through the domain adapter;
//  5. stamp what the apply produced.
//
// All five happen in one transaction. Step 4 failing rolls back step 3, which
// is what makes "stale target returns a conflict without partial application"
// true rather than merely intended.
func (s *Service) DecideItem(
	ctx context.Context,
	p *authz.Principal,
	memberSvc *members.Service,
	requestID, itemID int64,
	params DecideParams,
	now time.Time,
) (Decision, error) {
	switch params.Decision {
	case ItemApproved, ItemRejected, ItemNeedsVerification:
	default:
		return Decision{}, ErrUnknownDecision
	}
	if params.Decision == ItemRejected && strings.TrimSpace(params.Reason) == "" {
		return Decision{}, ErrReasonRequired
	}

	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	var out Decision
	err := s.inTxWith(ctx, func(q *sqlcgen.Queries, tx *sql.Tx) error {
		// Bind the member service to THIS transaction. Without it the adapters
		// would write through the bare *sql.DB, and a rolled-back decision
		// would leave the canonical change committed.
		if memberSvc != nil {
			memberSvc = memberSvc.WithTx(tx)
		}
		request, err := q.GetChangeRequest(ctx, requestID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		itemRow, err := q.GetChangeRequestItem(ctx, itemID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if itemRow.RequestID != requestID {
			return ErrItemNotInRequest
		}

		item := itemsFrom([]sqlcgen.MemberChangeRequestItem{itemRow})[0]

		// An already-decided item is a replay if the caller is asking for the
		// same decision, and a refusal otherwise. Returning the recorded
		// outcome is what makes a retried approval safe.
		if item.Status != ItemPending {
			if item.Status == params.Decision {
				out, err = s.assembleDecision(ctx, q, requestID, item, false, true)
				return err
			}
			return ErrItemDecided
		}

		sensitivity := EffectiveSensitivity(item.Operation, item.Sensitivity)

		if params.Decision == ItemApproved {
			// Self-review. The requester is the member who submitted it; an
			// officer entering a correction on someone else's behalf is not
			// the requester, which is why this reads requester_user_id rather
			// than received_by.
			if sensitivity == SensitivitySensitive &&
				request.RequesterUserID.Valid && request.RequesterUserID.Int64 == actorID {
				return ErrSelfReview
			}
			if sensitivity == SensitivitySensitive &&
				strings.TrimSpace(params.VerificationNote) == "" {
				return ErrVerificationNoteRequired
			}
			if !CanApply(item.Operation) {
				return ErrNoAdapter
			}
		}

		decided, err := q.DecideChangeRequestItem(ctx, sqlcgen.DecideChangeRequestItemParams{
			Status:           params.Decision,
			ReviewedBy:       sql.NullInt64{Int64: actorID, Valid: actorID != 0},
			ReviewedAt:       sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
			DecisionReason:   nullString(params.Reason),
			VerificationNote: nullString(params.VerificationNote),
			ID:               itemID,
			Version:          item.Version,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The row exists and was pending a moment ago, so another
				// reviewer decided it first.
				return ErrItemDecided
			}
			return err
		}

		applied := false
		if params.Decision == ItemApproved {
			// The reviewer's amendment replaces the value for the APPLY only.
			// item is a local copy; the stored proposal is untouched.
			if amended := strings.TrimSpace(params.AmendedValue); amended != "" {
				item.ProposedValue = amendedProposal(item, amended)
			}
			result, err := s.applyItem(ctx, q, memberSvc, p, item)
			if err != nil {
				// Rolls back the decision recorded above.
				return err
			}
			decided, err = q.MarkChangeRequestItemApplied(ctx, sqlcgen.MarkChangeRequestItemAppliedParams{
				AppliedAt: sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
				// Valid unconditionally: an operation that sets no value
				// records the empty string, so that NULL keeps meaning
				// "applied before the portal recorded this" and nothing else.
				AppliedValue:           sql.NullString{String: result.Value, Valid: true},
				AppliedResourceKind:    sql.NullString{String: result.Kind, Valid: true},
				AppliedResourceID:      sql.NullInt64{Int64: result.ID, Valid: true},
				AppliedResourceVersion: sql.NullInt64{Int64: result.Version, Valid: result.Version != 0},
				ID:                     itemID,
				Version:                decided.Version,
			})
			if err != nil {
				return err
			}
			applied = true
		}

		// Resolve the request once every item is terminal.
		pending, err := q.CountPendingChangeRequestItems(ctx, requestID)
		if err != nil {
			return err
		}
		if pending == 0 && request.Status != StatusResolved {
			if _, err := q.SetChangeRequestStatus(ctx, sqlcgen.SetChangeRequestStatusParams{
				Status:     StatusResolved,
				ResolvedAt: sql.NullString{String: now.UTC().Format(isoTimestamp), Valid: true},
				ID:         requestID,
				Version:    request.Version,
			}); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		} else if request.Status == StatusSubmitted {
			if _, err := q.SetChangeRequestStatus(ctx, sqlcgen.SetChangeRequestStatusParams{
				Status:  StatusInReview,
				ID:      requestID,
				Version: request.Version,
			}); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		out, err = s.assembleDecision(ctx, q,
			requestID, itemsFrom([]sqlcgen.MemberChangeRequestItem{decided})[0], applied, false)
		return err
	})
	if err != nil {
		return Decision{}, err
	}
	return out, nil
}

func (s *Service) assembleDecision(ctx context.Context, q *sqlcgen.Queries, requestID int64, item Item, applied, replay bool) (Decision, error) {
	r, err := s.load(ctx, q, requestID)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Request: r, Item: item, Applied: applied, Replay: replay}, nil
}

// applyResult describes what an adapter produced.
type applyResult struct {
	Kind    string
	ID      int64
	Version int64
	// Value is what reached the record, in the form it was written -- an
	// upper-cased call sign is recorded upper-cased, and a contact value is
	// recorded without the "kind:" prefix the proposal carries. Since ADR-0014
	// an officer may amend a value while approving it, so this is not always
	// the proposed value, and the two are shown side by side.
	//
	// The operations that set no value at all -- making a contact primary,
	// archiving one -- leave this empty, which is why the column records the
	// empty string rather than NULL for them. NULL is reserved for items
	// applied before any of this was recorded (migration 0016).
	Value string
}

// applyItem calls the one domain adapter this operation maps to.
//
// Every branch delegates. There is no path here that writes a canonical table
// directly, which is what keeps each field's validation, audit stamping, and
// capability check in the service that owns it.
func (s *Service) applyItem(
	ctx context.Context,
	q *sqlcgen.Queries,
	memberSvc *members.Service,
	p *authz.Principal,
	item Item,
) (applyResult, error) {
	if memberSvc == nil {
		return applyResult{}, ErrNoAdapter
	}
	if RequiresTargetID(item.Operation) && item.TargetID == 0 {
		return applyResult{}, ErrTargetRequired
	}

	switch Adapters[item.Operation] {
	case AdapterPersonUpdate:
		return s.applyPersonUpdate(ctx, q, memberSvc, p, item)
	case AdapterContactCreate:
		return s.applyContactCreate(ctx, q, memberSvc, p, item)
	case AdapterContactUpdate:
		return s.applyContactUpdate(ctx, q, memberSvc, p, item)
	case AdapterContactArchive:
		cm, err := q.GetContactMethod(ctx, item.TargetID)
		if err != nil {
			return applyResult{}, mapMissing(err)
		}
		if err := checkTargetVersion(item, cm.Version); err != nil {
			return applyResult{}, err
		}
		if err := memberSvc.ArchiveContactMethod(ctx, p, item.TargetID, cm.Version); err != nil {
			return applyResult{}, err
		}
		return applyResult{Kind: "contact_method", ID: item.TargetID, Version: cm.Version + 1}, nil
	case AdapterContactSetPrimary:
		cm, err := q.GetContactMethod(ctx, item.TargetID)
		if err != nil {
			return applyResult{}, mapMissing(err)
		}
		if err := checkTargetVersion(item, cm.Version); err != nil {
			return applyResult{}, err
		}
		if err := memberSvc.MakePrimary(ctx, p, item.TargetID); err != nil {
			return applyResult{}, err
		}
		return applyResult{Kind: "contact_method", ID: item.TargetID}, nil
	case AdapterContactVisibility:
		audience := strings.TrimSpace(item.ProposedValue)
		if audience == "" {
			return applyResult{}, ErrBadValue
		}
		ev, err := memberSvc.SetDirectoryVisibility(ctx, p, item.TargetID, audience,
			members.PrefSourceMemberRequest)
		if err != nil {
			return applyResult{}, err
		}
		return applyResult{Kind: "contact_method_visibility_event", ID: ev.ID, Value: audience}, nil
	case AdapterSharingPreference:
		participates, err := parseBool(item.ProposedValue)
		if err != nil {
			return applyResult{}, err
		}
		ev, err := memberSvc.SetAcsAresSharing(ctx, p, item.TargetID, participates,
			"Applied from reviewed member request.", members.PrefSourceMemberRequest)
		if err != nil {
			return applyResult{}, err
		}
		return applyResult{
			Kind:  "acs_ares_sharing_event",
			ID:    ev.ID,
			Value: strconv.FormatBool(participates),
		}, nil
	}
	return applyResult{}, ErrNoAdapter
}

func (s *Service) applyPersonUpdate(ctx context.Context, q *sqlcgen.Queries, memberSvc *members.Service, p *authz.Principal, item Item) (applyResult, error) {
	personID := item.TargetID
	if personID == 0 {
		return applyResult{}, ErrTargetRequired
	}
	person, err := q.GetPerson(ctx, personID)
	if err != nil {
		return applyResult{}, mapMissing(err)
	}
	if err := checkTargetVersion(item, person.Version); err != nil {
		return applyResult{}, err
	}

	// Carry every field forward and change only the one the item names, so an
	// approval never blanks a field nobody proposed changing.
	params := members.UpdatePersonParams{
		ID:          personID,
		Version:     person.Version,
		DisplayName: person.DisplayName,
		SortName:    person.SortName,
		CallSign:    person.CallSign.String,
	}
	value := strings.TrimSpace(item.ProposedValue)
	if value == "" {
		return applyResult{}, ErrBadValue
	}
	// applied records the field as it was written, which is not always what
	// arrived: a call sign is upper-cased on the way in.
	applied := value
	switch item.Operation {
	case "person.display_name.set":
		params.DisplayName = value
		params.SortName = strings.ToLower(value)
	case "person.call_sign.set":
		params.CallSign = strings.ToUpper(value)
		applied = params.CallSign
	default:
		return applyResult{}, ErrNoAdapter
	}

	updated, err := memberSvc.UpdatePerson(ctx, p, params)
	if err != nil {
		return applyResult{}, err
	}
	return applyResult{Kind: "person", ID: updated.ID, Version: updated.Version, Value: applied}, nil
}

func (s *Service) applyContactCreate(ctx context.Context, q *sqlcgen.Queries, memberSvc *members.Service, p *authz.Principal, item Item) (applyResult, error) {
	kind, value, err := parseContactValue(item.ProposedValue)
	if err != nil {
		return applyResult{}, err
	}
	// An add attaches to the request's target person, since a new contact has
	// no existing row to name.
	personID := item.TargetID
	if personID == 0 {
		req, err := q.GetChangeRequest(ctx, item.RequestID)
		if err != nil {
			return applyResult{}, err
		}
		if !req.TargetPersonID.Valid {
			return applyResult{}, ErrTargetRequired
		}
		personID = req.TargetPersonID.Int64
	}

	cm, err := memberSvc.CreateContactMethod(ctx, p, members.CreateContactMethodParams{
		PersonID:  personID,
		Kind:      kind,
		ValueRaw:  value,
		ValueNorm: normalizeContact(kind, value),
	})
	if err != nil {
		return applyResult{}, err
	}
	return applyResult{Kind: "contact_method", ID: cm.ID, Version: cm.Version, Value: value}, nil
}

func (s *Service) applyContactUpdate(ctx context.Context, q *sqlcgen.Queries, memberSvc *members.Service, p *authz.Principal, item Item) (applyResult, error) {
	existing, err := q.GetContactMethod(ctx, item.TargetID)
	if err != nil {
		return applyResult{}, mapMissing(err)
	}
	if err := checkTargetVersion(item, existing.Version); err != nil {
		return applyResult{}, err
	}

	kind, value, err := parseContactValue(item.ProposedValue)
	if err != nil {
		return applyResult{}, err
	}
	if kind != existing.Kind {
		// Changing an email into a phone is not an edit of the same contact.
		return applyResult{}, ErrBadValue
	}

	updated, err := memberSvc.UpdateContactMethod(ctx, p, members.UpdateContactMethodParams{
		ID:        item.TargetID,
		Version:   existing.Version,
		Label:     existing.Label.String,
		ValueRaw:  value,
		ValueNorm: normalizeContact(kind, value),

		PostalLine1:      existing.PostalLine1.String,
		PostalLine2:      existing.PostalLine2.String,
		PostalCity:       existing.PostalCity.String,
		PostalState:      existing.PostalState.String,
		PostalPostalCode: existing.PostalPostalCode.String,
		PostalCountry:    existing.PostalCountry.String,
	})
	if err != nil {
		return applyResult{}, err
	}
	return applyResult{Kind: "contact_method", ID: updated.ID, Version: updated.Version, Value: value}, nil
}

// checkTargetVersion enforces the conflict rule.
//
// An item recording the version its submitter saw is refused if the resource
// moved since. Without it, a correction reported last week could silently
// overwrite a change an officer made yesterday. An item with no recorded
// version opts out, which is correct for a submitter who never saw one.
func checkTargetVersion(item Item, current int64) error {
	if item.TargetVersion == 0 || item.TargetVersion == current {
		return nil
	}
	return db.ErrStale
}

func mapMissing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: the resource this item names no longer exists", ErrTargetRequired)
	}
	return err
}

// parseContactValue splits the "kind:value" encoding a contact item carries.
//
// The encoding is deliberately narrow. A structured payload would let a
// submitter reach fields no reviewer looked at; a kind and a value can only
// ever say two things.
//
// No example address appears in this comment on purpose: check-no-secrets.sh
// rejects an email-like literal in any tracked non-test file, and it is right
// to, because a real one reaches production source the same way an example
// does.
// amendedProposal puts a reviewer's plain value back into the form the appliers
// read.
//
// A contact proposal is stored as "kind:value" so an approval cannot turn an
// email into a phone. A reviewer amending it types a telephone number, not
// "phone:814-555-0199", so the kind is carried over from the proposal being
// amended rather than asked for again. Changing the KIND is not an amendment;
// it is a different correction, and the encoding keeps it that way.
//
// A value that already carries a recognised kind prefix is left alone, so a
// caller that does supply the encoded form is not double-prefixed.
func amendedProposal(item Item, amended string) string {
	if Adapters[item.Operation] != AdapterContactCreate &&
		Adapters[item.Operation] != AdapterContactUpdate {
		return amended
	}
	if kind, _, err := parseContactValue(amended); err == nil && kind != "" {
		return amended
	}
	kind, _, err := parseContactValue(item.ProposedValue)
	if err != nil {
		// Nothing to carry over: hand it on unchanged and let the applier
		// refuse it, rather than inventing a kind here.
		return amended
	}
	return kind + ":" + amended
}

func parseContactValue(raw string) (kind, value string, err error) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: expected \"kind:value\"", ErrBadValue)
	}
	kind = strings.ToLower(strings.TrimSpace(parts[0]))
	value = strings.TrimSpace(parts[1])
	switch kind {
	case "email", "phone", "postal":
	default:
		return "", "", fmt.Errorf("%w: contact kind must be email, phone, or postal", ErrBadValue)
	}
	if value == "" {
		return "", "", fmt.Errorf("%w: empty contact value", ErrBadValue)
	}
	return kind, value, nil
}

// normalizeContact produces the comparison form stored alongside the raw value.
func normalizeContact(kind, value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if kind == "phone" {
		var digits strings.Builder
		for _, r := range v {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		return digits.String()
	}
	return v
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "1":
		return true, nil
	case "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%w: expected true or false", ErrBadValue)
}

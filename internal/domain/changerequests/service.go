// Package changerequests implements the intake and read model for member
// corrections, whatever channel they arrive through.
//
// The defining property of this package is that nothing in it changes canonical
// data. Creating a request writes rows in member_change_requests and
// member_change_request_items and nowhere else: no person, contact method,
// membership, coverage, payment, or preference event is touched. An officer
// recording a phone call has recorded a PROPOSAL, and the proposal is inert
// until bcars-portal-4ux.3's review path applies an approved item through the
// domain service that owns that field.
//
// That is what makes intake safe to hand to any officer, and what lets the same
// model carry an authenticated member's suggestion about someone else without
// that suggestion ever touching the other member's record — or revealing it.
package changerequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bcars/bcars-portal/internal/db"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/idem"
)

// Sources a request may arrive through. Source affects provenance and triage,
// never which validation rules apply on approval.
const (
	SourceOfficerPhone   = "officer_phone"
	SourceOfficerEmail   = "officer_email"
	SourceOfficerMail    = "officer_mail"
	SourceOfficerMeeting = "officer_meeting"
	SourceMember         = "member"
)

// intakeSources is the set a request may actually be created with.
//
// It is the same set migration 0013 leaves in the database CHECK constraint.
// Phase 3 briefly planned an anonymous 'public' channel; that plan was
// withdrawn (bcars-portal-4ux.16, ADR-0013) and the value is gone from both
// places, so neither this package nor a hand-run UPDATE can produce one.
var intakeSources = map[string]struct{}{
	SourceOfficerPhone:   {},
	SourceOfficerEmail:   {},
	SourceOfficerMail:    {},
	SourceOfficerMeeting: {},
	SourceMember:         {},
}

// Request lifecycle states.
const (
	StatusDraft     = "draft"
	StatusSubmitted = "submitted"
	StatusInReview  = "in_review"
	StatusResolved  = "resolved"
	StatusWithdrawn = "withdrawn"
)

// Item decision states.
const (
	ItemPending           = "pending"
	ItemApproved          = "approved"
	ItemRejected          = "rejected"
	ItemNeedsVerification = "needs_verification"
)

// Sensitivity classes.
const (
	SensitivityOrdinary  = "ordinary"
	SensitivitySensitive = "sensitive"
)

// OpOther is the escape hatch for a suggestion outside the supported set:
// membership lifecycle, FCC verification, dues, honorary status, or anything
// else no adapter can apply.
//
// It stays visible to officers and can be rejected or sent for verification,
// but the database refuses to mark it approved, so it can never reach an apply
// path. An officer who wants to act on one uses the existing specialized
// workflow, which keeps its own validation.
const OpOther = "other"

// SupportedOperations is the allowlist. There is no arbitrary field path here
// on purpose: a request can only ever propose something this list names, so
// intake cannot become a generic write primitive.
var SupportedOperations = []string{
	"person.display_name.set",
	"person.call_sign.set",
	"contact_method.add",
	"contact_method.update",
	"contact_method.archive",
	"contact_method.set_primary",
	"contact_method.visibility.set",
	"sharing_pref.acs_ares.set",
	"relationship.add",
	"relationship.correct",
	OpOther,
}

// Target kinds an item may reference.
var supportedTargetKinds = map[string]struct{}{
	"person":         {},
	"contact_method": {},
	"membership":     {},
	"relationship":   {},
}

// OpRequestCreate names this package's idempotent operation, scoped per actor
// in idempotency_records.
const OpRequestCreate = "change-request-create"

// DefaultLimit caps an unbounded listing.
const DefaultLimit = 50

// MaxLimit bounds what a caller may ask for in one page.
const MaxLimit = 200

// Field bounds. Intake accepts free text from a telephone call or a member's
// own typing, so every string is bounded before it reaches the database.
const (
	MaxSummaryLen  = 4000
	MaxSuppliedLen = 200
	MaxValueLen    = 2000
	MaxItems       = 25
)

const isoTimestamp = "2006-01-02T15:04:05.000Z"

var (
	// ErrSourceRequired is returned for an unknown or missing source.
	ErrSourceRequired = errors.New("changerequests: source must be a known intake channel")

	// ErrSummaryRequired is returned for a blank summary. Every request says
	// something in plain language, because that is what an officer reads first.
	ErrSummaryRequired = errors.New("changerequests: a summary is required")

	// ErrTooLong is returned when a supplied field exceeds its bound.
	ErrTooLong = errors.New("changerequests: a supplied value is too long")

	// ErrNoTarget is returned when an officer-entered request names neither a
	// target person nor any hint about who it concerns. A request nobody can
	// ever match is not a correction, it is a note with no subject.
	ErrNoTarget = errors.New("changerequests: name a target person or supply an identifying hint")

	// ErrUnknownOperation is returned for an item outside SupportedOperations.
	ErrUnknownOperation = errors.New("changerequests: unsupported item operation")

	// ErrUnknownTargetKind is returned for an item target outside the known
	// resource kinds.
	ErrUnknownTargetKind = errors.New("changerequests: unsupported item target kind")

	// ErrTargetIncomplete is returned when an item names a target kind without
	// an id, or the reverse.
	ErrTargetIncomplete = errors.New("changerequests: an item target needs both a kind and an id")

	// ErrValueRequired is returned when a supported operation carries no
	// proposed value. Only `other` may be pure prose.
	ErrValueRequired = errors.New("changerequests: this operation needs a proposed value")

	// ErrTooManyItems is returned for an oversized item list.
	ErrTooManyItems = errors.New("changerequests: too many items in one request")

	// ErrNoItems is returned for a request proposing nothing at all.
	ErrNoItems = errors.New("changerequests: a request needs at least one item")

	// ErrUnknownPerson is returned when a named target person does not exist.
	ErrUnknownPerson = errors.New("changerequests: target person does not exist")

	// ErrNotFound is returned when a request id does not exist.
	ErrNotFound = errors.New("changerequests: request not found")

	// ErrAlreadyResolved is returned when triage targets a terminal request.
	ErrAlreadyResolved = errors.New("changerequests: request is already resolved or withdrawn")

	// ErrNotYours is returned when a member names a request they did not
	// submit.
	//
	// Callers MUST translate it to the same response ErrNotFound produces. A
	// distinct "that is not yours" would confirm the request exists, which is
	// one bit more than a stranger is entitled to and enough to enumerate the
	// queue by id.
	ErrNotYours = errors.New("changerequests: request belongs to another submitter")

	// ErrDecidedItems is returned when withdrawing a request an officer has
	// already started deciding. What was decided stays decided; the member
	// takes it up with an officer rather than erasing the record.
	ErrDecidedItems = errors.New("changerequests: an officer has already decided part of this request")
)

// Service captures and reads member change requests.
type Service struct {
	DB *sql.DB
	Q  *sqlcgen.Queries
}

// NewService creates a change-request service over database.
func NewService(database *sql.DB) *Service {
	return &Service{DB: database, Q: sqlcgen.New(database)}
}

// ItemInput is one proposed change.
type ItemInput struct {
	// Operation must be one of SupportedOperations.
	Operation string
	// ProposedValue is the new value, encoded as the operation expects.
	// Required for every operation except `other`.
	ProposedValue string
	// TargetKind and TargetID name the resource the item concerns, when the
	// submitter knew it. Both or neither.
	TargetKind string
	TargetID   int64
	// TargetVersion is the version the submitter saw, when known. The review
	// path uses it to detect a conflicting change made since.
	TargetVersion int64
	// Sensitivity classifies the approval. Defaults to ordinary.
	Sensitivity string
}

// CreateParams describes one intake.
type CreateParams struct {
	Source             string
	TargetPersonID     int64
	SuppliedName       string
	SuppliedCallSign   string
	SuppliedContact    string
	StatedRelationship string
	Summary            string
	// RequesterUserID identifies the authenticated member who submitted this,
	// whether about themselves or about someone else. Zero for officer-entered
	// intake. The schema requires it for source 'member'.
	RequesterUserID int64
	// SourceIPHash is the hashed caller address, kept for abuse review of
	// authenticated member submissions. Empty when unknown.
	SourceIPHash string
	Items        []ItemInput
}

// Item is one stored proposal.
type Item struct {
	ID                     int64
	RequestID              int64
	Ordinal                int64
	Operation              string
	ProposedValue          string
	TargetKind             string
	TargetID               int64
	TargetVersion          int64
	Sensitivity            string
	Status                 string
	ReviewedBy             int64
	ReviewedAt             string
	DecisionReason         string
	VerificationNote       string
	AppliedAt              string
	AppliedResourceKind    string
	AppliedResourceID      int64
	AppliedResourceVersion int64
	Version                int64
}

// Request is one stored intake with its items.
type Request struct {
	ID                 int64
	Source             string
	Status             string
	RequesterUserID    int64
	TargetPersonID     int64
	TargetDisplayName  string
	SuppliedName       string
	SuppliedCallSign   string
	SuppliedContact    string
	StatedRelationship string
	Summary            string
	ReceivedBy         int64
	SubmittedAt        string
	TriagedBy          int64
	TriagedAt          string
	ResolvedAt         string
	WithdrawnAt        string
	CreatedAt          string
	UpdatedAt          string
	Version            int64
	Items              []Item
	// PendingItems is the count of items still awaiting a decision. A request
	// resolves only when it reaches zero.
	PendingItems int64
}

// ListFilter selects a page of requests.
type ListFilter struct {
	// Status and Source are optional exact filters.
	Status string
	Source string
	// UnresolvedTargetOnly restricts the page to requests with no canonical
	// target, which is the triage queue.
	UnresolvedTargetOnly bool
	// RequesterUserID restricts to one submitter's own requests. Used by the
	// member API in 4ux.6; zero means no restriction.
	RequesterUserID int64
	Limit           int64
	Offset          int64
}

// Create records one proposal set and returns it.
//
// It writes nothing outside member_change_requests and
// member_change_request_items. The caller supplies an idempotency key so a
// retried submission does not become a second request.
func (s *Service) Create(ctx context.Context, p *authz.Principal, params CreateParams, idempotencyKey string, now time.Time) (Request, error) {
	if err := validateCreate(params); err != nil {
		return Request{}, err
	}

	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	fingerprint := createFingerprint(actorID, params)

	var out Request
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		claim, err := idem.Begin(ctx, q, actorID, OpRequestCreate, idempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if claim.Replay {
			out, err = s.load(ctx, q, claim.ResourceID)
			return err
		}

		if params.TargetPersonID != 0 {
			if _, err := q.GetPerson(ctx, params.TargetPersonID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUnknownPerson
				}
				return err
			}
		}

		row, err := q.CreateChangeRequest(ctx, sqlcgen.CreateChangeRequestParams{
			Source:             params.Source,
			Status:             StatusSubmitted,
			RequesterUserID:    nullInt(params.RequesterUserID),
			TargetPersonID:     nullInt(params.TargetPersonID),
			SuppliedName:       nullString(params.SuppliedName),
			SuppliedCallSign:   nullString(params.SuppliedCallSign),
			SuppliedContact:    nullString(params.SuppliedContact),
			StatedRelationship: nullString(params.StatedRelationship),
			Summary:            strings.TrimSpace(params.Summary),
			ReceivedBy:         nullInt(actorID),
			SubmittedAt:        now.UTC().Format(isoTimestamp),
			SourceIpHash:       nullString(params.SourceIPHash),
		})
		if err != nil {
			return err
		}

		for i, in := range params.Items {
			if _, err := q.CreateChangeRequestItem(ctx, sqlcgen.CreateChangeRequestItemParams{
				RequestID:     row.ID,
				Ordinal:       int64(i),
				Operation:     in.Operation,
				ProposedValue: nullString(in.ProposedValue),
				TargetKind:    nullString(in.TargetKind),
				TargetID:      nullInt(in.TargetID),
				TargetVersion: nullInt(in.TargetVersion),
				Sensitivity:   sensitivityOrDefault(in.Sensitivity),
			}); err != nil {
				return err
			}
		}

		if err := claim.Complete(ctx, q, "change_request", row.ID); err != nil {
			return err
		}

		out, err = s.load(ctx, q, row.ID)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return out, nil
}

// Get returns one request with its items.
func (s *Service) Get(ctx context.Context, p *authz.Principal, id int64) (Request, error) {
	r, err := s.load(ctx, s.Q, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return r, err
}

// List returns a deterministic page of requests.
//
// Ordering is submitted_at descending with an id tie-breaker, so two requests
// recorded in the same millisecond cannot swap places between pages and hide a
// row from a paging caller.
func (s *Service) List(ctx context.Context, p *authz.Principal, f ListFilter) ([]Request, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	rows, err := s.Q.ListChangeRequests(ctx, sqlcgen.ListChangeRequestsParams{
		Status:          nullableFilter(f.Status),
		Source:          nullableFilter(f.Source),
		RequesterUserID: nullableIDFilter(f.RequesterUserID),
		UntargetedOnly:  boolToInt(f.UnresolvedTargetOnly),
		PageLimit:       limit,
		PageOffset:      offset,
	})
	if err != nil {
		return nil, err
	}

	out := make([]Request, 0, len(rows))
	for _, row := range rows {
		r := requestFromListRow(row)
		items, err := s.Q.ListChangeRequestItems(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		r.Items = itemsFrom(items)
		r.PendingItems = countPending(r.Items)
		out = append(out, r)
	}
	return out, nil
}

// TriageParams links a request to a canonical person.
type TriageParams struct {
	TargetPersonID  int64
	ExpectedVersion int64
}

// Triage records the officer's conclusion about who an unresolved request
// concerns.
//
// It never rewrites the supplied_* snapshot: what the submitter said stays on
// the record next to what the officer decided it meant. The write is
// version-guarded, so two officers triaging the same public submission cannot
// silently overwrite each other.
func (s *Service) Triage(ctx context.Context, p *authz.Principal, id int64, params TriageParams, now time.Time) (Request, error) {
	var actorID int64
	if p != nil {
		actorID = p.UserID
	}

	var out Request
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := q.GetChangeRequest(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current.Status == StatusResolved || current.Status == StatusWithdrawn {
			return ErrAlreadyResolved
		}
		if params.TargetPersonID != 0 {
			if _, err := q.GetPerson(ctx, params.TargetPersonID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrUnknownPerson
				}
				return err
			}
		}

		if _, err := q.SetChangeRequestTarget(ctx, sqlcgen.SetChangeRequestTargetParams{
			TargetPersonID: nullInt(params.TargetPersonID),
			TriagedBy:      nullInt(actorID),
			TriagedAt:      nullString(now.UTC().Format(isoTimestamp)),
			ID:             id,
			Version:        params.ExpectedVersion,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The row exists — it was read above — so a missing row here
				// means the version moved.
				return db.ErrStale
			}
			return err
		}

		out, err = s.load(ctx, q, id)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return out, nil
}

// GetForRequester returns one request only to the member who submitted it.
//
// The ownership test is part of the read, not a check a caller is trusted to
// have done: an API handler that forgot it would otherwise expose every
// officer-entered request by id, complete with the reviewer's notes.
func (s *Service) GetForRequester(ctx context.Context, p *authz.Principal, id int64) (Request, error) {
	if p == nil || p.UserID == 0 {
		return Request{}, ErrNotYours
	}
	r, err := s.load(ctx, s.Q, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if r.RequesterUserID != p.UserID {
		return Request{}, ErrNotYours
	}
	return r, nil
}

// Withdraw retracts a member's own request while it is still undecided.
//
// It only ever moves the request's status. Items, the supplied snapshot, and
// any decision already recorded are left exactly as they are: withdrawal is the
// member saying "never mind", not a delete, and the audit trail of what was
// asked for survives it. A request an officer has begun deciding cannot be
// withdrawn at all, because retracting a request whose approved item already
// changed a record would leave the record with no stated reason.
func (s *Service) Withdraw(ctx context.Context, p *authz.Principal, id int64, now time.Time) (Request, error) {
	if p == nil || p.UserID == 0 {
		return Request{}, ErrNotYours
	}

	var out Request
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := s.load(ctx, q, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.RequesterUserID != p.UserID {
			return ErrNotYours
		}
		if current.Status == StatusResolved || current.Status == StatusWithdrawn {
			return ErrAlreadyResolved
		}
		for _, it := range current.Items {
			if it.Status != ItemPending {
				return ErrDecidedItems
			}
		}

		if _, err := q.SetChangeRequestStatus(ctx, sqlcgen.SetChangeRequestStatusParams{
			Status:      StatusWithdrawn,
			ResolvedAt:  sql.NullString{},
			WithdrawnAt: nullString(now.UTC().Format(isoTimestamp)),
			ID:          id,
			Version:     current.Version,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The row was read a moment ago, so a missing row here means
				// the version moved: an officer touched it concurrently.
				return db.ErrStale
			}
			return err
		}

		out, err = s.load(ctx, q, id)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return out, nil
}

// --- internals ---

func (s *Service) inTx(ctx context.Context, fn func(*sqlcgen.Queries) error) error {
	return s.inTxWith(ctx, func(q *sqlcgen.Queries, _ *sql.Tx) error { return fn(q) })
}

// inTxWith exposes the transaction itself, so a caller can bind ANOTHER
// service to it. Applying a reviewed item needs that: the decision and the
// canonical write must commit or roll back together, and a members.Service
// built over the bare *sql.DB would write outside the transaction and survive
// a rollback.
func (s *Service) inTxWith(ctx context.Context, fn func(*sqlcgen.Queries, *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.Q.WithTx(tx), tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) load(ctx context.Context, q *sqlcgen.Queries, id int64) (Request, error) {
	row, err := q.GetChangeRequest(ctx, id)
	if err != nil {
		return Request{}, err
	}
	r := requestFrom(row)

	if r.TargetPersonID != 0 {
		if person, err := q.GetPerson(ctx, r.TargetPersonID); err == nil {
			r.TargetDisplayName = person.DisplayName
		}
	}

	items, err := q.ListChangeRequestItems(ctx, id)
	if err != nil {
		return Request{}, err
	}
	r.Items = itemsFrom(items)
	r.PendingItems = countPending(r.Items)
	return r, nil
}

func validateCreate(params CreateParams) error {
	if _, ok := intakeSources[params.Source]; !ok {
		return ErrSourceRequired
	}

	if strings.TrimSpace(params.Summary) == "" {
		return ErrSummaryRequired
	}
	if len(params.Summary) > MaxSummaryLen {
		return fmt.Errorf("%w: summary", ErrTooLong)
	}
	for name, v := range map[string]string{
		"supplied_name":       params.SuppliedName,
		"supplied_call_sign":  params.SuppliedCallSign,
		"supplied_contact":    params.SuppliedContact,
		"stated_relationship": params.StatedRelationship,
	} {
		if len(v) > MaxSuppliedLen {
			return fmt.Errorf("%w: %s", ErrTooLong, name)
		}
	}

	// Something must identify who this concerns, or no officer can ever act on
	// it. A canonical target counts; so does any hint a human could match.
	if params.TargetPersonID == 0 &&
		strings.TrimSpace(params.SuppliedName) == "" &&
		strings.TrimSpace(params.SuppliedCallSign) == "" &&
		strings.TrimSpace(params.SuppliedContact) == "" {
		return ErrNoTarget
	}

	if len(params.Items) == 0 {
		return ErrNoItems
	}
	if len(params.Items) > MaxItems {
		return ErrTooManyItems
	}

	for _, in := range params.Items {
		if !isSupportedOperation(in.Operation) {
			return fmt.Errorf("%w: %s", ErrUnknownOperation, in.Operation)
		}
		if len(in.ProposedValue) > MaxValueLen {
			return fmt.Errorf("%w: proposed_value", ErrTooLong)
		}
		// Which operations need a value is the policy table's answer, not a
		// rule restated here. Archiving a contact and making one primary name
		// a target and change nothing else, so demanding a value would force a
		// submitter to invent a meaningless one.
		if RequiresValue(in.Operation) && strings.TrimSpace(in.ProposedValue) == "" {
			return fmt.Errorf("%w: %s", ErrValueRequired, in.Operation)
		}
		if (in.TargetKind == "") != (in.TargetID == 0) {
			return ErrTargetIncomplete
		}
		if in.TargetKind != "" {
			if _, ok := supportedTargetKinds[in.TargetKind]; !ok {
				return fmt.Errorf("%w: %s", ErrUnknownTargetKind, in.TargetKind)
			}
		}
		switch in.Sensitivity {
		case "", SensitivityOrdinary, SensitivitySensitive:
		default:
			return fmt.Errorf("changerequests: unknown sensitivity %q", in.Sensitivity)
		}
	}
	return nil
}

// createFingerprint defines what makes two intakes the same request for
// idempotency. Every field that changes what is stored is included, so a
// retry with a corrected value is treated as new work rather than replayed as
// the original.
func createFingerprint(actorID int64, params CreateParams) string {
	parts := []string{
		fmt.Sprint(actorID),
		params.Source,
		fmt.Sprint(params.TargetPersonID),
		params.SuppliedName,
		params.SuppliedCallSign,
		params.SuppliedContact,
		params.StatedRelationship,
		strings.TrimSpace(params.Summary),
		fmt.Sprint(params.RequesterUserID),
	}
	for _, in := range params.Items {
		parts = append(parts,
			in.Operation,
			in.ProposedValue,
			in.TargetKind,
			fmt.Sprint(in.TargetID),
			fmt.Sprint(in.TargetVersion),
			sensitivityOrDefault(in.Sensitivity),
		)
	}
	return idem.Hash(parts...)
}

func isSupportedOperation(op string) bool {
	for _, s := range SupportedOperations {
		if s == op {
			return true
		}
	}
	return false
}

func sensitivityOrDefault(s string) string {
	if s == "" {
		return SensitivityOrdinary
	}
	return s
}

func countPending(items []Item) int64 {
	var n int64
	for _, it := range items {
		if it.Status == ItemPending {
			n++
		}
	}
	return n
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(i int64) sql.NullInt64 {
	if i == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: i, Valid: true}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/dues"
)

// --- Response types ---
//
// DuesStanding is the safe summary. Every field here is visible to any caller
// holding dues.read, which includes the president, vice-president, and
// secretary. Payment amounts, methods, references, receipts, and treasurer
// notes are deliberately absent — those require payment.read and live on the
// payment endpoints.

type HonoraryWaiver struct {
	Kind   string `json:"kind" enum:"lifetime,fixed_term" doc:"Whether the waiver is for life or for a fixed term."`
	EndsOn string `json:"ends_on,omitempty" format:"date" doc:"End date of a fixed-term waiver, when one is set."`
}

type DuesStanding struct {
	MembershipID int64  `json:"membership_id"`
	PersonID     int64  `json:"person_id"`
	DisplayName  string `json:"display_name"`
	CallSign     string `json:"call_sign,omitempty"`
	BaseType     string `json:"base_type" enum:"full,associate" doc:"Underlying membership right. An honorary waiver never changes this."`
	Lifecycle    string `json:"lifecycle"`
	Status       string `json:"status" enum:"honorary_waived,current,expiring,expired,unknown"`
	PaidThrough  string `json:"paid_through,omitempty" format:"date" doc:"Effective coverage date; absent when no decision has been recorded."`
	AsOf         string `json:"as_of" format:"date" doc:"The date this standing was judged against."`
	WarningDays  int    `json:"warning_days" doc:"Look-ahead window used to classify expiring."`

	Honorary *HonoraryWaiver `json:"honorary,omitempty" doc:"Present only when status is honorary_waived."`

	CoverageEventID int64 `json:"coverage_event_id,omitempty" doc:"The effective coverage event, for reading its history."`
}

type CoverageEvent struct {
	ID                int64  `json:"id"`
	MembershipID      int64  `json:"membership_id"`
	PaidThrough       string `json:"paid_through" format:"date"`
	ReasonKind        string `json:"reason_kind" enum:"payment,correction,adjustment,legacy_import,import"`
	Reason            string `json:"reason,omitempty"`
	PaymentID         int64  `json:"payment_id,omitempty"`
	ImportRunID       int64  `json:"import_run_id,omitempty"`
	SupersedesEventID int64  `json:"supersedes_event_id,omitempty" doc:"The decision this one replaced, when it replaced any."`
	DecidedByUserID   int64  `json:"decided_by_user_id,omitempty" doc:"Absent for system decisions such as the legacy import."`
	DecidedAt         string `json:"decided_at" format:"date-time"`
}

type DuesRate struct {
	Year        int64  `json:"year"`
	AmountCents int64  `json:"amount_cents"`
	Note        string `json:"note,omitempty"`
	SetByUserID int64  `json:"set_by_user_id"`
	SetAt       string `json:"set_at" format:"date-time"`
	Version     int64  `json:"version"`
}

type DuesSuggestion struct {
	PaidThrough  string `json:"paid_through" format:"date"`
	Label        string `json:"label"`
	YearsCovered int    `json:"years_covered"`
	AmountCents  int64  `json:"amount_cents,omitempty" doc:"Rate-derived hint. Zero when rate_known is false."`
	RateKnown    bool   `json:"rate_known" doc:"False when a covered year has no rate on file."`
	Explanation  string `json:"explanation"`
}

type DuesSuggestions struct {
	AsOf    string           `json:"as_of" format:"date"`
	Binding bool             `json:"binding" doc:"Always false. Suggestions are display hints, never validation."`
	Choices []DuesSuggestion `json:"choices"`
	Note    string           `json:"note"`
}

// --- Inputs and outputs ---

type DuesStandingListInput struct {
	PageQuery
	AsOf        string `query:"as_of" doc:"Judge standing as of this ISO date. Defaults to today (UTC)."`
	WarningDays int    `query:"warning_days" minimum:"1" maximum:"365" doc:"Expiring look-ahead in days. Defaults to 60."`
	Status      string `query:"status" enum:"honorary_waived,current,expiring,expired,unknown" doc:"Filter to one derived status."`
	Q           string `query:"q" doc:"Match display name or call sign."`
}
type DuesStandingListOutput struct {
	Body Page[DuesStanding]
}

type MembershipStandingInput struct {
	ID          int64  `path:"id"`
	AsOf        string `query:"as_of" doc:"Judge standing as of this ISO date. Defaults to today (UTC)."`
	WarningDays int    `query:"warning_days" minimum:"1" maximum:"365" doc:"Expiring look-ahead in days. Defaults to 60."`
}
type MembershipStandingOutput struct {
	Body DuesStanding
}

type DuesSuggestionsInput struct {
	AsOf string `query:"as_of" doc:"Base the suggestions on this ISO date. Defaults to today (UTC)."`
}
type DuesSuggestionsOutput struct {
	Body DuesSuggestions
}

type CoverageEventsListInput struct {
	PageQuery
	ID int64 `path:"id"`
}
type CoverageEventsListOutput struct {
	Body Page[CoverageEvent]
}

type CreateCoverageEventBody struct {
	PaidThrough string `json:"paid_through" format:"date" doc:"The coverage date being granted. Any real date is accepted; the club year-end is a convention, not a rule."`
	Reason      string `json:"reason" minLength:"1" doc:"Plain-language explanation, required so the history reads back."`
}
type CreateCoverageEventInput struct {
	ID   int64 `path:"id"`
	Body CreateCoverageEventBody
}
type CreateCoverageEventOutput struct {
	Body CoverageEvent
}

type DuesRatesListOutput struct {
	Body Page[DuesRate]
}

type PutDuesRateBody struct {
	AmountCents int64  `json:"amount_cents" minimum:"0"`
	Note        string `json:"note,omitempty"`
}
type PutDuesRateInput struct {
	Year    int64  `path:"year" minimum:"1900" maximum:"2999"`
	IfMatch string `header:"If-Match" doc:"Version of the rate being revised, as returned in ETag. Omit to create a rate for a year that has none."`
	Body    PutDuesRateBody
}
type PutDuesRateOutput struct {
	ETag string `header:"ETag"`
	Body DuesRate
}

// --- Conversions ---

func duesStandingToResponse(s dues.Standing) DuesStanding {
	out := DuesStanding{
		MembershipID:    s.MembershipID,
		PersonID:        s.PersonID,
		DisplayName:     s.DisplayName,
		CallSign:        s.CallSign,
		BaseType:        s.BaseType,
		Lifecycle:       s.Lifecycle,
		Status:          s.Status,
		PaidThrough:     s.PaidThrough,
		AsOf:            s.AsOf,
		WarningDays:     s.WarningDays,
		CoverageEventID: s.CoverageEventID,
	}
	if s.Honorary != nil {
		out.Honorary = &HonoraryWaiver{Kind: s.Honorary.Kind, EndsOn: s.Honorary.EndsOn}
	}
	return out
}

func coverageEventToResponse(e sqlcgen.CoverageEvent) CoverageEvent {
	return CoverageEvent{
		ID:                e.ID,
		MembershipID:      e.MembershipID,
		PaidThrough:       e.PaidThrough,
		ReasonKind:        e.ReasonKind,
		Reason:            e.Reason.String,
		PaymentID:         e.PaymentID.Int64,
		ImportRunID:       e.ImportRunID.Int64,
		SupersedesEventID: e.SupersedesEventID.Int64,
		DecidedByUserID:   e.DecidedBy.Int64,
		DecidedAt:         e.DecidedAt,
	}
}

func duesRateToResponse(r sqlcgen.DuesRate) DuesRate {
	return DuesRate{
		Year:        r.Year,
		AmountCents: r.AmountCents,
		Note:        r.Note.String,
		SetByUserID: r.SetBy,
		SetAt:       r.SetAt,
		Version:     r.Version,
	}
}

// parseAsOf reads an optional ISO date query parameter.
func parseAsOf(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(dues.ISODate, raw)
	if err != nil {
		return time.Time{}, huma.Error422UnprocessableEntity(
			fmt.Sprintf("as_of must be an ISO date (YYYY-MM-DD), got %q", raw))
	}
	return t, nil
}

// parseIfMatchValue reads the version from an If-Match header value. An empty
// header means "no version presented".
func parseIfMatchValue(raw string) (int64, error) {
	raw = strings.Trim(strings.TrimSpace(raw), `"`)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, huma.Error422UnprocessableEntity(
			fmt.Sprintf("If-Match must be a quoted positive integer version, got %q", raw))
	}
	return v, nil
}

// mapDuesError translates dues-domain errors to HTTP status codes, falling
// through to the shared mapper for authorization, staleness, and not-found.
func mapDuesError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, dues.ErrReasonRequired):
		return huma.Error422UnprocessableEntity("a reason is required")
	case errors.Is(err, dues.ErrInvalidDate):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, dues.ErrUnknownStatus):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, dues.ErrRateExists):
		return ErrConflict("a rate for that year already exists; send If-Match with its version to revise it")
	}
	return mapDomainError(err)
}

// offsetFromCursor decodes the shared offset-style cursor used by list
// endpoints in this API.
func offsetFromCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := DecodeCursor(cursor)
	if err != nil {
		return 0, huma.Error422UnprocessableEntity("invalid cursor")
	}
	off, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || off < 0 {
		return 0, huma.Error422UnprocessableEntity("invalid cursor")
	}
	return off, nil
}

// nextCursor returns the cursor for the following page, or "" on the last page.
func nextCursor(returned int, limit, offset int64) string {
	if int64(returned) < limit {
		return ""
	}
	return EncodeCursor(strconv.FormatInt(offset+limit, 10))
}

// RegisterDues registers dues standing, rate, suggestion, and coverage
// endpoints.
func RegisterDues(api huma.API, deps Deps) {
	var svc *dues.Service
	if deps.DB != nil {
		svc = dues.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "dues-standing-list",
		Method:      http.MethodGet,
		Path:        "/dues-standing",
		Summary:     "List memberships by derived dues standing",
		Description: "Returns the safe standing summary only. Payment amounts, methods, " +
			"references, receipts, and treasurer notes are not included at any capability level.",
		Tags: []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "dues.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *DuesStandingListInput) (*DuesStandingListOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		asOf, err := parseAsOf(input.AsOf)
		if err != nil {
			return nil, err
		}
		offset, err := offsetFromCursor(input.Cursor)
		if err != nil {
			return nil, err
		}
		limit := int64(input.Limit)
		if limit <= 0 {
			limit = dues.DefaultLimit
		}

		rows, err := svc.ListStanding(ctx, principal, dues.StandingQuery{
			AsOf:        asOf,
			WarningDays: input.WarningDays,
			Status:      input.Status,
			Search:      input.Q,
			Limit:       limit,
			Offset:      offset,
		})
		if err != nil {
			return nil, mapDuesError(err)
		}

		data := make([]DuesStanding, len(rows))
		for i, r := range rows {
			data[i] = duesStandingToResponse(r)
		}
		return &DuesStandingListOutput{Body: Page[DuesStanding]{
			Data:       data,
			NextCursor: nextCursor(len(rows), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "membership-dues-standing",
		Method:      http.MethodGet,
		Path:        "/memberships/{id}/dues-standing",
		Summary:     "Read one membership's derived dues standing",
		Tags:        []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "dues.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *MembershipStandingInput) (*MembershipStandingOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		asOf, err := parseAsOf(input.AsOf)
		if err != nil {
			return nil, err
		}
		standing, err := svc.GetStanding(ctx, principal, input.ID, asOf, input.WarningDays)
		if err != nil {
			return nil, mapDuesError(err)
		}
		return &MembershipStandingOutput{Body: duesStandingToResponse(standing)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "dues-suggestions",
		Method:      http.MethodGet,
		Path:        "/dues-suggestions",
		Summary:     "Non-binding paid-through choices with explanations",
		Description: "Display hints only. The client submits the date the treasurer chose; " +
			"the server never derives a paid-through date from an amount.",
		Tags: []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "dues.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *DuesSuggestionsInput) (*DuesSuggestionsOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		asOf, err := parseAsOf(input.AsOf)
		if err != nil {
			return nil, err
		}
		s, err := svc.SuggestFor(ctx, principal, asOf)
		if err != nil {
			return nil, mapDuesError(err)
		}
		out := DuesSuggestions{AsOf: s.AsOf, Binding: s.Binding, Note: s.Note}
		for _, c := range s.Choices {
			out.Choices = append(out.Choices, DuesSuggestion{
				PaidThrough:  c.PaidThrough,
				Label:        c.Label,
				YearsCovered: c.YearsCovered,
				AmountCents:  c.AmountCents,
				RateKnown:    c.RateKnown,
				Explanation:  c.Explanation,
			})
		}
		return &DuesSuggestionsOutput{Body: out}, nil
	})

	Register(api, huma.Operation{
		OperationID: "dues-rates-list",
		Method:      http.MethodGet,
		Path:        "/dues-rates",
		Summary:     "List dues rates by effective year",
		Tags:        []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "dues.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, _ *struct{}) (*DuesRatesListOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		rates, err := svc.ListRates(ctx, principal)
		if err != nil {
			return nil, mapDuesError(err)
		}
		data := make([]DuesRate, len(rates))
		for i, r := range rates {
			data[i] = duesRateToResponse(r)
		}
		return &DuesRatesListOutput{Body: Page[DuesRate]{Data: data}}, nil
	})

	Register(api, huma.Operation{
		OperationID: "dues-rate-put",
		Method:      http.MethodPut,
		Path:        "/dues-rates/{year}",
		Summary:     "Create or revise the dues rate for a year",
		Description: "Omit If-Match to create a rate for a year that has none. Send If-Match " +
			"with the version you read to revise one. A rate informs suggestions and never " +
			"validates a payment amount.",
		Tags: []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "dues.rate.manage",
		AuditAction:        "dues.rate.set",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *PutDuesRateInput) (*PutDuesRateOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		expected, err := parseIfMatchValue(input.IfMatch)
		if err != nil {
			return nil, err
		}
		rate, err := svc.SetRate(ctx, principal, input.Year, input.Body.AmountCents,
			input.Body.Note, expected, time.Now())
		if err != nil {
			return nil, mapDuesError(err)
		}
		audit.StampResource(ctx, "dues_rate", rate.Year)
		return &PutDuesRateOutput{
			ETag: FormatETag(rate.Version),
			Body: duesRateToResponse(rate),
		}, nil
	})

	Register(api, huma.Operation{
		OperationID: "coverage-events-list",
		Method:      http.MethodGet,
		Path:        "/memberships/{id}/coverage-events",
		Summary:     "List the append-only paid-through history",
		Description: "Superseded decisions remain visible; that is how the history explains itself.",
		Tags:        []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "coverage.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *CoverageEventsListInput) (*CoverageEventsListOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		offset, err := offsetFromCursor(input.Cursor)
		if err != nil {
			return nil, err
		}
		limit := int64(input.Limit)
		if limit <= 0 {
			limit = dues.DefaultLimit
		}
		events, err := svc.ListCoverageEvents(ctx, principal, input.ID, limit, offset)
		if err != nil {
			return nil, mapDuesError(err)
		}
		data := make([]CoverageEvent, len(events))
		for i, e := range events {
			data[i] = coverageEventToResponse(e)
		}
		return &CoverageEventsListOutput{Body: Page[CoverageEvent]{
			Data:       data,
			NextCursor: nextCursor(len(events), limit, offset),
		}}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "coverage-event-create",
		Method:        http.MethodPost,
		Path:          "/memberships/{id}/coverage-events",
		Summary:       "Record an independent paid-through adjustment",
		Description:   "Appends a new decision superseding the effective one. Nothing is rewritten or deleted.",
		DefaultStatus: http.StatusCreated,
		Tags:          []string{"dues"},
	}, OperationMeta{
		RequiredCapability: "coverage.adjust",
		AuditAction:        "coverage.adjust",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateCoverageEventInput) (*CreateCoverageEventOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		event, err := svc.AdjustCoverage(ctx, principal, input.ID,
			input.Body.PaidThrough, input.Body.Reason, time.Now())
		if err != nil {
			return nil, mapDuesError(err)
		}
		audit.StampResource(ctx, "coverage_event", event.ID)
		return &CreateCoverageEventOutput{Body: coverageEventToResponse(event)}, nil
	})
}

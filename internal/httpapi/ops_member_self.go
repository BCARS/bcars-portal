package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/domain/authz"
	"github.com/bcars/bcars-portal/internal/domain/changerequests"
	"github.com/bcars/bcars-portal/internal/domain/memberprofile"
)

// Member self-service (bcars-portal-4ux.6).
//
// Everything here lives under /me and takes its subject from the session. There
// is no user_id path parameter anywhere in this file, which is deliberate: an
// endpoint that accepted one would be one authorization bug away from reading
// somebody else's profile, and no amount of checking inside the handler makes
// that parameter safe to have offered.
//
// The two authorities are kept apart, because the corrected Phase 3 plan
// separates them (ADR-0010, bcars-portal-4ux.16):
//
//   - profile.self.read answers WHICH RECORDS MAY I SEE, and only records an
//     officer explicitly granted;
//   - change_request.submit.member answers MAY I PROPOSE A CORRECTION AT ALL,
//     about myself or about another person.
//
// Neither implies the other. An Associate with no grant to anyone may suggest a
// correction about another member and learns nothing about them by doing it:
// submission performs no lookup, echoes no current value, returns no match
// candidate, and confers no later access.

// --- Response types ---

// MemberContact is one contact method on a granted record.
type MemberContact struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
	// SharedWith is the audience the club currently shares this value with, so
	// a member can see what is published without having to ask an officer.
	// Empty means no explicit decision is on file.
	SharedWith string `json:"shared_with,omitempty"`
	Version    int64  `json:"version" doc:"Send as target_version when proposing a correction to this value."`
}

// MemberDuesStanding is the safe dues summary a member may see.
//
// It is the derived Phase 2 standing and nothing else. There is no payment,
// amount, method, reference, batch, receipt, or treasurer note in it, and no
// field here is computed from one.
type MemberDuesStanding struct {
	Status      string `json:"status" enum:"current,expiring,expired,unknown,honorary_waived"`
	PaidThrough string `json:"paid_through,omitempty" format:"date"`
	AsOf        string `json:"as_of" format:"date"`
}

// MemberProfile is one granted record as its member sees it.
type MemberProfile struct {
	PersonID    int64  `json:"person_id"`
	DisplayName string `json:"display_name"`
	CallSign    string `json:"call_sign,omitempty"`
	// Version is the record's version, for the same reason MemberContact
	// carries one: a correction to the name or call sign should say which
	// version it was written against, and until now a client had no way to
	// learn it. Present on a single-record read; a list omits it.
	Version    int64  `json:"version,omitempty" doc:"Send as target_version when proposing a correction to the name or call sign."`
	AccessKind string `json:"access_kind" enum:"self,delegate" doc:"Why you can see this record. Both kinds read the same fields."`

	BaseType  string `json:"base_type,omitempty" enum:"full,associate" doc:"The underlying membership right. An honorary waiver changes dues, never this."`
	Lifecycle string `json:"lifecycle,omitempty"`

	Standing *MemberDuesStanding `json:"dues_standing,omitempty"`
	Contacts []MemberContact     `json:"contacts,omitempty" doc:"Present on a single-record read."`
}

// MemberRequestItem is one proposal as its submitter sees it.
//
// The reviewer's identity and the verification note are absent on purpose. A
// member is entitled to know the decision and the reason for a rejection; who
// decided it and how they verified it is officer working material.
type MemberRequestItem struct {
	ID             int64  `json:"id"`
	Ordinal        int64  `json:"ordinal"`
	Operation      string `json:"operation"`
	ProposedValue  string `json:"proposed_value,omitempty"`
	Status         string `json:"status" enum:"pending,approved,rejected,needs_verification"`
	DecisionReason string `json:"decision_reason,omitempty" doc:"Why an officer rejected this item."`

	// AppliedValue is what the officer actually wrote, which may differ from
	// what this member proposed.
	//
	// Sent ONLY for a request about a record the caller may see. On a
	// suggestion about somebody else it stays absent, for the same reason the
	// canonical target link does: "your suggestion was applied as
	// 814-555-0199" is a statement about a stranger's record, and submitting a
	// suggestion has never been a way to read one.
	AppliedValue *string `json:"applied_value,omitempty" doc:"What the officer applied, when it differs from what you proposed. Only present for a record you may see."`
}

// MemberRequest is one of the caller's own submissions.
type MemberRequest struct {
	ID     int64  `json:"id"`
	Status string `json:"status" enum:"draft,submitted,in_review,resolved,withdrawn"`

	// AboutPersonID is echoed only for a record the caller may see. It stays
	// empty for a suggestion about someone else even after an officer links the
	// request, because learning the club's id for a person is learning that the
	// person is on file.
	AboutPersonID int64 `json:"about_person_id,omitempty"`
	// AboutName and AboutCallSign are what the CALLER typed, returned so their
	// own submission reads back to them. They are never replaced by anything
	// canonical.
	AboutName     string `json:"about_name,omitempty"`
	AboutCallSign string `json:"about_call_sign,omitempty"`

	Summary     string `json:"summary"`
	SubmittedAt string `json:"submitted_at" format:"date-time"`
	ResolvedAt  string `json:"resolved_at,omitempty" format:"date-time"`
	WithdrawnAt string `json:"withdrawn_at,omitempty" format:"date-time"`

	Items   []MemberRequestItem `json:"items"`
	Version int64               `json:"version"`
}

// --- Inputs and outputs ---

type ListMyRecordsInput struct{}
type ListMyRecordsOutput struct {
	Body []MemberProfile
}

type GetMyRecordInput struct {
	PersonID int64 `path:"person_id"`
}
type GetMyRecordOutput struct {
	Body MemberProfile
}

type MemberRequestItemBody struct {
	Operation     string `json:"operation" minLength:"1" doc:"Must be an allowlisted operation. There is no arbitrary field path. About somebody else, only 'other' is accepted: such a report is a note an officer acts on, not a change anything can apply."`
	ProposedValue string `json:"proposed_value,omitempty" maxLength:"2000" doc:"Required for every operation except 'other'."`
	TargetKind    string `json:"target_kind,omitempty" enum:"person,contact_method" doc:"Only for one of your own records, and only a resource on that record."`
	TargetID      int64  `json:"target_id,omitempty"`
	TargetVersion int64  `json:"target_version,omitempty" doc:"The version you were looking at, so an officer can tell if it changed since."`
}

type SubmitMyRequestBody struct {
	AboutPersonID int64  `json:"about_person_id,omitempty" doc:"One of your own records. Omit it to report something about someone else, which is a note: describe them and say what you know, and an officer acts on it."`
	AboutName     string `json:"about_name,omitempty" maxLength:"200" doc:"Who this is about, in your words. No lookup is performed and nothing is confirmed."`
	AboutCallSign string `json:"about_call_sign,omitempty" maxLength:"200"`

	StatedRelationship string `json:"stated_relationship,omitempty" maxLength:"200" doc:"How you know them. Informational only; it grants nothing."`

	Summary string                  `json:"summary" minLength:"1" maxLength:"4000" doc:"What should change, in plain language."`
	Items   []MemberRequestItemBody `json:"items" minItems:"1" maxItems:"25"`
}

type SubmitMyRequestInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Required. A retry with the same key returns the original submission rather than filing a second one."`
	Body           SubmitMyRequestBody
}

type SubmitMyRequestOutput struct {
	Body MemberRequest
}

type ListMyRequestsInput struct {
	Status string `query:"status" enum:"draft,submitted,in_review,resolved,withdrawn" doc:"Optional exact filter."`
	Limit  int64  `query:"limit" minimum:"1" maximum:"200" doc:"Defaults to 50."`
	Offset int64  `query:"offset" minimum:"0"`
}
type ListMyRequestsOutput struct {
	Body []MemberRequest
}

type GetMyRequestInput struct {
	ID int64 `path:"id"`
}
type GetMyRequestOutput struct {
	Body MemberRequest
}

type WithdrawMyRequestInput struct {
	ID int64 `path:"id"`
}
type WithdrawMyRequestOutput struct {
	Body MemberRequest
}

// --- Conversions ---

func memberProfileToResponse(p memberprofile.Profile) MemberProfile {
	out := MemberProfile{
		PersonID:    p.PersonID,
		DisplayName: p.DisplayName,
		CallSign:    p.CallSign,
		Version:     p.PersonVersion,
		AccessKind:  p.AccessKind,
		BaseType:    p.BaseType,
		Lifecycle:   p.Lifecycle,
	}
	if p.Standing != nil {
		out.Standing = &MemberDuesStanding{
			Status:      p.Standing.Status,
			PaidThrough: p.Standing.PaidThrough,
			AsOf:        p.Standing.AsOf,
		}
	}
	for _, c := range p.Contacts {
		out.Contacts = append(out.Contacts, MemberContact{
			ID:         c.ID,
			Kind:       c.Kind,
			Label:      c.Label,
			Value:      c.Value,
			Primary:    c.Primary,
			SharedWith: c.SharedWith,
			Version:    c.Version,
		})
	}
	return out
}

// memberRequestToResponse narrows an officer-shaped request to what its
// submitter may see.
//
// It is a separate conversion rather than a filtered reuse of
// changeRequestToResponse. A shared converter with a "hide some fields" flag is
// one forgotten call site away from returning the reviewer's notes to the
// member, and the fields are few enough to state.
//
// visibleTarget reports whether the caller holds a grant to the linked person.
// When they do not, the canonical link an officer added during triage stays
// hidden: the member said "I think Marguerite's number is wrong", and the reply
// must not become confirmation that Marguerite is on file.
func memberRequestToResponse(r changerequests.Request, visibleTarget bool) MemberRequest {
	out := MemberRequest{
		ID:            r.ID,
		Status:        r.Status,
		AboutName:     r.SuppliedName,
		AboutCallSign: r.SuppliedCallSign,
		Summary:       r.Summary,
		SubmittedAt:   r.SubmittedAt,
		ResolvedAt:    r.ResolvedAt,
		WithdrawnAt:   r.WithdrawnAt,
		Version:       r.Version,
	}
	if visibleTarget {
		out.AboutPersonID = r.TargetPersonID
	}
	for _, it := range r.Items {
		row := MemberRequestItem{
			ID:             it.ID,
			Ordinal:        it.Ordinal,
			Operation:      it.Operation,
			ProposedValue:  it.ProposedValue,
			Status:         it.Status,
			DecisionReason: it.DecisionReason,
		}
		if visibleTarget {
			row.AppliedValue = appliedValueOrNil(it)
		}
		out.Items = append(out.Items, row)
	}
	return out
}

func mapMemberProfileError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memberprofile.ErrNotFound):
		// The same answer for "no such person" and "not one of yours". A
		// separate message would make the id parameter a membership oracle.
		return huma.Error404NotFound("no such record")
	}
	return mapDomainError(err)
}

func mapMemberRequestError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, changerequests.ErrNotYours):
		// Deliberately identical to ErrNotFound's answer: telling a member that
		// a request exists but is not theirs would let them count the queue.
		return huma.Error404NotFound("change request not found")
	case errors.Is(err, changerequests.ErrDecidedItems):
		return huma.Error409Conflict(
			"an officer has already decided part of this request; ask an officer rather than withdrawing it")
	}
	return mapChangeRequestError(err)
}

// RegisterMemberSelfService registers the member profile and request endpoints.
func RegisterMemberSelfService(api huma.API, deps Deps) {
	var (
		profiles *memberprofile.Service
		requests *changerequests.Service
	)
	if deps.DB != nil {
		profiles = memberprofile.NewService(deps.DB)
		requests = changerequests.NewService(deps.DB)
	}

	// grantedTo reports whether this caller may currently see that person, and
	// returns the record when they may. Every place that needs the answer asks
	// the same way, so submission and reading cannot disagree about it.
	grantedTo := func(ctx context.Context, p *authz.Principal, personID int64) (memberprofile.Profile, bool) {
		if profiles == nil || personID == 0 {
			return memberprofile.Profile{}, false
		}
		profile, err := profiles.Get(ctx, p, personID)
		if err != nil {
			return memberprofile.Profile{}, false
		}
		return profile, true
	}

	// visibleRecords is the same question asked once for a whole page.
	//
	// A list of requests needs only "may I see this person", not the person's
	// contacts and dues standing, and calling grantedTo per row would fetch
	// both for every row. The set is read once per request rather than cached
	// across requests, so a grant revoked a moment ago is already gone from it.
	visibleRecords := func(ctx context.Context, p *authz.Principal) map[int64]struct{} {
		out := map[int64]struct{}{}
		if profiles == nil {
			return out
		}
		granted, err := profiles.List(ctx, p)
		if err != nil {
			return out
		}
		for _, g := range granted {
			out[g.PersonID] = struct{}{}
		}
		return out
	}

	Register(api, huma.Operation{
		OperationID: "member-records-list",
		Method:      http.MethodGet,
		Path:        "/me/records",
		Summary:     "List the records you may see",
		Description: "Every person record an officer explicitly granted to your account, with the safe " +
			"dues summary for each. Access comes only from those grants: a matching contact address " +
			"or a family relationship confers nothing.",
		Tags: []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: memberprofile.Capability,
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListMyRecordsInput) (*ListMyRecordsOutput, error) {
		if profiles == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		found, err := profiles.List(ctx, principal)
		if err != nil {
			return nil, mapMemberProfileError(err)
		}

		out := &ListMyRecordsOutput{Body: make([]MemberProfile, 0, len(found))}
		for _, p := range found {
			out.Body = append(out.Body, memberProfileToResponse(p))
		}
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-record-get",
		Method:      http.MethodGet,
		Path:        "/me/records/{person_id}",
		Summary:     "Read one of your records",
		Description: "A record you were never granted and a record that does not exist both answer 404. " +
			"Contains no payment detail and no officer or treasurer notes.",
		Tags: []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: memberprofile.Capability,
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GetMyRecordInput) (*GetMyRecordOutput, error) {
		if profiles == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		profile, err := profiles.Get(ctx, principal, input.PersonID)
		if err != nil {
			return nil, mapMemberProfileError(err)
		}
		return &GetMyRecordOutput{Body: memberProfileToResponse(profile)}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "member-request-submit",
		Method:        http.MethodPost,
		Path:          "/me/change-requests",
		Summary:       "Suggest a correction",
		DefaultStatus: http.StatusCreated,
		Description: "Files a suggestion for officer review. It changes no record: canonical data moves " +
			"only when an officer approves a supported item. Name one of your own records with " +
			"about_person_id, or omit it and describe the person instead — describing someone performs " +
			"no lookup and tells you nothing about whether they are on file.",
		Tags: []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: "change_request.submit.member",
		AuditAction:        "change_request.submit",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *SubmitMyRequestInput) (*SubmitMyRequestOutput, error) {
		if requests == nil || profiles == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if input.IdempotencyKey == "" {
			return nil, huma.Error400BadRequest("Idempotency-Key is required")
		}

		params := changerequests.CreateParams{
			Source:             changerequests.SourceMember,
			RequesterUserID:    principal.UserID,
			SuppliedName:       input.Body.AboutName,
			SuppliedCallSign:   input.Body.AboutCallSign,
			StatedRelationship: input.Body.StatedRelationship,
			Summary:            input.Body.Summary,
			SourceIPHash:       ClientIPHashFrom(ctx),
		}

		var about memberprofile.Profile
		aboutSelf := false
		if input.Body.AboutPersonID != 0 {
			granted, ok := grantedTo(ctx, principal, input.Body.AboutPersonID)
			if !ok {
				// One message whether the person exists or not. Answering
				// "no such person" here would turn submission into the lookup
				// this endpoint exists not to be.
				return nil, huma.Error422UnprocessableEntity(
					"about_person_id is not one of your records; omit it and describe the person instead")
			}
			about = granted
			aboutSelf = true
			params.TargetPersonID = granted.PersonID
		}

		items, err := memberItems(input.Body.Items, aboutSelf, about)
		if err != nil {
			return nil, err
		}
		params.Items = items

		created, err := requests.Create(ctx, principal, params, input.IdempotencyKey, time.Now())
		if err != nil {
			return nil, mapMemberRequestError(err)
		}
		audit.StampResource(ctx, "change_request", created.ID)

		return &SubmitMyRequestOutput{Body: memberRequestToResponse(created, aboutSelf)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-requests-list",
		Method:      http.MethodGet,
		Path:        "/me/change-requests",
		Summary:     "List your own suggestions",
		Description: "Only requests you submitted. Officer-entered requests about you are not yours and " +
			"do not appear.",
		Tags: []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: "change_request.submit.member",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListMyRequestsInput) (*ListMyRequestsOutput, error) {
		if requests == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		found, err := requests.List(ctx, principal, changerequests.ListFilter{
			Status: input.Status,
			// The filter is the authorization. It is set from the session, not
			// from any input, so there is no value a caller can send that
			// widens it.
			RequesterUserID: principal.UserID,
			Limit:           input.Limit,
			Offset:          input.Offset,
		})
		if err != nil {
			return nil, mapMemberRequestError(err)
		}

		visible := visibleRecords(ctx, principal)
		out := &ListMyRequestsOutput{Body: make([]MemberRequest, 0, len(found))}
		for _, r := range found {
			_, ok := visible[r.TargetPersonID]
			out.Body = append(out.Body, memberRequestToResponse(r, ok))
		}
		return out, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-request-get",
		Method:      http.MethodGet,
		Path:        "/me/change-requests/{id}",
		Summary:     "Read one of your own suggestions",
		Description: "A request you did not submit answers 404, the same as one that does not exist.",
		Tags:        []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: "change_request.submit.member",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GetMyRequestInput) (*GetMyRequestOutput, error) {
		if requests == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		found, err := requests.GetForRequester(ctx, principal, input.ID)
		if err != nil {
			return nil, mapMemberRequestError(err)
		}
		_, visible := grantedTo(ctx, principal, found.TargetPersonID)
		return &GetMyRequestOutput{Body: memberRequestToResponse(found, visible)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "member-request-withdraw",
		Method:      http.MethodPost,
		Path:        "/me/change-requests/{id}/withdrawal",
		Summary:     "Withdraw one of your own suggestions",
		Description: "Retracts a suggestion no officer has started deciding. Nothing is deleted: what you " +
			"asked for stays on the record, marked withdrawn.",
		Tags: []string{"member-self-service"},
	}, OperationMeta{
		RequiredCapability: "change_request.submit.member",
		AuditAction:        "change_request.withdraw",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *WithdrawMyRequestInput) (*WithdrawMyRequestOutput, error) {
		if requests == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		withdrawn, err := requests.Withdraw(ctx, principal, input.ID, time.Now())
		if err != nil {
			return nil, mapMemberRequestError(err)
		}
		audit.StampResource(ctx, "change_request", withdrawn.ID)

		_, visible := grantedTo(ctx, principal, withdrawn.TargetPersonID)
		return &WithdrawMyRequestOutput{Body: memberRequestToResponse(withdrawn, visible)}, nil
	})
}

// memberItems converts submitted items and bounds what a member may point one
// at.
//
// A member may name a resource ONLY on a record they hold, and only a resource
// that belongs to that record. Without this, a member suggesting a correction
// about someone else could attach target_kind=contact_method with a guessed id
// and have an officer review a proposal aimed at a stranger's row — and, worse,
// discover from the response whether that id existed.
//
// Sensitivity is not accepted from the submitter at all. The checked-in policy
// sets the floor for each operation at review time, so there is nothing here a
// member could declare in order to make a call-sign change look ordinary.
func memberItems(in []MemberRequestItemBody, aboutSelf bool, about memberprofile.Profile) ([]changerequests.ItemInput, error) {
	ownContacts := make(map[int64]struct{}, len(about.Contacts))
	for _, c := range about.Contacts {
		ownContacts[c.ID] = struct{}{}
	}

	out := make([]changerequests.ItemInput, 0, len(in))
	for i, item := range in {
		converted := changerequests.ItemInput{
			Operation:     item.Operation,
			ProposedValue: item.ProposedValue,
			TargetVersion: item.TargetVersion,
		}

		// A submission about SOMEBODY ELSE carries no structured change
		// (ADR-0014.4). It used to, and nothing could ever apply the result:
		// the item named no record, and linking the request did not give the
		// item one, so an officer was told to link what they had just linked
		// (bcars-portal-3la). The words were always the useful part.
		//
		// Refused rather than quietly converted to a note: a client that
		// believes it proposed a call-sign change should be told it did not.
		if !aboutSelf && item.Operation != changerequests.OpOther {
			return nil, huma.Error422UnprocessableEntity(
				"items[" + strconv.Itoa(i) + "]: a suggestion about someone else carries no structured change; " +
					"use operation \"other\" and describe it in the summary, which an officer reads and acts on")
		}

		switch {
		case item.TargetKind == "" && item.TargetID == 0:
			// No target named. Allowed for the caller's own record, where an
			// officer resolves what the item concerns during review.
		case !aboutSelf:
			return nil, huma.Error422UnprocessableEntity(
				"items[" + strconv.Itoa(i) + "]: a suggestion about someone else cannot name a target resource")
		case item.TargetKind == "person":
			if item.TargetID != about.PersonID {
				return nil, huma.Error422UnprocessableEntity(
					"items[" + strconv.Itoa(i) + "]: target_id must be the record this request is about")
			}
			converted.TargetKind = item.TargetKind
			converted.TargetID = item.TargetID
		case item.TargetKind == "contact_method":
			if _, ok := ownContacts[item.TargetID]; !ok {
				return nil, huma.Error422UnprocessableEntity(
					"items[" + strconv.Itoa(i) + "]: target_id is not a contact method on that record")
			}
			converted.TargetKind = item.TargetKind
			converted.TargetID = item.TargetID
		default:
			return nil, huma.Error422UnprocessableEntity(
				"items[" + strconv.Itoa(i) + "]: target_kind must be person or contact_method")
		}

		out = append(out, converted)
	}
	return out, nil
}

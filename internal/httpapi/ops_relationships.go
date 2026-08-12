package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/audit"
	"github.com/bcars/bcars-portal/internal/db"
	"github.com/bcars/bcars-portal/internal/domain/relationships"
)

// Officer-maintained informational relationships (bcars-portal-4ux.8).
//
// Every operation here requires relationship.manage, and relationship.manage
// buys exactly one thing: the ability to write down that two people are
// connected. It confers no record access, no review authority, and no directory
// visibility, and holding it says nothing about whose records the officer may
// read — those come from their own capabilities and grants.
//
// There is deliberately no member-facing relationship surface. A member cannot
// list their household here, because the list would answer "does the club have
// a record for this person" for anyone the member cared to name.

// --- Response types ---

type RelationshipResponse struct {
	ID           int64  `json:"id"`
	FromPersonID int64  `json:"from_person_id"`
	ToPersonID   int64  `json:"to_person_id"`
	Kind         string `json:"kind" enum:"spouse_partner,parent_guardian,child_dependent,household,other"`

	// Context is restricted to officers holding relationship.manage. It is
	// never included in the directory or the member profile.
	Context string `json:"context,omitempty" doc:"Restricted officer note. Never returned by member-facing surfaces."`

	Active bool `json:"active"`

	CreatedByUserID int64  `json:"created_by_user_id,omitempty"`
	CreatedAt       string `json:"created_at" format:"date-time"`
	UpdatedAt       string `json:"updated_at,omitempty" format:"date-time"`

	ArchivedByUserID int64  `json:"archived_by_user_id,omitempty"`
	ArchivedAt       string `json:"archived_at,omitempty" format:"date-time"`
	ArchiveReason    string `json:"archive_reason,omitempty"`

	Version int64 `json:"version"`

	// Direction is relative to the person the listing was asked about, and is
	// empty for the single-relationship reads where there is no such subject.
	Direction        string `json:"direction,omitempty" enum:"outgoing,incoming"`
	OtherPersonID    int64  `json:"other_person_id,omitempty"`
	OtherDisplayName string `json:"other_display_name,omitempty"`
	OtherCallSign    string `json:"other_call_sign,omitempty"`
}

// --- Inputs and outputs ---

type CreateRelationshipBody struct {
	FromPersonID int64  `json:"from_person_id" minimum:"1"`
	ToPersonID   int64  `json:"to_person_id" minimum:"1"`
	Kind         string `json:"kind" enum:"spouse_partner,parent_guardian,child_dependent,household,other"`
	Context      string `json:"context,omitempty" maxLength:"1000" doc:"Restricted note explaining the link, for officers only."`
}

type CreateRelationshipInput struct {
	Body CreateRelationshipBody
}

type CreateRelationshipOutput struct {
	Body RelationshipResponse
}

type GetRelationshipInput struct {
	RelationshipID int64 `path:"relationship_id"`
}

type GetRelationshipOutput struct {
	Body RelationshipResponse
}

type ListPersonRelationshipsInput struct {
	PersonID        int64 `path:"person_id"`
	IncludeArchived bool  `query:"include_archived" doc:"Include archived relationships, so a former household stays answerable."`
}

type ListPersonRelationshipsOutput struct {
	Body []RelationshipResponse
}

type UpdateRelationshipBody struct {
	Kind    string `json:"kind" enum:"spouse_partner,parent_guardian,child_dependent,household,other"`
	Context string `json:"context,omitempty" maxLength:"1000"`
}

type UpdateRelationshipInput struct {
	RelationshipID int64  `path:"relationship_id"`
	IfMatch        string `header:"If-Match" doc:"Relationship version you last read. Required: a missing header is a 428."`
	Body           UpdateRelationshipBody
}

type UpdateRelationshipOutput struct {
	Body RelationshipResponse
}

type ArchiveRelationshipBody struct {
	Reason string `json:"reason,omitempty" maxLength:"1000"`
}

type ArchiveRelationshipInput struct {
	RelationshipID int64  `path:"relationship_id"`
	IfMatch        string `header:"If-Match" doc:"Relationship version you last read. Required: a missing header is a 428."`
	Body           ArchiveRelationshipBody
}

type ArchiveRelationshipOutput struct {
	Body RelationshipResponse
}

// RegisterRelationships registers officer relationship maintenance.
func RegisterRelationships(api huma.API, deps Deps) {
	var svc *relationships.Service
	if deps.DB != nil {
		svc = relationships.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "relationship-create",
		Method:      http.MethodPost,
		Path:        "/relationships",
		Summary:     "Record an informational relationship between two people",
		Description: "Informational only. Recording a relationship grants no record access, no " +
			"correction-management authority, and no directory visibility; an officer grants " +
			"access separately and revocably.",
		Tags: []string{"relationships"},
	}, OperationMeta{
		RequiredCapability: "relationship.manage",
		AuditAction:        "relationship.create",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *CreateRelationshipInput) (*CreateRelationshipOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		rel, err := svc.Create(ctx, principal, relationships.CreateParams{
			FromPersonID: input.Body.FromPersonID,
			ToPersonID:   input.Body.ToPersonID,
			Kind:         input.Body.Kind,
			Context:      input.Body.Context,
		})
		if err != nil {
			return nil, mapRelationshipError(err)
		}
		audit.StampResource(ctx, "relationship", rel.ID)
		return &CreateRelationshipOutput{Body: relationshipToResponse(rel)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "relationship-get",
		Method:      http.MethodGet,
		Path:        "/relationships/{relationship_id}",
		Summary:     "Read one relationship",
		Tags:        []string{"relationships"},
	}, OperationMeta{
		RequiredCapability: "relationship.manage",
		AuditAction:        "relationship.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *GetRelationshipInput) (*GetRelationshipOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		rel, err := svc.Get(ctx, principal, input.RelationshipID)
		if err != nil {
			return nil, mapRelationshipError(err)
		}
		return &GetRelationshipOutput{Body: relationshipToResponse(rel)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "person-relationships-list",
		Method:      http.MethodGet,
		Path:        "/members/{person_id}/relationships",
		Summary:     "List a person's relationships in both directions",
		Description: "Current relationships by default; include_archived adds former ones, because " +
			"a household that has since changed is still the household a past request arrived from.",
		Tags: []string{"relationships"},
	}, OperationMeta{
		RequiredCapability: "relationship.manage",
		AuditAction:        "relationship.list",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ListPersonRelationshipsInput) (*ListPersonRelationshipsOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		var rels []relationships.Relationship
		if input.IncludeArchived {
			rels, err = svc.ListHistoryForPerson(ctx, principal, input.PersonID)
		} else {
			rels, err = svc.ListForPerson(ctx, principal, input.PersonID)
		}
		if err != nil {
			return nil, mapRelationshipError(err)
		}
		return &ListPersonRelationshipsOutput{Body: relationshipsToResponse(rels)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "relationship-update",
		Method:      http.MethodPatch,
		Path:        "/relationships/{relationship_id}",
		Summary:     "Correct a relationship's kind or restricted note",
		Description: "The two people are not editable. Re-pointing one end would rewrite history " +
			"under one row; archive the relationship and record the new one instead.",
		Tags: []string{"relationships"},
	}, OperationMeta{
		RequiredCapability: "relationship.manage",
		AuditAction:        "relationship.update",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *UpdateRelationshipInput) (*UpdateRelationshipOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		version, err := requireIfMatch(input.IfMatch)
		if err != nil {
			return nil, err
		}
		rel, err := svc.Update(ctx, principal, input.RelationshipID, relationships.UpdateParams{
			Kind:            input.Body.Kind,
			Context:         input.Body.Context,
			ExpectedVersion: version,
		})
		if err != nil {
			return nil, mapRelationshipError(err)
		}
		audit.StampResource(ctx, "relationship", rel.ID)
		return &UpdateRelationshipOutput{Body: relationshipToResponse(rel)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "relationship-archive",
		Method:      http.MethodPost,
		Path:        "/relationships/{relationship_id}/archive",
		Summary:     "Archive a relationship that is no longer current",
		Description: "There is no delete. A relationship that stopped being true is still a fact " +
			"about the time it covered.",
		Tags: []string{"relationships"},
	}, OperationMeta{
		RequiredCapability: "relationship.manage",
		AuditAction:        "relationship.archive",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *ArchiveRelationshipInput) (*ArchiveRelationshipOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		version, err := requireIfMatch(input.IfMatch)
		if err != nil {
			return nil, err
		}
		rel, err := svc.Archive(ctx, principal, input.RelationshipID, relationships.ArchiveParams{
			Reason:          input.Body.Reason,
			ExpectedVersion: version,
		}, time.Now())
		if err != nil {
			return nil, mapRelationshipError(err)
		}
		audit.StampResource(ctx, "relationship", rel.ID)
		return &ArchiveRelationshipOutput{Body: relationshipToResponse(rel)}, nil
	})
}

func mapRelationshipError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, relationships.ErrNotFound):
		return huma.Error404NotFound("relationship not found")
	case errors.Is(err, relationships.ErrDuplicate):
		return huma.Error409Conflict("that relationship is already recorded")
	case errors.Is(err, relationships.ErrArchived):
		return huma.Error409Conflict("that relationship is already archived")
	case errors.Is(err, db.ErrStale):
		return huma.Error412PreconditionFailed("that relationship changed since you read it; re-read and retry")
	case errors.Is(err, relationships.ErrUnknownKind),
		errors.Is(err, relationships.ErrUnknownPerson),
		errors.Is(err, relationships.ErrSelfRelationship),
		errors.Is(err, relationships.ErrContextTooLong):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return huma.Error500InternalServerError("relationship operation failed")
}

func relationshipsToResponse(in []relationships.Relationship) []RelationshipResponse {
	out := make([]RelationshipResponse, 0, len(in))
	for _, r := range in {
		out = append(out, relationshipToResponse(r))
	}
	return out
}

func relationshipToResponse(r relationships.Relationship) RelationshipResponse {
	return RelationshipResponse{
		ID:               r.ID,
		FromPersonID:     r.FromPersonID,
		ToPersonID:       r.ToPersonID,
		Kind:             r.Kind,
		Context:          r.Context,
		Active:           r.Active(),
		CreatedByUserID:  r.CreatedBy,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		ArchivedByUserID: r.ArchivedBy,
		ArchivedAt:       r.ArchivedAt,
		ArchiveReason:    r.ArchiveReason,
		Version:          r.Version,
		Direction:        r.Direction,
		OtherPersonID:    r.OtherPersonID,
		OtherDisplayName: r.OtherDisplayName,
		OtherCallSign:    r.OtherCallSign,
	}
}

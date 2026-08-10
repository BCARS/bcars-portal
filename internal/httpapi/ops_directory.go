package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/bcars/bcars-portal/internal/domain/directory"
)

// The private member directory (bcars-portal-4ux.7).
//
// Two guards, not one. The capability middleware checks directory.read, which
// every member holds; the service then checks eligibility, which only an active
// approved Full member has. A caller who fails the second gets the same answer
// a caller who asks for a nonexistent resource gets, so an Associate learns
// nothing about who can read what.

// DirectoryEntry is one row as served.
//
// There is no postal address, dues standing, note, or administrative field
// here, and none is selected by the query behind it.
type DirectoryEntry struct {
	PersonID    int64  `json:"person_id"`
	DisplayName string `json:"display_name"`
	CallSign    string `json:"call_sign,omitempty"`
	BaseType    string `json:"base_type" enum:"full,associate"`

	Email string `json:"email,omitempty" doc:"Present only when this member shares it with Full members."`
	Phone string `json:"phone,omitempty" doc:"Present only when this member shares it with Full members."`

	// EmailShared and PhoneShared let a UI render "Not shared" without
	// inferring anything from an empty string. Both are false for a withheld
	// value AND for one that was never recorded, which is the point: the two
	// are indistinguishable to a caller.
	EmailShared bool `json:"email_shared"`
	PhoneShared bool `json:"phone_shared"`
}

type DirectoryListInput struct {
	Search string `query:"search" maxLength:"100" doc:"Matches display name or call sign."`
	Limit  int64  `query:"limit" minimum:"1" maximum:"1000" doc:"Defaults to 50, or the whole roster when print=true."`
	Offset int64  `query:"offset" minimum:"0"`
	Print  bool   `query:"print" doc:"Raise the page bound so a club-sized roster prints as one sheet. The same filtering applies."`
}

type DirectoryListBody struct {
	Entries []DirectoryEntry `json:"entries"`
	Total   int64            `json:"total" doc:"Every member in the directory, not every member whose details you can see."`
	Limit   int64            `json:"limit"`
	Offset  int64            `json:"offset"`
}

type DirectoryListOutput struct {
	Body DirectoryListBody
}

// RegisterDirectory registers the member directory.
func RegisterDirectory(api huma.API, deps Deps) {
	var svc *directory.Service
	if deps.DB != nil {
		svc = directory.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "directory-list",
		Method:      http.MethodGet,
		Path:        "/directory",
		Summary:     "Browse the private member directory",
		Description: "Requires an active approved Full membership, which the directory.read " +
			"capability alone does not confer. Contact values are filtered per member before " +
			"they leave the database.",
		Tags: []string{"directory"},
	}, OperationMeta{
		RequiredCapability: "directory.read",
		AuditAction:        "directory.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "never",
	}, func(ctx context.Context, input *DirectoryListInput) (*DirectoryListOutput, error) {
		if svc == nil {
			return nil, ErrNotImplemented()
		}
		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		page, err := svc.List(ctx, principal, directory.Query{
			Search: input.Search,
			Limit:  input.Limit,
			Offset: input.Offset,
			Print:  input.Print,
		})
		if err != nil {
			return nil, mapDirectoryError(err)
		}

		entries := make([]DirectoryEntry, 0, len(page.Entries))
		for _, e := range page.Entries {
			entries = append(entries, DirectoryEntry{
				PersonID:    e.PersonID,
				DisplayName: e.DisplayName,
				CallSign:    e.CallSign,
				BaseType:    e.BaseType,
				Email:       e.Email,
				Phone:       e.Phone,
				EmailShared: e.EmailShared(),
				PhoneShared: e.PhoneShared(),
			})
		}

		return &DirectoryListOutput{Body: DirectoryListBody{
			Entries: entries,
			Total:   page.Total,
			Limit:   page.Limit,
			Offset:  page.Offset,
		}}, nil
	})
}

// mapDirectoryError translates domain errors to HTTP.
//
// Ineligibility is a 404, not a 403. A 403 would confirm the directory exists
// and that someone else may read it; a 404 says only that this caller has
// nothing here, which is the same thing an unknown path says.
func mapDirectoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, directory.ErrNotEligible):
		return huma.Error404NotFound("not found")
	}
	return huma.Error500InternalServerError("directory read failed")
}

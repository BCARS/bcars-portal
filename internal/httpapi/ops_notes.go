package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
	"github.com/bcars/bcars-portal/internal/domain/members"
)

// --- Note types ---

type Note struct {
	ID          int64  `json:"id"`
	SubjectKind string `json:"subject_kind" enum:"person,membership"`
	SubjectID   int64  `json:"subject_id"`
	Category    string `json:"category"`
	Visibility  string `json:"visibility"`
	Body        string `json:"body"`
	AuthorID    int64  `json:"author_id"`
	Source      string `json:"source"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at" format:"date-time"`
	UpdatedAt   string `json:"updated_at" format:"date-time"`
}

type NotesListInput struct {
	PageQuery
	SubjectKind string `query:"subject_kind" required:"true" doc:"Filter by subject_kind (person, membership)."`
	SubjectID   int64  `query:"subject_id"   required:"true" doc:"Filter by subject_id."`
}
type NotesListOutput struct {
	Body Page[Note]
}

type CreateNoteBody struct {
	SubjectKind string `json:"subject_kind" enum:"person,membership"`
	SubjectID   int64  `json:"subject_id"`
	Category    string `json:"category" enum:"general,approval,honorary,coverage"`
	Visibility  string `json:"visibility" enum:"officer,treasurer,system"`
	Body        string `json:"body" minLength:"1"`
}
type CreateNoteInput struct{ Body CreateNoteBody }
type CreateNoteOutput struct {
	Body Note
}

type UpdateNoteBody struct {
	Body       string `json:"body" minLength:"1"`
	EditReason string `json:"edit_reason,omitempty"`
	Version    int64  `json:"version"`
}
type UpdateNoteInput struct {
	ID   int64 `path:"id"`
	Body UpdateNoteBody
}
type UpdateNoteOutput struct {
	Body Note
}

func noteToResponse(n sqlcgen.Note) Note {
	return Note{
		ID:          n.ID,
		SubjectKind: n.SubjectKind,
		SubjectID:   n.SubjectID,
		Category:    n.Category,
		Visibility:  n.Visibility,
		Body:        n.Body,
		AuthorID:    n.AuthorID,
		Source:      n.Source,
		Version:     n.Version,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}
}

// RegisterNotes registers note endpoints.
func RegisterNotes(api huma.API, deps Deps) {
	var memberSvc *members.Service
	if deps.DB != nil {
		memberSvc = members.NewService(deps.DB)
	}

	Register(api, huma.Operation{
		OperationID: "notes-list",
		Method:      http.MethodGet,
		Path:        "/notes",
		Summary:     "List notes for a subject (filtered by caller visibility)",
		Tags:        []string{"notes"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *NotesListInput) (*NotesListOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		limit := int64(input.Limit)
		if limit <= 0 {
			limit = 50
		}

		notes, err := memberSvc.ListNotes(ctx, principal,
			input.SubjectKind, input.SubjectID, limit, 0)
		if err != nil {
			return nil, mapDomainError(err)
		}

		data := make([]Note, len(notes))
		for i, n := range notes {
			data[i] = noteToResponse(n)
		}

		return &NotesListOutput{
			Body: Page[Note]{Data: data},
		}, nil
	})

	Register(api, huma.Operation{
		OperationID:   "note-create",
		Method:        http.MethodPost,
		Path:          "/notes",
		Summary:       "Create a note (officer or treasurer category)",
		Tags:          []string{"notes"},
		DefaultStatus: http.StatusCreated,
	}, OperationMeta{
		RequiredCapability: "notes.write.officer",
		AuditAction:        "note.create",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateNoteInput) (*CreateNoteOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		note, err := memberSvc.CreateNote(ctx, principal, members.CreateNoteParams{
			SubjectKind: input.Body.SubjectKind,
			SubjectID:   input.Body.SubjectID,
			Category:    input.Body.Category,
			Visibility:  input.Body.Visibility,
			Body:        input.Body.Body,
		})
		if err != nil {
			return nil, mapDomainError(err)
		}

		return &CreateNoteOutput{Body: noteToResponse(note)}, nil
	})

	Register(api, huma.Operation{
		OperationID: "note-update",
		Method:      http.MethodPatch,
		Path:        "/notes/{id}",
		Summary:     "Edit a note (writes a revision row)",
		Tags:        []string{"notes"},
	}, OperationMeta{
		RequiredCapability: "notes.write.officer",
		AuditAction:        "note.update",
		ConfirmationLevel:  ConfirmNone,
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *UpdateNoteInput) (*UpdateNoteOutput, error) {
		if memberSvc == nil {
			return nil, ErrNotImplemented()
		}

		principal, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}

		note, err := memberSvc.UpdateNote(ctx, principal,
			input.ID, input.Body.Version, input.Body.Body, input.Body.EditReason)
		if err != nil {
			return nil, mapDomainError(err)
		}

		return &UpdateNoteOutput{Body: noteToResponse(note)}, nil
	})
}

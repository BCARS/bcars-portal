package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// --- Note types ---

type Note struct {
	ID          int64  `json:"id"`
	SubjectKind string `json:"subject_kind" enum:"person,membership"`
	SubjectID   int64  `json:"subject_id"`
	Category    string `json:"category" enum:"officer,treasurer"`
	Body        string `json:"body"`
	CreatedByID int64  `json:"created_by_id"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at" format:"date-time"`
	UpdatedAt   string `json:"updated_at" format:"date-time"`
}

type NotesListInput struct {
	PageQuery
	SubjectKind string `query:"subject_kind" doc:"Filter by subject_kind (person, membership)."`
	SubjectID   int64  `query:"subject_id"   doc:"Filter by subject_id."`
}
type NotesListOutput struct {
	Body Page[Note]
}

type CreateNoteBody struct {
	SubjectKind string `json:"subject_kind" enum:"person,membership"`
	SubjectID   int64  `json:"subject_id"`
	Category    string `json:"category" enum:"officer,treasurer"`
	Body        string `json:"body" minLength:"1"`
}
type CreateNoteInput struct{ Body CreateNoteBody }
type CreateNoteOutput struct {
	Body Note
}

type UpdateNoteBody struct {
	Body    string `json:"body" minLength:"1"`
	Version int64  `json:"version"`
}
type UpdateNoteInput struct {
	ID   int64 `path:"id"`
	Body UpdateNoteBody
}
type UpdateNoteOutput struct {
	Body Note
}

// RegisterNotes registers note endpoints.
func RegisterNotes(api huma.API) {
	Register(api, huma.Operation{
		OperationID: "notes-list",
		Method:      http.MethodGet,
		Path:        "/notes",
		Summary:     "List notes for a subject (filtered by caller visibility)",
		Tags:        []string{"notes"},
	}, OperationMeta{
		RequiredCapability: "member.read",
		ConfirmationLevel:  "none",
		AIToolEligibility:  "read-only",
	}, func(ctx context.Context, input *NotesListInput) (*NotesListOutput, error) {
		return nil, ErrNotImplemented()
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
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *CreateNoteInput) (*CreateNoteOutput, error) {
		return nil, ErrNotImplemented()
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
		ConfirmationLevel:  "none",
		AIToolEligibility:  "curated",
	}, func(ctx context.Context, input *UpdateNoteInput) (*UpdateNoteOutput, error) {
		return nil, ErrNotImplemented()
	})
}

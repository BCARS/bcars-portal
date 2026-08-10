package changerequests

import (
	sqlcgen "github.com/bcars/bcars-portal/internal/db/sqlc"
)

// Mapping from generated rows to the domain shapes.
//
// Note what is NOT carried out of the database: source_ip_hash stays in the
// row. It exists so an officer reviewing a burst of public submissions can see
// they came from one source, and it is read by the abuse limiter — but it is
// not part of a request's description, so it never reaches a caller through
// this package.

func requestFrom(row sqlcgen.MemberChangeRequest) Request {
	return Request{
		ID:                 row.ID,
		Source:             row.Source,
		Status:             row.Status,
		RequesterUserID:    row.RequesterUserID.Int64,
		TargetPersonID:     row.TargetPersonID.Int64,
		SuppliedName:       row.SuppliedName.String,
		SuppliedCallSign:   row.SuppliedCallSign.String,
		SuppliedContact:    row.SuppliedContact.String,
		StatedRelationship: row.StatedRelationship.String,
		Summary:            row.Summary,
		ReceivedBy:         row.ReceivedBy.Int64,
		SubmittedAt:        row.SubmittedAt,
		TriagedBy:          row.TriagedBy.Int64,
		TriagedAt:          row.TriagedAt.String,
		ResolvedAt:         row.ResolvedAt.String,
		WithdrawnAt:        row.WithdrawnAt.String,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		Version:            row.Version,
	}
}

func requestFromListRow(row sqlcgen.ListChangeRequestsRow) Request {
	r := requestFrom(sqlcgen.MemberChangeRequest{
		ID:                 row.ID,
		Source:             row.Source,
		Status:             row.Status,
		RequesterUserID:    row.RequesterUserID,
		TargetPersonID:     row.TargetPersonID,
		SuppliedName:       row.SuppliedName,
		SuppliedCallSign:   row.SuppliedCallSign,
		SuppliedContact:    row.SuppliedContact,
		StatedRelationship: row.StatedRelationship,
		Summary:            row.Summary,
		ReceivedBy:         row.ReceivedBy,
		SubmittedAt:        row.SubmittedAt,
		TriagedBy:          row.TriagedBy,
		TriagedAt:          row.TriagedAt,
		ResolvedAt:         row.ResolvedAt,
		WithdrawnAt:        row.WithdrawnAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		Version:            row.Version,
	})
	r.TargetDisplayName = row.TargetDisplayName.String
	return r
}

func itemsFrom(rows []sqlcgen.MemberChangeRequestItem) []Item {
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, Item{
			ID:                     row.ID,
			RequestID:              row.RequestID,
			Ordinal:                row.Ordinal,
			Operation:              row.Operation,
			ProposedValue:          row.ProposedValue.String,
			TargetKind:             row.TargetKind.String,
			TargetID:               row.TargetID.Int64,
			TargetVersion:          row.TargetVersion.Int64,
			Sensitivity:            row.Sensitivity,
			Status:                 row.Status,
			ReviewedBy:             row.ReviewedBy.Int64,
			ReviewedAt:             row.ReviewedAt.String,
			DecisionReason:         row.DecisionReason.String,
			VerificationNote:       row.VerificationNote.String,
			AppliedAt:              row.AppliedAt.String,
			AppliedResourceKind:    row.AppliedResourceKind.String,
			AppliedResourceID:      row.AppliedResourceID.Int64,
			AppliedResourceVersion: row.AppliedResourceVersion.Int64,
			Version:                row.Version,
		})
	}
	return out
}

// nullableFilter converts an optional string filter into the interface{} shape
// sqlc emits for sqlc.narg on a comparison it cannot type.
func nullableFilter(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullableIDFilter is the same for an optional id filter.
func nullableIDFilter(id int64) interface{} {
	if id == 0 {
		return nil
	}
	return id
}

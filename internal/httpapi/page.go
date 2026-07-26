package httpapi

import (
	"encoding/base64"
	"fmt"
)

// PageQuery holds the common pagination query parameters understood by list
// endpoints. Embed this in an endpoint's input struct.
//
//	type MembersListInput struct {
//	    PageQuery
//	    // domain-specific filters ...
//	}
type PageQuery struct {
	Limit  int    `query:"limit"  default:"50"  minimum:"1" maximum:"200" doc:"Maximum number of items to return."`
	Cursor string `query:"cursor" doc:"Opaque pagination cursor returned by a previous list call."`
}

// Page is the standard paginated response envelope. It is the body of every
// list response.
//
//	type MembersListOutput struct {
//	    Body Page[MemberSummary]
//	}
type Page[T any] struct {
	Data []T `json:"data" doc:"Slice of results for this page."`
	// NextCursor is omitted when there are no more pages.
	NextCursor string `json:"next_cursor,omitempty" doc:"Pass as cursor= to fetch the next page."`
}

// EncodeCursor encodes a raw cursor value to the opaque base64 form returned
// to clients. Use this to turn a last-seen row key (e.g. an ID string) into a
// cursor. The encoding is not a security boundary; it just discourages clients
// from constructing cursors by hand.
func EncodeCursor(rawValue string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(rawValue))
}

// DecodeCursor decodes an opaque cursor back to the raw value that was encoded
// by EncodeCursor. Returns an error when the input is not valid base64 URL
// encoding, which the caller should surface as a 400 validation error.
func DecodeCursor(cursor string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("invalid cursor: %w", err)
	}
	return string(b), nil
}

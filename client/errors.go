package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Violation describes a single validation error returned by the API.
type Violation struct {
	PropertyPath string `json:"propertyPath,omitempty"`
	Message      string `json:"message,omitempty"`
	Code         string `json:"code,omitempty"`
}

// APIError is returned when the API responds with a non-2xx status code.
//
// The Order API returns RFC 7807 style problem documents, the REST API
// returns a slightly different format; both are decoded on a best-effort
// basis. The raw body is always available in Body.
type APIError struct {
	StatusCode int         `json:"-"`
	Type       string      `json:"type,omitempty"`
	Title      string      `json:"title,omitempty"`
	Detail     string      `json:"detail,omitempty"`
	Code       string      `json:"code,omitempty"`
	Violations []Violation `json:"violations,omitempty"`
	Body       []byte      `json:"-"`
}

func newAPIError(status int, body []byte) *APIError {
	var e = &APIError{StatusCode: status, Body: body}
	_ = json.Unmarshal(body, e)

	// REST API style: {"errors": {"general": {"code": "...", "message": "..."}}}
	if e.Title == "" && e.Detail == "" {
		var alt struct {
			Message string `json:"message"`
			Errors  map[string]struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &alt) == nil {
			if alt.Message != "" {
				e.Detail = alt.Message
			}
			for path, v := range alt.Errors {
				e.Violations = append(e.Violations, Violation{
					PropertyPath: path, Message: v.Message, Code: v.Code,
				})
			}
		}
	}

	return e
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "paynl: api error %d", e.StatusCode)

	switch {
	case e.Title != "" && e.Detail != "":
		fmt.Fprintf(&b, ": %s: %s", e.Title, e.Detail)
	case e.Title != "":
		fmt.Fprintf(&b, ": %s", e.Title)
	case e.Detail != "":
		fmt.Fprintf(&b, ": %s", e.Detail)
	default:
		fmt.Fprintf(&b, ": %s", http.StatusText(e.StatusCode))
	}

	for _, v := range e.Violations {
		fmt.Fprintf(&b, "; %s: %s", v.PropertyPath, v.Message)
	}

	return b.String()
}

// IsNotFound reports whether err is an API error with status 404.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

// IsUnauthorized reports whether err is an API error with status 401 or 403.
func IsUnauthorized(err error) bool {
	var e *APIError
	return errors.As(err, &e) && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}

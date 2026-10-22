package work

import (
	"errors"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/GoYoko/web"
	"net/http"
)

// Only explicit validation messages are public. Database/transport internals
// keep the platform's generic error response. Never mutate shared Err values.
type skillValidationError struct {
	detail  string
	binding bool
}

func (e *skillValidationError) Error() string { return e.detail }
func (e *skillValidationError) Unwrap() error { return errcode.ErrBadRequest }
func skillInputError(err error) error         { return &skillValidationError{detail: err.Error()} }
func skillHandlerError(c *web.Context, err error) error {
	var input *skillValidationError
	if errors.As(err, &input) {
		return c.JSON(http.StatusBadRequest, map[string]any{"code": 400, "message": input.detail, "data": map[string]bool{"requires_binding": input.binding}})
	}
	return err
}

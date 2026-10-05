package v1

import (
	"net/http"

	"github.com/GoYoko/web"
	"github.com/63747756/jingjiaagent/backend/domain"
)

// Check only after signature verification, before host selection or Redis
// deduplication. A deferred review must not be acknowledged as accepted work.
func ensureGitReviewAvailable(c *web.Context, usecase domain.GitTaskUsecase) (bool, error) {
	if availability, ok := usecase.(domain.GitTaskAvailability); ok {
		if err := availability.CheckAdmission(c.Request().Context()); err != nil {
			return false, c.String(http.StatusServiceUnavailable, "automatic PR/MR review is unavailable for the selected runtime")
		}
	}
	return true, nil
}

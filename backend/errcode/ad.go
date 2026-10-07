package errcode

import (
	"net/http"
	"github.com/GoYoko/web"
)

var (
	ErrADConfigInvalid = web.NewErr(http.StatusBadRequest, 10640, "err-ad-config-invalid")
	ErrADUnavailable = web.NewErr(http.StatusServiceUnavailable, 10641, "err-ad-unavailable")
	ErrADInvalidCredentials = web.NewErr(http.StatusUnauthorized, 10642, "err-ad-invalid-credentials")
	ErrADDisabled = web.NewErr(http.StatusForbidden, 10643, "err-ad-disabled")
	ErrADModeConflict = web.NewErr(http.StatusConflict, 10644, "err-ad-mode-conflict")
	ErrADConfigConflict = web.NewErr(http.StatusConflict, 10645, "err-ad-config-conflict")
	ErrADManaged = web.NewErr(http.StatusForbidden, 10646, "err-ad-managed")
	ErrADRateLimited = web.NewErr(http.StatusTooManyRequests, 10647, "err-ad-rate-limited")
	ErrADLocalPasswordDenied = web.NewErr(http.StatusForbidden, 10648, "err-ad-local-password-denied")
)

package v1

import (
	"context"
	"crypto/subtle"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/GoYoko/web"
)

func (h *TeamGroupUserHandler) DeploymentStatus(c *web.Context) error {
	if !h.config.Deployment.Enabled {
		return c.Success(map[string]bool{"enabled": false, "initialized": true})
	}
	n, err := h.deploymentDB.Team.Query().Count(c.Request().Context())
	if err != nil {
		return errcode.ErrInternalServer
	}
	return c.Success(map[string]bool{"enabled": true, "initialized": n > 0})
}

type deploymentSetup struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *TeamGroupUserHandler) SetupDeployment(c *web.Context, r deploymentSetup) error {
	if !h.config.Deployment.Enabled {
		return errcode.ErrForbidden
	}
	origin := c.Request().Header.Get("Origin")
	if origin != "" && !sameDeploymentOrigin(origin, h.config.Server.BaseURL) {
		return errcode.ErrForbidden
	}
	token, err := os.ReadFile(h.config.Deployment.SetupTokenFile)
	if err != nil || len(token) != 64 || len(r.Token) != 64 || subtle.ConstantTimeCompare(token, []byte(r.Token)) != 1 {
		return errcode.ErrForbidden
	}
	r.Email = strings.TrimSpace(r.Email)
	r.Name = strings.TrimSpace(r.Name)
	address, err := mail.ParseAddress(r.Email)
	if err != nil || address.Address != r.Email || len(r.Email) > 254 || r.Name == "" || utf8.RuneCountInString(r.Name) > 60 || len(r.Password) < 12 || len(r.Password) > 32 {
		return errcode.ErrBadRequest
	}
	initializer, ok := h.repo.(interface {
		InitDeployment(context.Context, string, string, string, string) (*domain.InitTeamResult, error)
	})
	if !ok {
		return errcode.ErrInternalServer
	}
	// The transaction locks the singleton before testing for existing teams. A
	// replay or concurrent request can never reset an existing administrator.
	result, err := initializer.InitDeployment(c.Request().Context(), r.Email, r.Name, r.Password, h.config.Deployment.GuestImage)
	if err != nil {
		return err
	}
	return c.Success(result)
}

// Compare URL origins, not their spelling: browsers omit default ports and
// lowercase hostnames. Keep the scheme and nondefault port in the boundary.
func sameDeploymentOrigin(requestOrigin, configuredURL string) bool {
	parse := func(raw string) ([3]string, bool) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") ||
			(u.Path != "" && u.Path != "/") || strings.HasSuffix(u.Host, ":") {
			return [3]string{}, false
		}
		host := strings.ToLower(u.Hostname())
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return [3]string{}, false
		}
		return [3]string{u.Scheme, host, strconv.Itoa(n)}, true
	}
	a, validA := parse(requestOrigin)
	b, validB := parse(configuredURL)
	return validA && validB && a == b
}

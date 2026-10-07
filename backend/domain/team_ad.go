package domain

import (
	"context"
	"github.com/google/uuid"
)

type TeamADUsecase interface {
	GetConfig(context.Context, *TeamUser) (*TeamADConfigResp, error)
	SaveConfig(context.Context, *TeamUser, *SaveTeamADConfigReq) (*TeamADConfigResp, error)
	TestConfig(context.Context, *TeamUser, *SaveTeamADConfigReq) (*TeamADTestResp, error)
	PublicConfig(context.Context) (*AuthConfigResp, error)
	Login(context.Context, *ADLoginReq, string) (*User, error)
	RequireNonADLogin(context.Context) error
}

type AuthConfigResp struct {
	Mode          string `json:"mode"`
	DisplayName   string `json:"display_name"`
	AccountFormat string `json:"account_format"`
	SessionDays   int    `json:"session_days"`
}

type ADLoginReq struct {
	Account      string `json:"account" validate:"required"`
	Password     string `json:"password" validate:"required"`
	CaptchaToken string `json:"captcha_token"`
}

type SaveTeamADConfigReq struct {
	Enabled         bool     `json:"enabled"`
	DisplayName     string   `json:"display_name"`
	URL             string   `json:"url"`
	BaseDN          string   `json:"base_dn"`
	BindDN          string   `json:"bind_dn"`
	BindPassword    string   `json:"bind_password"`
	CAPEM           string   `json:"ca_pem"`
	AllowedGroupDNs []string `json:"allowed_group_dns"`
	Revision        int      `json:"revision"`
}

type TeamADConfig struct {
	DirectoryID     uuid.UUID `json:"directory_id"`
	TeamID          uuid.UUID `json:"team_id"`
	Enabled         bool      `json:"enabled"`
	DisplayName     string    `json:"display_name"`
	URL             string    `json:"url"`
	BaseDN          string    `json:"base_dn"`
	BindDN          string    `json:"bind_dn"`
	CAPEM           string    `json:"ca_pem"`
	AllowedGroupDNs []string  `json:"allowed_group_dns"`
	HasBindPassword bool      `json:"has_bind_password"`
	Revision        int       `json:"revision"`
}
type TeamADConfigResp struct {
	Config *TeamADConfig `json:"config"`
}
type TeamADTestResp struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

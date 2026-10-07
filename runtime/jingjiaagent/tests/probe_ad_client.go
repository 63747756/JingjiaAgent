package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/63747756/jingjiaagent/backend/pkg/adldap"
	"os"
)

func main() {
	var config struct {
		URL          string   `json:"url"`
		BaseDN       string   `json:"base_dn"`
		BindDN       string   `json:"bind_dn"`
		BindPassword string   `json:"bind_password"`
		CAPEM        string   `json:"ca_pem"`
		Allowed      []string `json:"allowed_group_dns"`
	}
	var users map[string]struct {
		Password string `json:"password"`
	}
	data, err := os.ReadFile(os.Args[1] + "/config.json")
	if err != nil {
		panic("fixture config unavailable")
	}
	if json.Unmarshal(data, &config) != nil {
		panic("invalid fixture config")
	}
	data, err = os.ReadFile(os.Args[1] + "/users.json")
	if err != nil {
		panic("fixture users unavailable")
	}
	if json.Unmarshal(data, &users) != nil {
		panic("invalid fixture users")
	}
	cfg := adldap.Config{URL: "ldaps://127.0.0.1:1636", BaseDN: config.BaseDN, BindDN: config.BindDN, BindPassword: config.BindPassword, CAPEM: config.CAPEM, AllowedGroupDNs: config.Allowed}
	client := adldap.New()
	checks := map[string]bool{"connection": client.Test(context.Background(), cfg) == nil}
	for _, name := range []string{"alice", "bob", "charlie", "escaped", "noou", "noemail"} {
		profile, err := client.Authenticate(context.Background(), cfg, name, users[name].Password)
		checks[name] = err == nil && profile != nil && profile.Username == name
		if name == "noemail" {
			checks["empty_mail"] = profile != nil && profile.Email == ""
		}
		if name == "noou" {
			checks["unassigned"] = profile != nil && profile.Department.GUID == "unassigned"
		}
		if name == "escaped" {
			checks["escaped_path"] = profile != nil && profile.Department.Path == "公司／研发,工具组"
		}
	}
	for _, name := range []string{"disabled", "outsider", "locked"} {
		_, err := client.Authenticate(context.Background(), cfg, name, users[name].Password)
		checks[name] = errors.Is(err, adldap.ErrInvalidCredentials)
	}
	_, err = client.Authenticate(context.Background(), cfg, "alice", "wrong")
	checks["wrong_password"] = errors.Is(err, adldap.ErrInvalidCredentials)
	result, _ := json.Marshal(checks)
	fmt.Println(string(result))
	for _, ok := range checks {
		if !ok {
			os.Exit(1)
		}
	}
}

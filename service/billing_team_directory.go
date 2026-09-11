package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// ResolveTeamInvitee only performs exact lookup. Provider credentials and the
// directory endpoint are server configuration, never browser input.
func ResolveTeamInvitee(ctx context.Context, username string) (model.TeamInvitee, error) {
	var result model.TeamInvitee
	if username == "" || strings.TrimSpace(username) != username || len(username) > 128 || strings.ContainsAny(username, "/\\\x00") {
		return result, model.ErrTeamInvalid
	}
	raw := os.Getenv("BILLING_TEAM_CASDOOR_PROVIDER_ID")
	if raw == "" {
		var user model.User
		err := model.DB.Where("username = ?", username).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, model.ErrTeamUnavailable
		}
		if err != nil {
			return result, err
		}
		if user.Username != username || user.Status != common.UserStatusEnabled {
			return result, model.ErrTeamUnavailable
		}
		return model.TeamInvitee{UserId: user.Id, Username: user.Username, DisplayName: user.DisplayName}, nil
	}
	id, err := strconv.Atoi(raw)
	if err != nil {
		return result, err
	}
	var provider model.CustomOAuthProvider
	if err = model.DB.First(&provider, id).Error; err != nil {
		return result, err
	}
	if !provider.Enabled || provider.UserIdField != "sub" || !strings.HasSuffix(provider.WellKnown, "/.well-known/openid-configuration") {
		return result, model.ErrTeamUnavailable
	}
	base := strings.TrimSuffix(provider.WellKnown, "/.well-known/openid-configuration")
	endpoint, err := url.Parse(base)
	if err != nil {
		return result, err
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost")) {
		return result, model.ErrTeamUnavailable
	}
	owner := os.Getenv("BILLING_TEAM_CASDOOR_ORGANIZATION")
	clientID, secret := os.Getenv("BILLING_TEAM_DIRECTORY_CLIENT_ID"), os.Getenv("BILLING_TEAM_DIRECTORY_CLIENT_SECRET")
	if owner == "" || clientID == "" || secret == "" {
		return result, errors.New("directory is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/get-user?"+url.Values{"id": {owner + "/" + username}}.Encode(), nil)
	if err != nil {
		return result, err
	}
	req.SetBasicAuth(clientID, secret)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return result, errors.New("directory unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("directory unavailable")
	}
	var data struct {
		Status string `json:"status"`
		Data   *struct {
			Id          string `json:"id"`
			Owner       string `json:"owner"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			IsForbidden bool   `json:"isForbidden"`
			IsDeleted   bool   `json:"isDeleted"`
		} `json:"data"`
	}
	if err = common.DecodeJson(io.LimitReader(response.Body, 1<<20), &data); err != nil {
		return result, errors.New("invalid directory response")
	}
	if data.Status != "ok" || data.Data == nil || data.Data.Id == "" || len(data.Data.Id) > 256 || data.Data.Owner != owner || data.Data.Name != username || data.Data.IsForbidden || data.Data.IsDeleted {
		return result, model.ErrTeamUnavailable
	}
	result = model.TeamInvitee{ProviderId: id, Subject: data.Data.Id, Username: username, DisplayName: data.Data.DisplayName}
	var binding model.UserOAuthBinding
	err = model.DB.Where("provider_id = ? AND provider_user_id = ?", id, result.Subject).First(&binding).Error
	if err == nil {
		if binding.ProviderUserId != result.Subject {
			return model.TeamInvitee{}, model.ErrTeamUnavailable
		}
		result.UserId = binding.UserId
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.TeamInvitee{}, err
	}
	return result, nil
}

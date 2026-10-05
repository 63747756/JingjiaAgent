package v1

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoYoko/web"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/63747756/jingjiaagent/backend/domain"
)

type reviewWebhookBot struct{ domain.GitBotUsecase }

func (*reviewWebhookBot) GetByID(context.Context, uuid.UUID) (*domain.GitBot, error) {
	return &domain.GitBot{SecretToken: "webhook-fixture-secret"}, nil
}

type reviewWebhookAdmission struct {
	domain.GitTaskUsecase
	unavailable bool
	checked     int
}

func (u *reviewWebhookAdmission) CheckAdmission(context.Context) error {
	u.checked++
	if u.unavailable {
		return errors.New("private runtime diagnostic")
	}
	return nil
}

func (*reviewWebhookAdmission) Create(context.Context, domain.CreateGitTaskReq) (*domain.GitTask, error) {
	panic("deferred or empty webhook must not create a review")
}

func TestReviewWebhooksAuthenticateBeforeDeferredAdmission(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bot := &reviewWebhookBot{}
	for _, platform := range []string{"github", "gitlab", "gitee", "gitea", "codeup"} {
		for _, mode := range []string{"deferred", "invalid_signature", "legacy", "ping"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				admission := &reviewWebhookAdmission{unavailable: mode != "legacy"}
				var handler func(*web.Context) error
				request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
				mac := hmac.New(sha256.New, []byte("webhook-fixture-secret"))
				mac.Write([]byte("{}"))
				signature := hex.EncodeToString(mac.Sum(nil))
				if mode == "invalid_signature" {
					signature = "invalid"
				}
				token := "webhook-fixture-secret"
				if mode == "invalid_signature" {
					token = "invalid"
				}
				switch platform {
				case "github":
					handler = (&GithubWebhookHandler{logger: logger, gitbotUsecase: bot, gitTaskUsecase: admission}).Webhook
					request.Header.Set("X-Github-Event", "pull_request")
					request.Header.Set("X-Hub-Signature-256", "sha256="+signature)
				case "gitlab":
					handler = (&GitlabWebhookHandler{logger: logger, gitbotUsecase: bot, gitTaskUsecase: admission}).Webhook
					request.Header.Set("X-Gitlab-Event", "Merge Request Hook")
					request.Header.Set("X-Gitlab-Token", token)
				case "gitee":
					handler = (&GiteeWebhookHandler{logger: logger, gitbotUsecase: bot, gitTaskUsecase: admission}).Webhook
					request.Header.Set("X-Gitee-Event", "Merge Request Hook")
					request.Header.Set("X-Gitee-Token", token)
				case "gitea":
					handler = (&GiteaWebhookHandler{logger: logger, gitbotUsecase: bot, gitTaskUsecase: admission}).Webhook
					request.Header.Set("X-Gitea-Event", "pull_request")
					request.Header.Set("X-Gitea-Signature", signature)
				case "codeup":
					handler = (&CodeupWebhookHandler{logger: logger, gitbotUsecase: bot, gitTaskUsecase: admission}).Webhook
					request.Header.Set("X-Event-Type", "merge request")
					request.Header.Set("X-Codeup-Token", token)
				}
				if mode == "ping" {
					for _, name := range []string{"X-Github-Event", "X-Gitlab-Event", "X-Gitee-Event", "X-Gitea-Event", "X-Event-Type"} {
						request.Header.Set(name, "ping")
					}
				}
				recorder := httptest.NewRecorder()
				c := &web.Context{Context: echo.New().NewContext(request, recorder)}
				c.SetParamNames("id")
				c.SetParamValues(uuid.NewString())
				if err := handler(c); err != nil {
					t.Fatal(err)
				}
				want := http.StatusOK
				if mode == "deferred" {
					want = http.StatusServiceUnavailable
				}
				if mode == "invalid_signature" {
					want = http.StatusUnauthorized
				}
				if recorder.Code != want || strings.Contains(recorder.Body.String(), "private runtime diagnostic") {
					t.Fatalf("unexpected webhook response: %d", recorder.Code)
				}
				wantChecks := 1
				if mode == "invalid_signature" || mode == "ping" {
					wantChecks = 0
				}
				if admission.checked != wantChecks {
					t.Fatal("authentication/event filtering did not precede admission")
				}
			})
		}
	}
}

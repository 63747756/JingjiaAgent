package agentresource

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/oss"
	"github.com/samber/do"
)

func TestAgentDownloadsUseConfiguredSigningOrigin(t *testing.T) {
	for _, internal := range []string{"", "http://storage.internal:9000"} {
		t.Run(internal, func(t *testing.T) {
			cfg := &config.Config{ObjectStorage: config.ObjectStorageConfig{
				Enabled: true, ForcePathStyle: true, Endpoint: "http://backend-storage:9000",
				AccessEndpoint: "https://web.example.com/oss", AgentAccessEndpoint: internal,
				AccessKey: "test-key", AccessKeySecret: "test-secret", Bucket: "private-assets",
			}}
			i := do.New()
			do.ProvideValue(i, cfg)
			do.ProvideValue(i, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ProvideAgentResource(i)
			store := do.MustInvoke[ObjectStore](i)
			key := "temp/user-owned.txt"
			got, err := store.PresignGet(context.Background(), key, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			gotURL, _ := url.Parse(got)
			expectedOrigin := cfg.ObjectStorage.AccessEndpoint
			if internal != "" {
				expectedOrigin = internal
			}
			base, _ := url.Parse(expectedOrigin)
			if gotURL.Host != base.Host || gotURL.Path != base.Path+"/private-assets/"+key {
				t.Fatal("Agent download URL was not signed for its selected origin")
			}
			// Verify the signature at the actual recorded signing time, avoiding
			// wall-clock races between independently generated presigned URLs.
			q := gotURL.Query()
			signature := q.Get("X-Amz-Signature")
			signingTime, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
			if err != nil {
				t.Fatal(err)
			}
			q.Del("X-Amz-Signature")
			gotURL.RawQuery = q.Encode()
			gotURL.Path = strings.TrimPrefix(gotURL.Path, base.Path)
			req, err := http.NewRequest(http.MethodGet, gotURL.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			verified, _, err := v4.NewSigner().PresignHTTP(context.Background(), aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signingTime)
			if err != nil {
				t.Fatal(err)
			}
			verifiedURL, _ := url.Parse(verified)
			if signature == "" || signature != verifiedURL.Query().Get("X-Amz-Signature") {
				t.Fatal("download signature does not authenticate its selected host")
			}
			browser, err := oss.NewS3Compatible(context.Background(), cfg.ObjectStorage, oss.S3Option{ForcePathStyle: true})
			if err != nil {
				t.Fatal(err)
			}
			put, err := browser.Presign(context.Background(), "temp", "user-owned.txt", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(put.UploadURL, "https://web.example.com/oss/private-assets/") {
				t.Fatal("Browser upload origin changed")
			}
		})
	}
}

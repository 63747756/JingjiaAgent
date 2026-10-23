package pkg

import (
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/llm"
	"github.com/samber/do"
)

func TestRegisteredLLMAllowsSDKDefaultEndpoint(t *testing.T) {
	for _, address := range []string{"", "https://api.deepseek.com/v1", "http://model.internal:8000/v1", "file:///etc/passwd"} {
		t.Run(address, func(t *testing.T) {
			i := do.New()
			cfg := &config.Config{}
			cfg.LLM.APIKey, cfg.LLM.BaseURL = "test-key", address
			do.ProvideValue(i, cfg)
			if err := RegisterInfra(i); err != nil {
				t.Fatal(err)
			}
			_, err := do.Invoke[*llm.Client](i)
			if (err != nil) != (address == "file:///etc/passwd") {
				t.Fatalf("address %q: %v", address, err)
			}
		})
	}
}

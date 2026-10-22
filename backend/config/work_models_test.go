package config

import (
	"context"
	"testing"
)

func TestOfflineModelsRequireExplicitPrivateHostAndPort(t *testing.T) {
	for _, test := range []struct {
		url     string
		hosts   []string
		allowed bool
	}{{"http://10.1.2.3:8080/v1", nil, false}, {"http://10.1.2.3:8080/v1", []string{"10.1.2.3:8080"}, true}, {"http://10.1.2.3:8081/v1", []string{"10.1.2.3:8080"}, false}, {"https://93.184.216.34/v1", []string{"93.184.216.34:443"}, false}, {"http://user:secret@10.1.2.3:8080", []string{"10.1.2.3:8080"}, false}, {"https://[fd00::1]:443/v1", []string{"[fd00::1]:443"}, true}} {
		err := (Work{Enabled: true, Offline: true, AllowedModelHosts: test.hosts}).ValidateModelURL(context.Background(), test.url)
		if (err == nil) != test.allowed {
			t.Fatalf("allowed=%v wanted=%v", err == nil, test.allowed)
		}
	}
}

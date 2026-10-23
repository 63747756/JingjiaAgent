package config

import "testing"

func TestModelAddressesDoNotDependOnNetworkOrAllowlist(t *testing.T) {
	for _, address := range []string{"https://api.deepseek.com/v1", "http://10.1.2.3:8080/v1", "http://localhost:8000/v1", "https://[fd00::1]/v1", "https://93.184.216.34/v1", "http://model.internal:8000/v1"} {
		if err := ValidateModelURL(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"file:///etc/passwd", "https://user:secret@model.internal/v1", "ftp://model.internal", "", "https://model.internal/v1#fragment"} {
		if ValidateModelURL(address) == nil {
			t.Errorf("invalid address accepted: %s", address)
		}
	}
}

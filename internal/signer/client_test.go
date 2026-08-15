package signer

import (
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		tls      bool
		want     string
	}{
		{"http://sign:8090", true, "https://sign:8090"},
		{"https://sign:8090", true, "https://sign:8090"},
		{"https://sign:8090", false, "http://sign:8090"},
		{"http://127.0.0.1:8090", false, "http://127.0.0.1:8090"},
		{"", true, ""},
	}
	for _, c := range cases {
		if got := normalizeEndpoint(c.endpoint, c.tls); got != c.want {
			t.Fatalf("normalizeEndpoint(%q, %v) = %q, want %q", c.endpoint, c.tls, got, c.want)
		}
	}
}

func TestNewClientUpgradesSchemeWhenTLSEnabled(t *testing.T) {
	var cfg config.SignConfig
	cfg.Endpoint = "http://sign:8090/"
	cfg.TLS.Enabled = true
	cfg.TLS.ServerName = "sign.internal"

	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.endpoint != "https://sign:8090" {
		t.Fatalf("endpoint = %q", c.endpoint)
	}
}

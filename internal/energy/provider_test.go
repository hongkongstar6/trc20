package energy

import (
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestBuildOnlyEnabledProviders(t *testing.T) {
	Register("test_alpha", func(conf config.ProviderConf) (Provider, error) {
		return &fakeProvider{name: "test_alpha"}, nil
	})
	Register("test_beta", func(conf config.ProviderConf) (Provider, error) {
		return &fakeProvider{name: "test_beta"}, nil
	})
	provs, err := Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		"test_alpha": {Enabled: true},
		"test_beta":  {Enabled: false},
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(provs) != 1 || provs["test_alpha"] == nil {
		t.Fatalf("built %v, want only test_alpha", provs)
	}
}

func TestBuildRejectsUnknownProvider(t *testing.T) {
	_, err := Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		"does_not_exist": {Enabled: true},
	}})
	if err == nil {
		t.Fatal("an unknown provider name must fail loudly at startup")
	}
}

func TestBuildFailsWithoutAnyEnabledProvider(t *testing.T) {
	if _, err := Build(config.EnergyConfig{}); err == nil {
		t.Fatal("expected ErrNoProvider")
	}
}

func TestOptionFallsBackToDefault(t *testing.T) {
	conf := config.ProviderConf{Options: map[string]string{"set": "value", "empty": ""}}
	if got := Option(conf, "set", "def"); got != "value" {
		t.Fatalf("got %s", got)
	}
	if got := Option(conf, "empty", "def"); got != "def" {
		t.Fatalf("an empty option must fall back, got %s", got)
	}
	if got := Option(conf, "missing", "def"); got != "def" {
		t.Fatalf("got %s", got)
	}
}

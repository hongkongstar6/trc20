// Package bootstrap wires the shared dependencies every entrypoint needs, so
// each cmd/* main stays a thin composition root.
package bootstrap

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/logx"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"

	// Providers self register through their init functions; the registry keeps
	// the rest of the system free of provider specific imports.
	_ "github.com/hongkongstar6/trc20/internal/energy/gasstation"
	_ "github.com/hongkongstar6/trc20/internal/energy/tronenergyrent"
	_ "github.com/hongkongstar6/trc20/internal/energy/trxburn"
)

type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Store   *store.Store
	Gateway *chain.Gateway
}

// Init loads the config, opens the datastores and builds the chain gateway.
func Init(service string) (*App, error) {
	path := flag.String("config", envOr("CONFIG_PATH", "configs/config.yaml"), "path to the config file")
	migrate := flag.Bool("migrate", false, "run schema auto migration and exit")
	flag.Parse()

	cfg, err := config.Load(*path)
	if err != nil {
		return nil, err
	}
	log := logx.New(cfg.Log.Level, service)
	st, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	if *migrate {
		if err := st.AutoMigrate(); err != nil {
			return nil, err
		}
		log.Info("schema migrated")
		os.Exit(0)
	}
	gw, err := chain.NewGateway(cfg.Chain)
	if err != nil {
		return nil, err
	}
	return &App{Cfg: cfg, Log: log, Store: st, Gateway: gw}, nil
}

// SignerClient builds the sign-service client.
func (a *App) SignerClient() (*signer.Client, error) {
	return signer.NewClient(a.Cfg.Sign)
}

// EnergyManager builds the provider registry and the manager on top of it.
func (a *App) EnergyManager() (*energy.Manager, error) {
	provs, err := energy.Build(a.Cfg.Energy)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(provs))
	for n := range provs {
		names = append(names, n)
	}
	a.Log.Info("energy providers loaded", "providers", names, "mode", a.Cfg.Energy.Mode)
	return energy.NewManager(a.Cfg.Energy, a.Store, a.Gateway, a.Log, provs), nil
}

// Context returns a context cancelled on SIGINT/SIGTERM.
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

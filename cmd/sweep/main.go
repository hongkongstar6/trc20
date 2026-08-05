// Command sweep runs sweep-service, the runtime sweep threshold pricer and the
// rental provider prepaid balance auto topup loop.
package main

import (
	"time"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/sweep"
)

func main() {
	app, err := bootstrap.Init("sweep-service")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	signClient, err := app.SignerClient()
	if err != nil {
		app.Log.Error("sign client init failed", "err", err)
		return
	}
	mgr, err := app.EnergyManager()
	if err != nil {
		app.Log.Error("energy manager init failed", "err", err)
		return
	}
	pricer := energy.NewPricer(app.Cfg.Sweep.Threshold, app.Cfg.Energy, mgr, app.Gateway, app.Log)
	go func() {
		if err := pricer.Run(ctx); err != nil {
			app.Log.Error("pricer stopped", "err", err)
		}
	}()

	topup := energy.NewTopup(app.Cfg.Energy.AutoTopup, app.Store, app.Gateway, signClient, app.Log,
		mgr.Providers(), app.Cfg.Wallet.GasAccount.Path)
	go func() {
		if err := topup.Run(ctx); err != nil {
			app.Log.Error("topup loop stopped", "err", err)
		}
	}()

	svc, err := sweep.New(app.Cfg, app.Store, app.Gateway, signClient, mgr, pricer, app.Log)
	if err != nil {
		app.Log.Error("sweep init failed", "err", err)
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := svc.Reconcile(ctx); err != nil {
					app.Log.Error("sweep reconcile failed", "err", err)
				}
				if err := topup.Reconcile(ctx); err != nil {
					app.Log.Error("topup reconcile failed", "err", err)
				}
			}
		}
	}()

	if err := svc.Run(ctx); err != nil {
		app.Log.Error("sweep stopped", "err", err)
	}
}

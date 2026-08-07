// Command sweep runs sweep-service, the runtime sweep threshold pricer and the
// rental provider prepaid balance auto topup loop.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/sweep"
	"github.com/sirupsen/logrus"
)

//归集

func main() {
	pwd, _ := os.Getwd()
	fmt.Println("sweep 当前工作目录:", pwd)

	app, err := bootstrap.Init("sweep-service")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	signClient, err := app.SignerClient()
	if err != nil {
		logrus.Error("sign client init failed", "err", err)
		return
	}
	mgr, err := app.EnergyManager()
	if err != nil {
		logrus.Error("energy manager init failed", "err", err)
		return
	}
	pricer := energy.NewPricer(bootstrap.Cfg.Sweep.Threshold, bootstrap.Cfg.Energy, mgr, app.Gateway, nil)
	go func() {
		if err := pricer.Run(ctx); err != nil {
			logrus.Error("pricer stopped", "err", err)
		}
	}()

	topup := energy.NewTopup(bootstrap.Cfg.Energy.AutoTopup, app.Store, app.Gateway, signClient, nil,
		mgr.Providers(), bootstrap.Cfg.Wallet.GasAccount.Path)
	go func() {
		if err := topup.Run(ctx); err != nil {
			logrus.Error("topup loop stopped", "err", err)
		}
	}()

	svc, err := sweep.New(app.Store, app.Gateway, signClient, mgr, pricer, nil)
	if err != nil {
		logrus.Error("sweep init failed", "err", err)
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
					logrus.Error("sweep reconcile failed", "err", err)
				}
				if err := topup.Reconcile(ctx); err != nil {
					logrus.Error("topup reconcile failed", "err", err)
				}
			}
		}
	}()

	if err := svc.Run(ctx); err != nil {
		logrus.Error("sweep stopped", "err", err)
	}
}

// Command sweep runs sweep-service, the runtime sweep threshold pricer and the
// rental provider prepaid balance auto topup loop.
package main

import (
	"os"
	"time"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/sweep"
	"github.com/sirupsen/logrus"
)

// 归集
func main() {
	app, err := bootstrap.Init("sweep-service")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	pwd, _ := os.Getwd()
	logrus.Println("sweep 当前工作目录:", pwd)

	signClient, err := app.SignerClient()
	if err != nil {
		logrus.Error("sign client init failed", ",err:", err)
		return
	}
	mgr, err := app.EnergyManager()
	if err != nil {
		logrus.Error("energy manager init failed ", ",err:", err)
		return
	}
	pricer := energy.NewPricer(config.Cfg.Sweep.Threshold, config.Cfg.Energy, mgr, app.Gateway, nil)
	go func() {
		if err := pricer.Run(ctx); err != nil {
			logrus.Error("pricer stopped", ",err:", err)
		}
	}()

	topup := energy.NewTopup(config.Cfg.Energy.AutoTopup, store.MyStore, app.Gateway, signClient, nil,
		mgr.Providers(), config.Cfg.Wallet.GasAccount.Path)
	go func() {
		if err := topup.Run(ctx); err != nil {
			logrus.Error("topup loop stopped", ",err:", err)
		}
	}()

	svc, err := sweep.New(app.Gateway, signClient, mgr, pricer)
	if err != nil {
		logrus.Error("sweep init failed", ",err:", err)
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
					logrus.Error("sweep reconcile failed", ",err:", err)
				}
				if err := topup.Reconcile(ctx); err != nil {
					logrus.Error("topup reconcile failed", ",err:", err)
				}
			}
		}
	}()

	if err := svc.Run(ctx); err != nil {
		logrus.Error("sweep stopped", ",err:", err)
	}
}

// Command withdraw runs withdraw-worker plus the hot wallet energy pool.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/withdraw"
	"github.com/sirupsen/logrus"
)

func main() {
	pwd, _ := os.Getwd()
	fmt.Println("withdraw 当前工作目录:", pwd)
	app, err := bootstrap.Init("withdraw-worker")
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
	pool := energy.NewPool(bootstrap.Cfg.Energy, mgr, app.Gateway, nil, bootstrap.Cfg.Wallet.HotWallet.Address)
	go func() {
		if err := pool.Run(ctx); err != nil {
			logrus.Error("energy pool stopped", "err", err)
		}
	}()

	worker, err := withdraw.New(app.Store, app.Gateway, signClient, mgr, pool, nil)
	if err != nil {
		logrus.Error("withdraw worker init failed", "err", err)
		return
	}
	if err := worker.Run(ctx); err != nil {
		logrus.Error("withdraw worker stopped", "err", err)
	}
}

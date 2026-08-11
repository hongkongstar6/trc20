// Command withdraw runs withdraw-worker plus the hot wallet energy pool.
package main

import (
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/withdraw"
	"github.com/sirupsen/logrus"
)

func main() {
	app, err := bootstrap.Init("withdraw")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	pwd, _ := os.Getwd()
	logrus.Println("withdraw 当前工作目录:", pwd)

	signClient, err := app.SignerClient()
	if err != nil {
		logrus.Error("sign client init failed ", ",err:", err)
		return
	}
	mgr, err := app.EnergyManager()
	if err != nil {
		logrus.Error("energy manager init failed,", "err:", err)
		return
	}
	go func() {
		if err := mgr.RunReconcile(ctx); err != nil {
			logrus.Error("energy order reconcile loop stopped,", ",err:", err)
		}
	}()

	pool := energy.NewPool(config.Cfg.Energy, mgr, app.Gateway, nil, config.Cfg.Wallet.HotWallet.Address)
	go func() {
		if err := pool.Run(ctx); err != nil {
			logrus.Error("energy pool stopped,", ",err:", err)
		}
	}()

	worker, err := withdraw.New(store.MyStore, app.Gateway, signClient, mgr, pool, nil)
	if err != nil {
		logrus.Error("withdraw worker init failed,", ",err:", err)
		return
	}
	if err := worker.Run(ctx); err != nil {
		logrus.Error("withdraw worker stopped,", ",err:", err)
	}
}

// Command withdraw runs withdraw-worker plus the hot wallet energy pool.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/withdraw"
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
		app.Log.Error("sign client init failed", "err", err)
		return
	}
	mgr, err := app.EnergyManager()
	if err != nil {
		app.Log.Error("energy manager init failed", "err", err)
		return
	}
	pool := energy.NewPool(app.Cfg.Energy, mgr, app.Gateway, app.Log, app.Cfg.Wallet.HotWallet.Address)
	go func() {
		if err := pool.Run(ctx); err != nil {
			app.Log.Error("energy pool stopped", "err", err)
		}
	}()

	worker, err := withdraw.New(app.Cfg, app.Store, app.Gateway, signClient, mgr, pool, app.Log)
	if err != nil {
		app.Log.Error("withdraw worker init failed", "err", err)
		return
	}
	if err := worker.Run(ctx); err != nil {
		app.Log.Error("withdraw worker stopped", "err", err)
	}
}

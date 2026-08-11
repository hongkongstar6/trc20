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

// 归集服务
// 1. 监听链上充值事件或扫块
// 2. 充值确认与风控校验,区块确认数达到19块
// 3. 从第三方平台租赁手续费（TRX/能量）准备与划拨
// 4.发起 TRC20 归集转账,1 调用TRC20 合约的 transfer 2用私钥签名,3 签名后的交易广播至波场网络
// 5.归集结果确认与账目更新
func main() {
	app, err := bootstrap.Init("sweep")
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
	pricer := energy.NewPricer(config.Cfg.SweepServer.Threshold, config.Cfg.Energy, mgr, app.Gateway, nil)
	go func() {
		if err := pricer.Run(ctx); err != nil {
			logrus.Error("pricer stopped", ",err:", err)
		}
	}()

	go func() {
		if err := mgr.RunReconcile(ctx); err != nil {
			logrus.Error("energy order reconcile loop stopped", ",err:", err)
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

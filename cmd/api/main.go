// Command api serves the business facing wallet API and also drains the
// notification outbox, so a small deployment only needs one long lived process
// next to the workers.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/api"
	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/outbox"
	"github.com/sirupsen/logrus"
)

func main() {
	pwd, _ := os.Getwd()
	fmt.Println("api 当前工作目录:", pwd)
	app, err := bootstrap.Init("wallet-api")
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
	logrus.Infoln("abcd")
	// The merchant publisher is always on: a deposit is notified to the
	// callback URL of the merchant owning the address.
	publishers := []outbox.Publisher{outbox.NewMerchantPublisher(config.Cfg.Notify)}
	if config.Cfg.Notify.HTTP.Enabled {
		publishers = append(publishers, outbox.NewHTTPPublisher(config.Cfg.Notify))
	}
	if config.Cfg.Notify.RocketMQ.Enabled {
		mq, err := outbox.NewRocketMQPublisher(config.Cfg.Notify)
		if err != nil {
			logrus.Error("rocketmq publisher init failed", "err", err)
			return
		}
		defer mq.Close()
		publishers = append(publishers, mq)
	}
	dispatcher := outbox.NewDispatcher(config.Cfg.Notify, publishers...)
	go func() {
		if err := dispatcher.Run(ctx); err != nil {
			logrus.Error("outbox dispatcher stopped", "err", err)
		}
	}()

	r := api.New(signClient).Router()
	logrus.Info("wallet-api listening", "addr", config.Cfg.API.Listen)
	if err := r.Run(config.Cfg.API.Listen); err != nil {
		logrus.Error("http server stopped", "err", err)
	}
}

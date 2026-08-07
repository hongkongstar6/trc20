// Command api serves the business facing wallet API and also drains the
// notification outbox, so a small deployment only needs one long lived process
// next to the workers.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/api"
	"github.com/hongkongstar6/trc20/internal/bootstrap"
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
	var publishers []outbox.Publisher
	if bootstrap.Cfg.Notify.HTTP.Enabled {
		publishers = append(publishers, outbox.NewHTTPPublisher(bootstrap.Cfg.Notify))
	}
	if bootstrap.Cfg.Notify.RocketMQ.Enabled {
		mq, err := outbox.NewRocketMQPublisher(bootstrap.Cfg.Notify)
		if err != nil {
			logrus.Error("rocketmq publisher init failed", "err", err)
			return
		}
		defer mq.Close()
		publishers = append(publishers, mq)
	}
	if len(publishers) > 0 {
		dispatcher := outbox.NewDispatcher(bootstrap.Cfg.Notify, app.Store, nil, publishers...)
		go func() {
			if err := dispatcher.Run(ctx); err != nil {
				logrus.Error("outbox dispatcher stopped", "err", err)
			}
		}()
	} else {
		logrus.Warn("no notify publisher enabled, events will queue in notify_outbox")
	}

	r := api.New(bootstrap.Cfg, app.Store, signClient, nil).Router()
	logrus.Info("wallet-api listening", "addr", bootstrap.Cfg.API.Listen)
	if err := r.Run(bootstrap.Cfg.API.Listen); err != nil {
		logrus.Error("http server stopped", "err", err)
	}
}

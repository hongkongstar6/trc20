// Command api serves the business facing wallet API and also drains the
// notification outbox, so a small deployment only needs one long lived process
// next to the workers.
package main

import (
	"os"

	"github.com/hongkongstar6/trc20/internal/api"
	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/outbox"
	"github.com/sirupsen/logrus"
)

func main() {
	app, err := bootstrap.Init("api")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	pwd, _ := os.Getwd()
	logrus.Println("api 当前工作目录:", pwd)

	signClient, err := app.SignerClient()
	if err != nil {
		logrus.Error("sign client init failed", ",err:", err)
		return
	}
	// The merchant publisher is always on: a deposit is notified to the
	// callback URL of the merchant owning the address.
	publishers := []outbox.Publisher{outbox.NewMerchantPublisher(config.Cfg.Notify)}
	// The platform wide http callback is optional: without a configured
	// NOTIFY_URL there is no endpoint to post to, and keeping the publisher on
	// would only fill notify_outbox.last_error with connection refused.
	// switch {
	// case !config.Cfg.Notify.HTTP.Enabled:
	// case config.Cfg.Notify.HTTP.URL == "":
	// 	logrus.Warn("notify.http enabled but url is empty (NOTIFY_URL unset), platform callback disabled")
	// default:
	// 	publishers = append(publishers, outbox.NewHTTPPublisher(config.Cfg.Notify))
	// }
	if config.Cfg.Notify.RocketMQ.Enabled {
		mq, err := outbox.NewRocketMQPublisher(config.Cfg.Notify)
		if err != nil {
			logrus.Infof("配置: %+v", config.Cfg.Notify)
			logrus.Error("rocketmq publisher init failed", "err:", err)
			return
		}
		defer mq.Close()
		publishers = append(publishers, mq)
	}

	dispatcher := outbox.NewDispatcher(config.Cfg.Notify, publishers...)

	go func() {
		if err := dispatcher.Run(ctx); err != nil {
			logrus.Error("outbox dispatcher stopped", ",err:", err)
		}
	}()

	r := api.New(signClient).Router()
	logrus.Info("wallet-api listening", "addr", config.Cfg.API.Listen)
	if err := r.Run(config.Cfg.API.Listen); err != nil {
		logrus.Error("http server stopped", ",err:", err)
	}
}

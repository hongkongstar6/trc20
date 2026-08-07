// Command api serves the business facing wallet API and also drains the
// notification outbox, so a small deployment only needs one long lived process
// next to the workers.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hongkongstar6/trc20/internal/api"
	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/outbox"
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
		app.Log.Error("sign client init failed", "err", err)
		return
	}

	var publishers []outbox.Publisher
	if app.Cfg.Notify.HTTP.Enabled {
		publishers = append(publishers, outbox.NewHTTPPublisher(app.Cfg.Notify))
	}
	if app.Cfg.Notify.RocketMQ.Enabled {
		mq, err := outbox.NewRocketMQPublisher(app.Cfg.Notify)
		if err != nil {
			app.Log.Error("rocketmq publisher init failed", "err", err)
			return
		}
		defer mq.Close()
		publishers = append(publishers, mq)
	}
	if len(publishers) > 0 {
		dispatcher := outbox.NewDispatcher(app.Cfg.Notify, app.Store, app.Log, publishers...)
		go func() {
			if err := dispatcher.Run(ctx); err != nil {
				app.Log.Error("outbox dispatcher stopped", "err", err)
			}
		}()
	} else {
		app.Log.Warn("no notify publisher enabled, events will queue in notify_outbox")
	}

	srv := &http.Server{
		Addr:              app.Cfg.API.Listen,
		Handler:           api.New(app.Cfg, app.Store, signClient, app.Log).Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		app.Log.Info("wallet-api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.Log.Error("http server stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := contextWithTimeout(config.Duration("10s", 10*time.Second))
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		app.Log.Error("graceful shutdown failed", "err", err)
	}
}

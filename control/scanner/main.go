// Command scanner runs deposit-scanner: block scanning, confirmation and
// reorg handling.
package main

import (
	"os"

	"github.com/hongkongstar6/trc20/internal/bloom"
	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/scanner"
	"github.com/sirupsen/logrus"
)

func main() {

	app, err := bootstrap.Init("scanner")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	pwd, _ := os.Getwd()
	logrus.Println("scanner 当前工作目录:", pwd)

	// The api process pushes every newly allocated address to this port, so a
	// deposit to a brand new address is matched from the next block on.
	go func() {
		if err := bloom.Serve(ctx); err != nil {
			logrus.Error("bloom address sync server stopped", ",err:", err)
		}
	}()

	s := scanner.New(app.Gateway)
	logrus.Info("deposit scanner starting",
		"confirmations", config.Cfg.Deposit.Confirmations, "batch_blocks", config.Cfg.Deposit.BatchBlocks)
	if err := s.Run(ctx); err != nil {
		logrus.Error("scanner stopped", ",err:", err)
	}
}

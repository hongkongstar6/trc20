// Command scanner runs deposit-scanner: block scanning, confirmation and
// reorg handling.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/scanner"
	"github.com/sirupsen/logrus"
)

func main() {
	pwd, _ := os.Getwd()
	fmt.Println("scanner 当前工作目录:", pwd)
	app, err := bootstrap.Init("deposit-scanner")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	s := scanner.New(app.Store, app.Gateway)
	logrus.Info("deposit scanner starting",
		"confirmations", bootstrap.Cfg.Deposit.Confirmations, "batch_blocks", bootstrap.Cfg.Deposit.BatchBlocks)
	if err := s.Run(ctx); err != nil {
		logrus.Error("scanner stopped", "err", err)
	}
}

// Command scanner runs deposit-scanner: block scanning, confirmation and
// reorg handling.
package main

import (
	"fmt"
	"os"

	"github.com/hongkongstar6/trc20/internal/bootstrap"
	"github.com/hongkongstar6/trc20/internal/scanner"
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

	s := scanner.New(app.Cfg, app.Store, app.Gateway, app.Log)
	app.Log.Info("deposit scanner starting",
		"confirmations", app.Cfg.Deposit.Confirmations, "batch_blocks", app.Cfg.Deposit.BatchBlocks)
	if err := s.Run(ctx); err != nil {
		app.Log.Error("scanner stopped", "err", err)
	}
}

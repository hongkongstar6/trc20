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

//专属地址，确认收款流程
//1. 从数据库表ChainCursor确认已扫过的块的下一个区块开始，调用api获取区块日志
//2. decodeTransfer():解码trc20的transfer事件，就是地址:TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
//3. parseLog():布隆过滤法，查询userWallet,本系统拥有的地址
//4. 存入数据库表deposit_record,status=Pending 状态为待确认
//5. confirmUpTo() 待确认的充值记录，超过19个区块后，查询记录的状态，然后把deposit_record的状态设置为已确认
//6. 数据存入表notify_box,
//7. api服务从数据库表notify_box中，查找已确认未发送的数据，回调给merchant表的notify_url.

func main() {

	app, err := bootstrap.Init("scanner")
	if err != nil {
		panic(err)
	}
	ctx, stop := bootstrap.Context()
	defer stop()

	pwd, _ := os.Getwd()
	logrus.Debug("scanner 当前工作目录2:", pwd)
	// The api process pushes every newly allocated address to this port, so a
	// deposit to a brand new address is matched from the next block on.
	go func() {
		if err := bloom.Serve(ctx); err != nil {
			logrus.Error("bloom address sync server stopped", ",err:", err)
		}
	}()

	s := scanner.New(app.Gateway)
	// A contract allowlist belonging to another network is indistinguishable
	// from "no deposits happened" at runtime, so it has to stop the process
	// here instead of being discovered by a missing deposit.
	if err := s.VerifyTokens(ctx); err != nil {
		logrus.Error("token allowlist does not match the connected chain", ",err:", err)
		panic(err)
	}
	logrus.Info("deposit scanner starting",
		"confirmations", config.Cfg.Deposit.Confirmations, "batch_blocks", config.Cfg.Deposit.BatchBlocks)
	if err := s.Run(ctx); err != nil {
		logrus.Error("scanner stopped", ",err:", err)
	}
}

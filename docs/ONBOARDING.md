# 项目速览（程序员接手指南）

面向刚接手本仓库的人：先看第 1 节知道「哪个文件干什么」，再看第 2/3/4 节把三条主链路（充值到账通知、归集、提现）跑通。
设计动机与取舍见 [docs/DESIGN.md](DESIGN.md)，本文只讲代码现状。

一句话定位：**托管型 TRC20（USDT/USDC）钱包网关**。它只管链上——发地址、认充值、推事件、签名广播、归集、能量；
**不记用户余额**，余额/冻结/退款全在业务系统，网关只输出「已确认」的只追加事件流。

---

## 1. 目录与文件说明

### 1.1 顶层

| 路径                 | 作用                                                                                    |
| -------------------- | --------------------------------------------------------------------------------------- |
| `control/`           | 5 个进程的入口（`main.go`）。注意：README 里写的 `cmd/*` 就是这里，目录已改名为 `control` |
| `internal/`          | 全部业务实现，按职责分包（下面逐个说明）                                                |
| `configs/`           | YAML 配置：`config.yaml`（当前使用，主网模板）、`config.nile.yaml`（Nile 测试网）、`config.mainnet.example.yaml` |
| `deploy/migrations/` | 生产用的建表/变更 SQL，按序号执行；也被 docker-compose 挂到 mysql 初始化目录             |
| `docs/DESIGN.md`     | 设计稿：为什么这么做（能量成本模型、归集门槛推导、事件契约等）                            |
| `etc/wallet/tls/`    | sign 服务 mTLS 证书目录（仓库里只有占位文件，证书自己放）                                |
| `Dockerfile`         | 只把 `bin/` 拷进镜像，所以要先 `make build` 再 `make docker`                             |
| `docker-compose.yaml`| 本地/Nile 全栈：mysql、redis、rocketmq + 5 个服务；sign 不对宿主机暴露端口               |
| `Makefile`           | `build/fmt/vet/test/lint/migrate/docker`；`make migrate` 用 AutoMigrate（生产请用 SQL）  |
| `.env.example`       | 需要的环境变量清单（助记词、系统地址、HMAC、能量平台凭据等），复制成 `.env`              |
| `.vscode/launch.json`| 本地调试各进程的配置                                                                     |

### 1.2 五个进程（`control/`）

| 入口                     | 进程名           | 职责                                                                                     |
| ------------------------ | ---------------- | ---------------------------------------------------------------------------------------- |
| `control/api/main.go`    | wallet-api       | 对业务系统的 HTTP API（申请地址、提现下单、对账查询）**并且**跑 outbox 分发器（回调商户/发 MQ） |
| `control/scanner/main.go`| deposit-scanner  | 扫块、解析 TRC20 `Transfer`、确认、reorg 回滚、写 outbox；同时监听布隆地址同步端口         |
| `control/withdraw/main.go`| withdraw-worker | 提现状态机（风控→租能量→组装→签名→广播→结单→回调）+ 热钱包能量池                          |
| `control/sweep/main.go`  | sweep-service    | 归集用户地址余额到金融钱包 + 归集门槛定价器 + 租赁平台预付余额自动充值                     |
| `control/sign/main.go`   | sign-service     | **唯一接触助记词/私钥的进程**：派生地址、按策略签名；建议独立网段 + mTLS                   |

所有 main 都先调 `bootstrap.Init(service)`：加载配置 → 初始化日志 → **连 MySQL/Redis** → （仅 api 且 `-migrate`）AutoMigrate → （仅 scanner）加载布隆过滤器 → 建链上网关。

### 1.3 `internal/` 各包

| 包                    | 关键文件                                | 作用                                                                                     |
| --------------------- | --------------------------------------- | ---------------------------------------------------------------------------------------- |
| `bootstrap`           | `bootstrap.go`                          | 各进程共用的装配：配置、日志、DB、布隆、链网关、signer 客户端、能量管理器、信号 ctx        |
| `config`              | `config.go`、`dotenv.go`                | 配置结构体与校验、`${VAR:-default}` 环境变量展开、`.env` 加载、未知配置节报错、`Duration` 工具 |
| `logx`                | `logx.go`、`formatter.go`、`rotate.go`  | logrus 初始化、日志格式、按天/大小切割                                                    |
| `store`               | `store.go`                              | GORM/Redis 连接（全局 `store.MyStore`）、Redis 分布式锁、派生 index 分配器、`EnqueueOutbox`、黑名单/钱包查询 |
| `model`               | `model.go`                              | 全部表结构与状态常量（见 1.4），`AllModels()` 供迁移使用                                   |
| `api`                 | `api.go`、`util.go`                     | Gin 路由与处理器：商户增改查、申请地址、提现下单/查询、充值对账、outbox 事件查询；IP 白名单 + HMAC 防重放 |
| `merchant`            | `merchant.go`                           | 商户读取（启用校验）、`account = merchant_id + "_" + uid`、商户 sha256 参数签名/验签、回调签名 |
| `hd`                  | `hd.go`                                 | BIP39/BIP44 派生：`m/44'/195'/...`，助记词→私钥/地址，路径解析                             |
| `signer`              | `signer.go`、`server.go`、`client.go`   | 签名服务三件套：策略校验（用途/来源/目标/合约/限额）、HTTP 服务端 + 审计、其它进程用的客户端 |
| `tron`                | `address.go`、`tx.go`                   | 波场底层编解码：base58check 地址、Keccak、txID 计算、交易签名、`transfer`/`balanceOf` ABI 编码、uint256 解析 |
| `chain`               | `gateway.go`、`failure.go`、`ratelimit.go` | 节点网关：多节点按优先级故障转移、每节点令牌桶限速与 429 冷却、区块/回执/账户资源/链参数查询、组装交易、广播（同一签名字节重发）、失败码分类 |
| `bloom`               | `bloom.go`、`registry.go`、`http.go`    | 进程内布隆过滤器：判断链上收款地址「肯定不是我们的」；启动全量加载 `user_wallet`，增量同步 + api→scanner 推送新地址 |
| `scanner`             | `scanner.go`                            | 扫块主逻辑：游标、并发预取、reorg 检测回滚、解析 Transfer、确认深度、写 outbox               |
| `outbox`              | `outbox.go`、`rocketmq.go`              | 事务性发件箱分发器：商户回调发布器（按 `merchant_id`/事件自带 `notify_url`）、平台 HTTP 发布器、RocketMQ 发布器、指数退避与死信 |
| `sweep`               | `sweep.go`                              | 归集：候选地址筛选、余额与门槛判断、估能量→租能量→组装→签名→广播、对账与「已归集」标记      |
| `withdraw`            | `worker.go`                             | 提现状态机：风控、热钱包余额（含在途）校验、能量池/租赁、签名广播、固化回执结单、结果回调    |
| `energy`              | `manager.go`、`provider.go`、`pool.go`、`pricing.go`、`topup.go` | 能量子系统：多平台比价与下单、委托到账确认与订单对账、热钱包能量池批量租、归集门槛运行时定价、平台预付余额自动充值 |
| `energy/gasstation`   | `gasstation.go`                         | GasStation 平台实现（`init()` 自注册）                                                     |
| `energy/tronenergyrent`| `tronenergyrent.go`                    | TronEnergyRent 平台实现                                                                    |
| `energy/trxburn`      | `trxburn.go`                            | 兜底「不租、直接烧 TRX」的伪平台；只对归集生效，提现不烧                                   |

### 1.4 数据表（`internal/model/model.go`）

| 表                       | 作用                                                                       |
| ------------------------ | -------------------------------------------------------------------------- |
| `merchant`               | 商户：回调地址、币种/链、签名密钥、开关                                     |
| `user_wallet`            | 派生地址：`account`、`address`、`addr_index`、`derive_path`、`purpose`（deposit/hot/finance/gas）。**不存私钥** |
| `wallet_index_allocator` | 单调递增的派生 index（不用 uid 当 index）                                   |
| `deposit_record`         | 一条 TRC20 Transfer 入账记录，唯一键 `(txid, event_index)`；`status`、`internal`、`swept` |
| `withdraw_record`        | 提现订单，`order_no` 唯一 = 「同一订单最多出金一次」；存签名原文 `signed_raw` 与过期时间 |
| `sweep_record`           | 一次归集尝试：金额、费用模式、能量订单、失败码、覆盖到的最大 deposit id      |
| `sweep_skip`             | 低于门槛被跳过的次数，达到 `max_skip_rounds` 强制归集，归集成功后删除        |
| `address_blacklist`      | 提现禁止到账地址，每次校验实时查库（改动无需重启）                          |
| `energy_rent_order`      | 各平台能量租赁订单的统一记录（含基线能量，用于确认委托到账）                |
| `topup_record`           | 平台预付余额充值审计                                                        |
| `chain_cursor`           | 扫块游标（按网络命名，带 block_hash 供 reorg 判定）                         |
| `block_snapshot`         | 近期区块哈希指纹，用于回溯分叉点，定期清理                                  |
| `notify_outbox`          | 事务性发件箱：`event_id` 唯一、`status`(pending/sent/dead)、重试与退避       |
| `sign_audit`             | 每次签名请求的放行/拒绝审计（不含密钥材料）                                 |

---

## 2. 用户地址、余额查询与「扫块 → 通知业务」全流程

### 2.1 申请专属地址（`POST /v1/address`）

`internal/api/api.go: createAddress`

1. 商户参数 sha256 验签（`merchant.Verify`）、商户存在且启用、请求的 `symbol/chain` 必须与商户登记一致，且网关自身开启了该币种/链。
2. `account = merchant_id + "_" + uid`；已存在则直接返回旧地址（幂等）。
3. `store.NextAddressIndex()` 事务内取下一个派生 index（`wallet_index_allocator`，行锁）。
4. `path = m/44'/195'/0'/0/<index>`，调 **sign 服务** `POST /v1/derive` 拿地址（api 进程永远不接触种子）。
5. 落库 `user_wallet`，并 `bloom.NotifyWithRetry(address)` 把地址推给 scanner（推失败也不算错，scanner 会定时增量同步）。

### 2.2 余额怎么查

网关**不保存用户余额**，也没有对外的余额接口。余额有两种含义：

- **链上余额**：归集/提现内部用 `TriggerConstantContract` 只读调用 TRC20 `balanceOf` 得到
  （`sweep.tokenBalance` / `withdraw.tokenBalance`，编码在 `tron.EncodeTRC20BalanceOf`）。TRX 余额用 `chain.GetTRXBalance`。
- **业务账面余额**：由业务系统按收到的 `deposit` 事件自行累加；网关只提供对账接口
  `GET /v1/deposits`（按时间段回放已确认充值）、`GET /v1/deposit/:event_id`、`GET /v1/events`（含死信事件）。

### 2.3 扫块 → 确认 → 通知（scanner + api 的 outbox）

`internal/scanner/scanner.go`，循环周期 `deposit.poll_interval`（默认 3s，落后时不等待连续追块）：

1. `GetNowBlock` 取链头；`loadCursor` 读 `chain_cursor`（游标按网络命名；发现游标不属于当前链会对齐到链头）。
2. `confirmUpTo(head)`：先做一遍确认（见第 5 步）。
3. 取区间 `[cursor+1, cursor+batch_blocks]`，`fetchRange` 用 `fetch_concurrency` 并发预取区块与回执，**按序应用**。
4. **reorg 检测**：新块的 `parent_hash` 与游标 `block_hash` 不一致 → `handleReorg`：从 `block_snapshot` 往回找哈希仍匹配的分叉点（最多 `reorg_depth`），把该高度以上的 `pending` 充值改 `orphaned`、删快照、游标回退；超过 `reorg_depth` 直接报错要人工介入。
5. `scanBlock`：只看**执行成功**的交易，逐条日志 `decodeTransfer`（3 个 topic 且 topic0 是 Transfer、合约在白名单、金额 > 0 且 ≥ `min_deposit_units` 粉尘门槛）→ `parseLog`：
   - 先过**布隆过滤器**（`bloom.AddrFilter.MayContain`），未命中直接丢，命中才查 `user_wallet`；
   - 命中则写 `deposit_record`，`status=pending`；发起方也是我们自己的地址（归集/热钱包）时打 `internal=true`，业务系统不入账；
   - 同一事务内 upsert `block_snapshot`；`(txid, event_index)` 唯一索引保证重扫幂等。
6. 更新游标（块号 + 哈希）。
7. **确认**（`confirmUpTo` → `confirmOne`）：`block_number <= head - deposit.confirmations`（默认 19）的 pending 记录，重新拉一次回执：
   - 交易不存在或失败 → `orphaned`；换块了 → 更新 `block_number`；
   - 成功 → 同一事务里把记录改 `confirmed` 并 `store.EnqueueOutbox` 写入**恰好一条** `notify_outbox` 事件（`event_id = txid:event_index`，`internal` 记录不发事件）。
8. **投递**（api 进程内的 `outbox.Dispatcher`，`internal/outbox/outbox.go`）：轮询 `status=pending && next_retry<=now`，逐个交给所有发布器：
   - `MerchantPublisher`：按事件的 `merchant_id` 找商户，POST 到事件自带 `notify_url`（提现）或商户 `callback_url`（充值），body 用商户 secret 做 sha256 签名；
   - 可选的平台级 HTTP 发布器（当前在 api main 里被注释掉）与 RocketMQ 发布器（`notify.rocketmq.enabled`）；
   - 全部成功才置 `sent`；失败按 `2^n` 退避（上限 10 分钟），达到 `max_retry` 记为 `dead`，仍留在表里可通过 `GET /v1/events` 查询，不会静默丢失。

事件契约：至少一次投递 + 稳定 `event_id`，**下游按 `event_id` 幂等**。

---

## 3. 归集流程（sweep-service）

`internal/sweep/sweep.go`，周期 `sweep_server.interval`（配置里默认 1 小时）。每个启用的币种独立跑一轮。

1. **候选地址**：`deposit_record` 中 `status=confirmed AND swept=false AND internal=false AND contract=?` 的 `to_address` 去重（`max_per_round` 条），再关联 `user_wallet`（`purpose=deposit`）拿派生路径。
2. **门槛判断**：链上查 `balanceOf`。
   - 余额 0 → 若该地址历史上有过成功归集，则把它的未归集充值补标 `swept`（避免永远是候选）；
   - 余额 ≥ 门槛 → 立即归集。门槛来自 `energy.Pricer`：`threshold.fixed_usdt > 0` 时直接写死（当前配置 100 USDT），否则按「能量报价 + 带宽 + TRX 价格」运行时算出 `min_sweep`；
   - 余额 < 门槛 → `sweep_skip` 计数 +1，被跳过 `max_skip_rounds` 次或最老未归集充值超过 `stale_days` 才强制归集，保证小额最终也能收上来。
3. **并发保护**：Redis 锁 `sweep:<contract>:<address>`；同地址同币种若有 `created/energy_ready/broadcast` 的在途记录则跳过；同一地址连续 `OUT_OF_ENERGY` 失败次数达到 `max_energy_retries` 就停手并告警。
4. **估能量**（顺序很关键）：`TriggerConstantContract` 只读预执行估算，它不校验能量、也不带 expiration，所以能安全地排在租赁之前；重试时按 `RetrySafetyFactor(attempts)`（1.15→1.3→1.5）放大。
5. **落库** `sweep_record`（`status=created`，记录本次覆盖的最大 deposit id `deposit_max_id`）。
6. **拿能量**：`energy.Manager.Acquire`（比价/按序选平台，可退化为 `trx_burn` 烧地址自己的 TRX）→ 等委托到账（按下单前的基线能量确认）→ 状态改 `energy_ready`，记录费用模式与订单号。
   关闭租赁（`energy.rental_enabled=false`）时，先检查地址 TRX 够不够烧手续费，不够就本轮跳过，不写记录。
7. **组装** `BuildTRC20Transfer(from=用户地址, to=金融钱包)`（此刻才开始计交易过期时间）→ **签名**：调 sign 服务，`purpose=sweep`，服务端策略强制目标必须是金融钱包、合约在白名单 → 存 `txid`/`signed_raw`/`expired_at` → **广播**。
   广播被永久性拒绝（`chain.ClassifyBroadcast` 判定）直接标 `failed`；瞬时错误留在 `broadcast` 交给对账。
8. **对账**（`Reconcile`，每 30s）：
   - 链上成功 → `confirmed`，同一事务把 `id <= deposit_max_id` 的未归集充值标 `swept`，删掉 `sweep_skip`；
   - 链上失败 → `failed` + 失败码（`OUT_OF_ENERGY` 下轮加安全系数重试，USDT 未动）；
   - 链上查不到：未过期就**重发同一份签名字节**（重签名是确定性的，txid 变了就拒绝广播），已过期则标 `failed`，余额留给下一轮重新走流程。

配套循环（仅开启租赁时）：`energy.Manager.RunReconcile` 对账租赁订单（超时未确认标 `abandoned` 需人工与平台核账）、`energy.Topup` 用 gas 账户给平台预付余额自动充值。

---

## 4. 提现流程（withdraw-worker）

### 4.1 下单（api）

`POST /v1/withdraw` → `createWithdraw`：校验目标地址合法、`notify_url` 是绝对 http(s)、金额为正整数最小单位、链/币种受支持；写 `withdraw_record`（`status=created`，`from_address=热钱包`，我方生成 `trade_no`）。
`order_no` 唯一，重复提交返回已存在的订单，这就是「一个业务订单最多一次链上出金」的根。

### 4.2 执行（`internal/withdraw/worker.go`，周期 `withdraw_server.poll_interval`）

1. 取 `status=created` 的订单（每轮 20 条），每单加 Redis 锁 `withdraw:<order_no>`。
2. **风控 `riskCheck`**（钱包侧最后一道，业务风控在业务系统）：地址合法性、`address_blacklist`、目标不能是我们自己的地址、单笔上限 `max_amount_units`、当日累计上限 `daily_max_units`。不通过 → `rejected` 并**在同一事务写 outbox 回调**。
3. **热钱包余额校验**：链上 `balanceOf` 必须 ≥ 本单金额 + 同币种在途（`signed`/`broadcast`）金额之和。不够则 `halt`：订单**保持 created**、记 `fail_code=hot_wallet_insufficient` 并打 ALERT，等财务补币后自动重跑（不回退给业务系统）。
4. **能量**：`EstimateEnergy` 估算 → 有能量池且够用就直接用；否则 `AcquireRented` 只租不烧（租不到就 `halt` 告警，绝不烧热钱包 TRX）。
   `energy.rental_enabled=false` 时改为检查热钱包 TRX 是否够烧手续费，不够同样 `halt`。
5. **组装 → 签名 → 状态机推进**：`BuildTRC20Transfer(from=热钱包)` → sign 服务 `purpose=withdraw`（策略强制来源必须是热钱包、合约白名单）→ CAS 更新 `created → signed`（同时存 `txid`、`signed_raw`、`expired_at`），CAS 失败说明别的 worker 抢到了，直接返回。
6. **广播** → CAS `signed → broadcast`。永久性拒绝立刻结单为 `failed` 并回调（业务系统据此退款）；瞬时错误也进 `broadcast`，由对账按 txid 定论。
7. **对账结单（`Reconcile`，与主循环同周期）**：
   - 只依据**不可回滚**的回执：配了 `chain.solidity_for_confirm` 时读固化节点（`GetTxInfoByIDConfirmed`），否则退化为 `head - block_number >= confirm_blocks`。未固化 → 什么都不做，等下轮。
   - 链上查不到且未过期 → 重发同一份签名字节；查不到且已过期 → `failed`（"transaction expired without inclusion"）。
   - 固化成功 → `confirmed`；固化失败 → `failed` + 失败码。**提现永不换新交易重试**，避免同一业务订单重复出金。
   - 结单在事务里写 `notify_outbox`（`event_id = withdraw:<order_no>`，`event_type=withdraw_result`，带 `notify_url`），由 api 的 dispatcher 投递到下单时提交的 `notify_url`。
8. 业务系统也可主动查 `GET /v1/withdraw/:order_no`。

### 4.3 状态与失败码一览

- 提现：`created → signed → broadcast → confirmed | failed`，另有 `rejected`（风控拒绝，未上链）。
- `halt` 类失败码（订单仍 `created`，等人工/自动恢复）：`hot_wallet_insufficient`、`energy_rental_failed`、`hot_wallet_trx_insufficient`。
- 链上失败码分类见 `internal/chain/failure.go`（如 `OUT_OF_ENERGY`、过期、节点拒绝等），业务系统可直接按 `fail_code` 分支处理。

---

## 5. 本地怎么跑

```bash
cp .env.example .env        # 填助记词、系统地址、HMAC、（主网才需要）能量平台凭据
make build && make docker   # 镜像只拷 bin/，必须先 build
docker compose up -d        # mysql/redis/rocketmq + api/scanner/withdraw/sweep/sign
```

要点：

- 配置查找顺序：`-config` 参数 → `CONFIG_PATH` → 向上找 `configs/config.yaml`（再退 `configs/config.nile.yaml`）；YAML 支持 `${VAR:-default}`。
- 建表：`make migrate`（AutoMigrate，开发用）或执行 `deploy/migrations/*.sql`（生产用）；compose 里 api 启动命令自带 `-migrate`。
- Nile 测试网必须 `energy.rental_enabled=false`（两家租赁平台都没有测试环境），归集靠地址自己的 TRX 烧手续费。
- TronGrid 限速按 API Key 统计，scanner/api/sweep/withdraw 共用一个 Key 时，各进程 `qps` 之和才是实际用量。
- 提交前跑 `make fmt vet test`（或 `golangci-lint run`）。

## 6. 接手时容易踩的点

- **sign 服务当前也依赖 MySQL**：`bootstrap.Init("sign")` 无条件 `store.Open()`，且审计走 `NewDBAudit()` 写 `sign_audit`，所以数据库不可用时 sign 起不来（compose 里也写了 `depends_on: mysql`）。签名/派生本身不需要数据库，如果按「独立网段」部署，可考虑换成文件/日志型 `AuditSink` 或把 DB 审计降级为可选。
- 布隆过滤器只在 scanner 进程初始化（`bootstrap.needsAddressFilter`），api 只是通过 HTTP 把新地址推给它。
- outbox 分发器跑在 api 进程里，**api 不启动就没有任何回调/MQ 投递**。
- `AutoMigrate` 只在 api 且 `-migrate` 时执行；其他进程不会改表结构。
- `internal/` 下的包不能被外部仓库引用（Go internal 规则），对外只有 HTTP API + 事件。


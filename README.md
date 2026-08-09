# trc20-wallet-gateway


托管型 USDT-TRC20 钱包网关：地址生成、存款扫描和

确认、事件传递、提现签名和广播、资金清零至

金融钱包以及能源管理。

该网关特意**不保存用户余额**。它发出一个
仅追加的已确认事件流；账本、

冻结、借记和退款均由业务系统管理。请参阅

[docs/DESIGN.md](docs/DESIGN.md)了解完整的设计以及

每个决策背后的原因。

## 服务

每个进程一个二进制文件，全部基于同一镜像构建：

| 二进制文件 | 功能 |

| --------------- | ---- |

| `cmd/api` | 面向业务的 HTTP API：地址分配、提现提交、对账查询 |

| `cmd/scanner` |区块扫描、TRC20 `Transfer` 解析、确认、重组回滚、发件箱入队 |

| `cmd/withdraw` | 提现状态机：构建、签名、广播、确认、报告 |

| `cmd/sweep` | 将用户存款地址扫入金融钱包，并为此租用能源 |

| `cmd/sign` | 唯一接触密钥材料的进程 |

`internal/chain` 是节点网关：一个按优先级排序的节点列表

（自托管的 FullNode 和/或 TronGrid），具有查询故障转移功能，以及一条广播路径
该路径将*相同的签名字节*发送到备用节点，而不是重建

TronGrid 按 API Key 限速（免费 Key 15 次/秒），超限会把整个 Key 暂停几十秒。
网关内置每节点令牌桶（`chain.nodes[].qps` / `burst`，trongrid 类型默认 10 QPS）
在请求发出前限速；收到 429 时按 `Retry-After` 或响应体里的
`suspended for N s` 把该节点挂起，期间只走其它节点，若节点全部被限流则最多
等待 `chain.rate_limit_wait`（默认 60s）后再报错——扫块宁可等待也不能跳块。
注意限额是按 Key 统计的：scanner / api / sweep / withdraw 共用同一个 Key 时，
每个进程的 `qps` 之和才是实际用量。

cmd/api/main.go — 业务侧钱包 HTTP API 服务（wallet-api），同时兼任 notify outbox 的分发器，小型部署只需这一个常驻进程配合 worker。它初始化 signer 客户端、按配置启用 HTTP/RocketMQ 发布器运行 outbox.Dispatcher，并启动 http.Server。 main.go:1-3 main.go:44-59

cmd/scanner/main.go — 充值扫描服务（deposit-scanner）：区块扫描、确认数处理与 reorg 处理，运行 scanner.New(...).Run(ctx)。 main.go:1-2 main.go:18-21

cmd/sign/main.go — 签名服务（sign-service）：唯一持有密钥材料的进程，需部署在独立网络段仅供 worker 访问，生产环境应从 KMS/HSM/Vault 读取助记词。支持 mTLS（要求客户端证书）。 main.go:1-3 main.go:65-67

cmd/sweep/main.go — 归集服务（sweep-service）：运行归集阈值定价器（energy.Pricer）与租赁商预付余额自动充值循环（energy.Topup），并周期性 Reconcile。 main.go:1-2 main.go:31-46

cmd/withdraw/main.go — 提现 worker（withdraw-worker）：加上热钱包能量池（energy.Pool），运行 withdraw.New(...).Run(ctx)。 main.go:1 main.go:28-35


交易。

## 快速入门 (Nile)

```bash

cp .env.example .env # 填写助记符、HMAC 密钥和地址

docker compose up -d mysql redis

docker compose up --build api scanner withdraw sweep sign

```

`docker compose` 会在 MySQL 容器首次启动时应用 `deploy/migrations/0001_init.sql`。

对于已存在的数据库，请手动应用该文件，或运行

`make migrate` 以使用 GORM `AutoMigrate`。

SQL 文件是权威 schema，`AutoMigrate` 仅用于开发环境：两者的列和

全部唯一索引一致，但 `AutoMigrate` 会把生产 SQL 中声明为 `NOT NULL` 的列

建成可为 NULL，因此由它建出的库会接受生产环境拒绝的数据。

不使用 Docker：

```bash

make build

CONFIG_PATH=configs/config.nile.yaml ./bin/sign

CONFIG_PATH=configs/config.nile.yaml ./bin/api

```

## 配置

`configs/config.nile.yaml` 是测试网的默认配置；

`configs/config.mainnet.example.yaml` 文件记录了主网的增量配置。每个密钥

都是一个 `${ENV}` 或 `${ENV:-default}` 引用，因此不会提交任何凭证。

所需的环境变量列在 `.env.example` 文件中。

启动时会自动加载 `.env`：优先取 `ENV_FILE` 指定的文件，否则从配置文件所在目录
（再退回当前工作目录）向上查找最近的 `.env`。已存在的真实环境变量优先，文件不存在
也不会报错。未传 `-config` / `CONFIG_PATH` 时，会向上查找 `configs/config.yaml`，
找不到再回退到 `configs/config.nile.yaml`，因此在 VS Code 里直接调试 `cmd/*`
无需额外参数（`.vscode/launch.json` 已提供各服务的调试配置）。

Nile 配置中的两个属性是有意为之：

- `energy.mode: fixed` 和 `energy.fixed: trx_burn`，并且两个租赁能源提供商

均已禁用。GasStation 和 TronEnergyRent 都没有测试环境，因此在

Nile 上它们完全无法使用；它们在主网上以小额交易进行验证。

- `energy.auto_topup.enabled: false`。禁用后，预付余额过低时，

只会发出警报。

## 能源

扫款和提款都会租赁能源。能源提供商是位于以下位置的插件：

`internal/energy.Provider`;添加平台意味着只需实现一个功能，并添加一个

配置块，无需更改扫款或提现逻辑。

- `mode: cheapest` 会对所有已启用的供应商进行报价，并选择最便宜的供应商，以满足

实际能源需求，包括每个平台的最低订单金额。

- 如果租用失败或超时，则会回退到销毁 TRX，因此平台故障

不会导致支付中断。

- `min_sweep` 在运行时根据实时报价和链上

`getEnergyFee` 计算得出，绝不会硬编码：成本高于收益的扫款

不值得广播。

- 热钱包使用能量池：在低水位线时，它会租用一个批次，

以覆盖大约一小时的提现量，这会将每天约 200 个订单减少到

约 20 个，并消除每次提现的租用延迟。

自动预付充值由专用的小额 gas 账户提供资金，绝不会由

金融冷钱包提供资金。每次充值都会从提供商 API 重新读取存款地址，并将其与硬编码的白名单进行比较，每次充值都受到单次转账、每日和单笔交易次数的限制，并且只有在前一次充值尚未到账的情况下才会开始新的充值。

## 密钥

`cmd/sign` 是唯一拥有种子值的进程。其他所有进程都持有派生路径，如果没有种子值，这些路径将毫无用处。地址由单调索引分配器分配；uid 永远不会用作派生索引。

签名服务会在签名前根据具体用途强制执行策略，并且声明的意图会与实际执行的调用数据进行比对：

- `withdraw` — 必须来自热钱包，且必须是已列入白名单的代币合约

- `sweep` — 必须支付到金融钱包，且必须是已列入白名单的代币合约

- `topup` — 必须来自 gas 账户，且必须是向白名单提供商的充值地址进行普通的 TRX 转账，金额不得超过单次转账限额

对于生产环境，请使用 KMS/HSM/Vault Transit 而非助记词来保护密钥，并将签名服务部署在独立的网络段上，并使用 mTLS 进行保护。

## 商户

每个用户都归属于一个商户。商户表 `merchant` 的字段为：主键 `id`、商户号
`merchant_id`、商户名称 `name`、回调地址 `callback_url`、sha256 密钥 `secret`、
状态 `status`（1 开启 / 0 关闭）。`secret` 只写不读，API 不会返回它。

```bash
# 新增/更新商户
POST /v1/merchant  {"merchant_id":"m1","name":"商户一","callback_url":"https://m1/notify","secret":"sfejo","status":1}
GET  /v1/merchants
```

申请专属地址时必须带上 `merchant_id`，并对参数签名：参数按 key 升序拼成
`k1=v1&k2=v2`，末尾接上商户密钥后取 sha256，签名放在 `sign` 字段里（`sign`
本身与空值不参与签名）。

```text
参数 {"a":1,"b":2}，secret="sfejo"
sign = sha256("a=1&b=2sfejo")
请求体 {"a":1,"b":2,"sign":sign}
```

地址按「商户号 + 用户 id」唯一分配：`wallet.account = <merchant_id>_<uid>`，
因此两个商户下的同一个 uid 是两个账号、两个地址。同一账号重复申请会返回
已分配的地址。

```bash
POST /v1/address  {"merchant_id":"m1","uid":"1001","sign":"<sha256>"}
-> {"merchant_id":"m1","uid":"1001","account":"m1_1001","address":"T...","chain":"TRON"}
```

## 专属地址匹配（布隆过滤器）

区块里的收款地址不再逐个回 MySQL 查询：`internal/bloom` 是进程内纯 Go 实现的
布隆过滤器（无 Redis、无第三方库），扫块时先用它过滤。

- 未命中 = 一定不是我们的地址，直接丢弃，不产生任何数据库查询；
- 命中 = 可能是某个专属地址，再按地址回 `user_wallet` 查出商户与账号。

因此过滤器只会「误报」不会「漏报」：误报的代价是一次多余的查询，漏报会丢充值，
而布隆过滤器结构上不可能漏报。

生命周期：

1. 只有 scanner 会用过滤器匹配链上收款地址，所以只有 scanner 在 `bootstrap.Init`
   时按 id 分页读取 `user_wallet` 全表建过滤器；api / sign / sweep / withdraw
   不再各存一份。2 万个地址在 `false_positive_rate: 0.0001` 下约占 48 KB 内存。
2. `POST /v1/address` 分配新地址后，api 异步 POST 推送给 scanner（两个进程），
   scanner 收到即刻加入，下一个区块就能命中。推送不阻塞、也不影响分配结果。
3. 兜底：推送失败时 10 秒后自动重推一次，仍失败即放弃；scanner 每
   `bloom.sync_interval` 按 `id > max_id` 从 `user_wallet` 增量补齐，
   所以一次推送失败不会丢地址，api 也不会因为 scanner 短暂不可用而分配失败。
4. 地址数超过 `bloom.expected_addresses` 后，过滤器会以两倍容量从全表重建，
   避免过载的过滤器把请求全部压回 MySQL。

scanner 的地址同步端口（`bloom.listen`，compose 里是 `:8091`，只在内网可达）：

```bash
POST /internal/bloom/address  {"addresses":["T..."]}   # 头 X-Bloom-Token: <bloom.token>
GET  /internal/bloom/stats    # 地址数、容量、bit 数、hash 数、当前误报率、max_id
```

配置见 `configs/config.yaml` 的 `bloom` 段。

## 事件传递

状态变更及其对应的发件箱行写入同一个数据库事务，因此
事件不会因两者之间的崩溃而丢失。传递至少一次
通过 HTTP 回调和/或 RocketMQ 进行，并具有重试机制和死信机制。事件 ID
是稳定的——存款事件的 ID 为 `<txid>:<event_index>`——因此业务系统
会按 ID 进行去重。`/v1/deposits` 和 `/v1/events` 允许重放单个
事件或时间范围以进行对账。

用户地址收到转账并确认后，事件除了走平台级发布器，还会按 `merchant_id`
投递到该商户的 `callback_url`：请求体是事件本身加上用商户密钥按同一规则算出的
`sign`，商户按相同规则验签即可。商户被关闭或未配置回调地址时跳过该商户投递，
事件仍可通过 `/v1/deposits`、`/v1/events` 对账。

## 开发

```bash

make fmt vet test build

```

## 状态

单元测试涵盖地址编码、HD 派生、事务签名、节点

故障转移和广播语义、`Transfer` 日志验证、签名策略、

提供商根据记录的 API 格式进行报价/排序/轮询、配置加载

以及发件箱签名。

尚未验证：在 Nile 上针对实时节点和数据库进行的端到端运行，
以及 GasStation 和 TronEnergyRent 的主网灰度验证。
## 需求背景

设计并开发基于 TRON（TRC20）网络的 USDT 托管钱包系统，实现用户专属充值地址分配、链上充值监听、资金归集（Sweep）、提现签名、账本管理等核心功能。采用 HD Wallet（BIP39/BIP32/BIP44）统一管理钱包体系，实现交易所钱包模式的资金管理方案。


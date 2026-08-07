# trc20

cmd/api/main.go — 业务侧钱包 HTTP API 服务（wallet-api），同时兼任 notify outbox 的分发器，小型部署只需这一个常驻进程配合 worker。它初始化 signer 客户端、按配置启用 HTTP/RocketMQ 发布器运行 outbox.Dispatcher，并启动 http.Server。 main.go:1-3 main.go:44-59

cmd/scanner/main.go — 充值扫描服务（deposit-scanner）：区块扫描、确认数处理与 reorg 处理，运行 scanner.New(...).Run(ctx)。 main.go:1-2 main.go:18-21

cmd/sign/main.go — 签名服务（sign-service）：唯一持有密钥材料的进程，需部署在独立网络段仅供 worker 访问，生产环境应从 KMS/HSM/Vault 读取助记词。支持 mTLS（要求客户端证书）。 main.go:1-3 main.go:65-67

cmd/sweep/main.go — 归集服务（sweep-service）：运行归集阈值定价器（energy.Pricer）与租赁商预付余额自动充值循环（energy.Topup），并周期性 Reconcile。 main.go:1-2 main.go:31-46

cmd/withdraw/main.go — 提现 worker（withdraw-worker）：加上热钱包能量池（energy.Pool），运行 withdraw.New(...).Run(ctx)。 main.go:1 main.go:28-35

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

## 事件传递

状态变更及其对应的发件箱行写入同一个数据库事务，因此
事件不会因两者之间的崩溃而丢失。传递至少一次
通过 HTTP 回调和/或 RocketMQ 进行，并具有重试机制和死信机制。事件 ID
是稳定的——存款事件的 ID 为 `<txid>:<event_index>`——因此业务系统
会按 ID 进行去重。`/v1/deposits` 和 `/v1/events` 允许重放单个
事件或时间范围以进行对账。

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

* 平台统一管理钱包私钥
* 使用 HD Wallet（BIP39 + BIP32 + BIP44）批量生成用户专属充值地址
* 每个用户拥有唯一 TRON 地址
* 监听 TRON 公链上的 USDT-TRC20 转账
* 自动识别用户充值
* 生成充值流水并通知业务系统
* 用户提现时，由平台签名并广播交易
* 支持资金归集（Sweep），将用户地址资金归集到平台财务地址

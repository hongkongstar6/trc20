# USDT-TRC20 托管钱包系统 — 方案讨论稿（v0.6，仅设计，不含代码）

> v0.6 变更：新增 **租赁平台预付余额自动补给（带开关）** 设计，资金源定为独立的**小额 gas 账户**（不直连财务冷钱包）—— 见 9.4。
>
> v0.5 变更（按你最新三条）：出金热钱包**改回租赁（不质押）**并新增「能量池 + 批量租」优化；选路默认 **`cheapest` 比价**；Provider **插件化注册**（后续加第三家只写一个实现 + 改配置，不动主干）；按 **200 笔/日** 算出成本与容量模型（见 9.1e）。
>
> v0.4 变更：新增 **TronEnergyRent** 作为第二家能量租赁平台并做成**可配置切换 + 自动比价**（已读其 OpenAPI 规范并**实测其公开报价接口**）、给出**用实时链上参数算出的 `min_sweep` 盈亏平衡模型**、出金热钱包改为**自质押 TRX**。
> 历史决策：能量租赁为主 + 烧 TRX 兜底；TronGrid 为主 / FullNode 就绪后切主；先跑 Nile；钱包系统**只推流水**，余额由业务系统维护。
>
> ⚠️ 仍未解决的前置坑：**GasStation 明确不支持测试环境**（文档原文 “Not supported in the test environment”），Nile 上租不到能量；TronEnergyRent 文档也只给出主网 API。→ 测试网阶段一律走 `trx_burn` Provider，租赁链路等主网小额灰度验证。

---

## 0. 系统定位

本系统 = **链上资产网关**，不是账务系统。

| 职责 | 钱包系统 | 业务系统 |
|---|---|---|
| 地址分配 | ✅ | ❌ |
| 链上充值识别、确认、去重 | ✅ | ❌ |
| 充值流水推送（MQ + 回调） | ✅ | 消费并加余额 |
| 用户余额 / 冻结 / 可用 | ❌ | ✅ |
| 提现风控、扣款、冻结 | ❌ | ✅ |
| 提现下单接收 + 签名广播 + 结果回推 | ✅ | 发起 + 据结果确认/退款 |
| 归集、能量管理、热钱包 | ✅ | ❌ |

**边界铁律**：同一笔链上事件，**只推一次「确认态」流水**，且带全局唯一 `event_id` 供下游幂等；提现只保证「同一 `biz_order_no` 最多出金一次」。

---

## 1. 总体架构

```
                       ┌───────────────────────────────────────────┐
   业务系统 ──HTTP──▶  │ wallet-api (Gin)                          │
      ▲                │  地址申请 / 提现下单 / 流水查询 / 对账拉取 │
      │                └───────┬───────────────────────┬───────────┘
      │ MQ + HTTP回调          │                       │
      │                ┌───────▼───────────────────────▼──────────┐
      │                │ wallet-service：流水记录 / 状态机 / outbox │
      │                └───┬───────────┬──────────────┬───────────┘
   ┌──┴────────────────────▼──┐   ┌────▼───────────┐  ┌▼──────────────────┐
   │ deposit-scanner           │   │ withdraw-worker│  │ sweep-service      │
   │ 区块拉取/确认位/重组处理   │   │ 构造+广播+追确认│  │ 归集 + 能量供给调度 │
   └───────────┬───────────────┘   └────┬───────────┘  └────┬───────┬──────┘
               │                        │ 仅传"未签名交易"   │       │
               │                   ┌────▼───────────────────▼────┐  │
               │                   │ sign-service（独立网段）      │  │
               │                   │ HD 派生 + 签名，只出签名结果  │  │
               │                   └──────────┬──────────────────┘  │
               │                              │ KMS/HSM              │
   ┌───────────▼──────────────────────────────▼───────────┐  ┌──────▼────────────────┐
   │ chain-gateway：TronGrid(主) ⇄ FullNode(就绪后转主)     │  │ energy-provider        │
   │ 统一封装 查询/广播/熔断/限流                            │  │ GasStation/TronEnergy  │
   │                                                       │  │ Rent/自质押/TRX兜底 比价│
   └───────────────────────────────────────────────────────┘  └───────────────────────┘

   MySQL 8（流水+状态） | Redis（地址集合/锁/幂等） | RocketMQ（流水通知）
```

## 2. chain-gateway：节点主备（v0.3 调整为 TronGrid 先主）

统一接口：`GetNowBlock / GetBlockByNum / GetTxInfoByBlockNum / GetTxInfoById / TriggerConstantContract / BroadcastTx / GetAccountResource`。

节点优先级 **完全由配置驱动**（`nodes: [{name, type, endpoint, priority, weight}]`）：
- 阶段一（现在）：`TronGrid(priority=1)` → `TronGrid 备用 Key(priority=2)`
- 阶段二（你的 FullNode 就绪）：`FullNode(priority=1)` → `TronGrid(priority=2)`
- **切换只改配置，不改代码**，且支持灰度（按请求类型分流：先把「查询」切到 FullNode，「广播」仍双发）。

**查询路由**
```
主节点 → 成功返回
      → 失败/超时(800ms)/高度落后 → 备节点
                                  → 都失败 → 熔断，游标停在原地并告警（绝不跳块）
```
健康判定不能只看进程存活：比较各节点 `now_block`，**落后主链 > 20 块判定不健康**自动降级，否则表现为「充值莫名延迟」。TronGrid 有 QPS 限制，需内置限流器 + 429 退避。

**广播路由**
```
主节点广播
 ├─ result=true → 记 txid，进入确认追踪
 ├─ 明确业务失败（SIGERROR / TAPOS / 余额不足 / CONTRACT_VALIDATE_ERROR）→ 不重试
 ├─ DUP_TRANSACTION_ERROR → 视为成功（已在内存池）
 └─ 网络错误/超时 → 备节点广播【同一份已签名字节】
```
- 兜底广播必须重发**同一份签名交易**（txid 相同，链上天然去重），**绝不允许重新构造**。
- 广播失败 ≠ 没上链：网络类错误一律按 txid 轮询链上定论；交易 `expiration`（默认 60s）过期且链上查无，才可判失败。

**环境**：Nile 先行；合约地址、确认位、节点列表、GasStation 开关全部走配置 / `coin` 表，测试网与主网代码零差异。

## 3. 服务拆分

| 服务 | 职责 | 扩容 | 约束 |
|---|---|---|---|
| wallet-api | 对外 HTTP（HMAC 签名 + 时间戳防重放 + IP 白名单） | 是 | 无状态 |
| wallet-service | 流水写入、状态机、outbox | 是 | 状态流转 CAS |
| deposit-scanner | 游标推进、事件解析、确认位、重组回滚 | **单主** | 分布式锁选主 |
| withdraw-worker | 出金构造/广播/确认 | 是（按记录分片） | 单笔串行 |
| sweep-service | 归集 + 能量供给编排 | 是 | 每地址一把锁 |
| sign-service | HD 派生 + 签名 | 是 | 独立网段、mTLS、白名单、全审计 |
| chain-gateway / energy-provider | 建议先做成**内嵌库**而非独立服务，少一跳、少一处故障点 | — | — |

交付形态建议：**monorepo + 多 entrypoint**（一个代码库，`-mode=api|scanner|withdraw|sweep|sign`），后续可平滑物理拆分。

## 4. MySQL 表结构（无 ledger / 无余额表）

金额统一存 **最小单位** `DECIMAL(38,0)`（USDT decimals=6），展示层换算。

1. `coin`：`id, chain, symbol, contract_address, decimals, confirmations, min_deposit, min_sweep, status`（多链多币开关，合约地址禁止硬编码）
2. `wallet`：`id, uid, chain, address, derive_path, address_index, status, created_at`；唯一 `uq(chain,address)`、`uq(chain,derive_path)`、`uq(uid,chain)`
3. `wallet_index_allocator`：`chain, next_index`（独立自增，**不用 uid 当 index**）
4. `deposit_record`：`id, uid, coin_id, txid, event_index, from_address, to_address, amount, block_number, block_hash, status(pending/confirmed/orphaned/notified), created_at`；唯一 `uq(txid,event_index)`
5. `withdraw_record`：`id, biz_order_no(唯一), uid, coin_id, to_address, amount, txid, signed_raw, expire_at, status(created/signed/broadcast/confirmed/failed/expired), retry_count, fail_reason, fail_code`
   → `fail_reason` 是节点原文，`fail_code` 是稳定枚举（`out_of_energy / revert / sig_error / validate_error / tapos_error / bandwidth / expired / node_error …`）。重试与告警只认 `fail_code`，不解析文案；`fail_code` 一并推给业务方回调
6. `sweep_record`：`id, wallet_id, coin_id, amount, txid, energy_used, trx_burned, fee_mode(rent/burn), rent_order_id, status, fail_reason, fail_code, retry_count, deposit_max_id, created_at`
   → `deposit_max_id` 是本次归集覆盖的最大充值 id：确认后只把 `id <= deposit_max_id` 的充值标 `swept`，归集在途期间新到的充值保持未归集，下一轮再走一遍；`retry_count` 是该地址连续 `out_of_energy` 失败次数，用来抬高安全系数并在超过 `sweep_server.max_energy_retries` 后停手
7. `energy_rent_order`（兼容两家）：`id, provider(gasstation/tronenergyrent), request_id(唯一,我方幂等键), provider_order_id(trade_no 或 orderId), receive_address, resource_type, requested_energy, delegated_energy, baseline_energy, period, cost_trx, state(本地统一枚举: submitting/created/delegated/failed/partial/reclaimed), provider_raw_state, delegate_txid, delegated_at, expire_at, created_at`
   → **两家的状态枚举不同（GasStation 用 0/1/2/3/10，TronEnergyRent 用 PAID_BY_USER/ENERGY_DELEGATED/…），必须在 Provider 层归一化成本地枚举**，业务代码不感知平台差异。
8. `chain_cursor`：`chain, scanner, last_block, last_block_hash, updated_at`
9. `block_snapshot`：最近 N 块 `number/hash/parent_hash`，用于定位分叉点
10. `notify_outbox`：`id, topic, biz_key, payload, status, retry, next_retry_at, last_error`（与业务写入同事务）
11. `hot_wallet`：出金热钱包 / gas station（TRX 补给）地址与水位
12. `sign_audit`：签名请求审计（调用方、path、交易摘要、结果），**不落私钥**

## 5. HD Wallet

- BIP39（24 词 + passphrase）→ BIP32 → BIP44 `m/44'/195'/0'/0/{index}`（195 = TRON）。
- Solana 扩展：`m/44'/501'/{index}'/0'`，**ed25519 + SLIP-0010**，与 secp256k1 派生不同 → sign-service 内按 chain 分派 `Signer{Derive, Sign}`。
- 存储：dev `.env`；生产 KMS 信封加密或 HSM/Vault Transit。
- 内存卫生：私钥用后清零；日志/错误/panic 栈脱敏；敏感结构体自定义 `String()`。
- 三层钱包：用户充值地址（热）→ 财务归集地址（冷/多签）→ **出金热钱包（独立助记词、限额持币、冷钱包定时补给）**。

## 6. TRON 地址生成（原理）

私钥 → secp256k1 非压缩公钥（去 `0x04`）→ Keccak-256 → 后 20 字节 → 前缀 `0x41` → Base58Check → `T` 开头。
入库前自检：长度 34、Base58 校验和、`0x41` 前缀，并抽样与节点 `validateaddress` 比对。

## 7. 充值扫描

**主通道**：按块 `gettransactioninfobyblocknum`，本地过滤 `topic0 = keccak("Transfer(address,address,uint256)")` 且 `log.address == USDT 合约`。
**辅通道（对账 + 安全网）**：TronGrid `/v1/accounts/{addr}/transactions/trc20` 定时按地址补扫近 N 小时。

流程：`游标块 → 拉块 → 解析 log → to 命中 wallet（Redis Set + 布隆过滤器）→ deposit_record(pending) → 确认位达标 confirmed → 同事务写 outbox → 推送 → notified`

必须处理：
- **确认位**：TRON 约 19 块（约 1 分钟）后固化。建议以 **solidity 节点（已固化数据）** 为准，比自己数确认位更稳；`coin.confirmations` 可配。
- **执行结果**：校验 `receipt.result == SUCCESS`（REVERT 交易也可能留 log）。
- **合约白名单**：只认 `coin` 表登记的合约，防山寨合约假充值。
- **金额过滤**：`value=0` 或 `< min_deposit` 只落库不推送（防粉尘刷爆下游）。
- **内部转账**：from 也属本系统 → 标 `internal`，不推流水。
- **链重组**：保存 `block_hash`，游标块 hash 与链上不符 → 回滚到分叉点，pending 置 `orphaned`；**只有 confirmed 才推流水**，因此重组不会出现「推了又要撤」。

## 8. 流水推送（替代原「账本」章节）

只推 confirmed，一笔一条，带幂等键：
```json
{
  "event_id": "<txid>:<event_index>",
  "type": "deposit",
  "uid": 10001, "chain": "TRON", "symbol": "USDT",
  "amount": "100000000", "decimals": 6,
  "txid": "...", "event_index": 3,
  "block_number": 12345678, "confirmed_at": 1712345678
}
```
- 双通道：**MQ（主） + HTTP 回调（备）**，均由 outbox 驱动，指数退避（1s→…→10min，最长 24h），失败进死信 + 告警。
- 语义是「至少一次」，下游必须按 `event_id` 幂等。
- 必备补偿手段：**按时间区间分页拉全量 confirmed 流水的对账接口** + 按 `event_id` 单条查询。这是「只推流水」模式下下游自愈的唯一办法。

## 9. 归集 Sweep + 能量租赁（v0.4 重点：双平台可配置切换）

### 9.0 EnergyProvider 抽象（架构底座）

```
type EnergyProvider interface {
    Name() string
    Quote(need Energy, period) (costTRX, availableStock, error)   // 比价/库存
    Ensure(ctx, addr, need Energy, idemKey) (orderRef, error)     // 下单
    Wait(ctx, orderRef, timeout) (delegatedEnergy, error)         // 到账确认
    Balance() (prepaidTRX, error)                                 // 预付余额水位
}
```
实现：`gasstation` / `tronenergyrent` / `trx_burn`（打 TRX 让其自烧，兜底 & 测试网）/ `staking`（自质押代理，用于出金热钱包）。

**插件化注册（为你上线后再加平台预留）**：每个 Provider 自注册到 `registry[name]`，启动时按配置 `providers: [{name, enabled, weight, credentials_env, ...}]` 实例化。
**新增一家 = 新增一个文件实现 5 个方法 + 配置里加一段**，不改 sweep/withdraw 主干。为此接口层必须屏蔽掉两家已知的差异：幂等键有/无、回调有/无、状态枚举不同、最小起租不同、计价规则不同（所以 `Quote` 一律返回**平台接口给的总价**，不允许自己按单价乘）。

选路策略（配置驱动，三种模式，**默认 `cheapest`**）：
- `fixed`：固定用某一家（灰度期用）
- `priority`：主 → 备 →（都不行）`trx_burn`
- `cheapest`：**每次下单前并发比价**（两家的报价接口都便宜且快），选总成本最低者；报价缓存 30~60s 防打满对方限流

无论哪种模式，**`trx_burn` 永远是最后兜底**，且每周主动跑一次防代码腐烂。

### 9.1 GasStation 接入事实（来自其官方文档）

- 域名 `https://openapi.gasstation.ai/`，认证 `app_id` + `secret`；**请求参数整体 AES 加密**（ECB / PKCS7 / Base64 UrlSafe）放在 `data` 查询参数里；`code=0` 为成功。
- 需要用到的接口：
  | 用途 | 接口 |
  |---|---|
  | 查预付余额 & 充值地址 | `GET /api/tron/gas/balance` → `balance`、`deposit_address` |
  | 查价格/档位/库存 | `GET /api/tron/gas/order/price` → `min_number`、`max_number`、`price_builder_list[{expire_min, service_charge_type, price, remaining_number}]` |
  | 估算某笔转账所需能量 | `GET /api/tron/gas/estimate`（传 `receive_address / address_to / contract_address / service_charge_type`）→ `energy_num`、`amount` |
  | 下单租赁 | `POST /api/tron/gas/create_order`（`request_id / receive_address / buy_type / service_charge_type / energy_num|net_num`）→ `trade_no` |
  | 查订单 | `GET /api/tron/gas/record/list?request_ids=...` → `status` 及各子单 `delegate_time / reclaim_time / txid` |
  | 异步回调 | GasStation `POST` 到我方地址（`x-www-form-urlencoded`，含 `trade_no/request_id/status`），我方必须返回纯文本 `SUCCESS` |
- 订单状态：`0` 下单成功 / `1` 资源代理成功 / `2` 代理失败 / `3` 部分成功 / `10` 资源已回收。
- 时长档位：`10010`=10 分钟、`20001`=1 小时、`30001`=1 天。
- 最小购买量：**能量 64000（文档另一处写 64400）**、带宽 5000；`buy_type=1`（系统估算）只支持能量，且一单不能同时买能量和带宽。
- 关键错误码：`100006` IP 非法（→ **必须固定出口 IP 并加白**）、`110042` 能量不足（库存）、`110044` 交易已存在（→ 天然幂等）、`110034` 解密失败。
- ⚠️ **无测试环境**（文档原文：Not supported in the test environment），Nile 上跑不了。

### 9.1b TronEnergyRent 接入事实（来自其 OpenAPI 规范 `api.tronenergyrent.com/v3/api-docs.yaml`）

- 全部是 **GET + 明文 query**，认证只有 `apiKey`，比 GasStation 的 AES 简单得多。响应统一 `{status, errorCode, errorDescription, requestId, payload}`。
  | 用途 | 接口 |
  |---|---|
  | 账户余额/充值地址 | `/account-info?apiKey=` → `depositAddress`、`balanceTrx` |
  | 能量报价+库存 | `/calculate-energy-price?period=&energyAmount=` → `totalPriceTrx`、`availableEnergy`、`minimumOrderEnergy` |
  | 带宽报价 | `/calculate-bandwidth-price?period=&bandwidthAmount=` |
  | 下单 | `/place-energy-order?apiKey=&period=&energyAmount=&destinationAddress=` → `orderId`、`state` |
  | 查单 | `/single-order-details?apiKey=&orderId=` → `state`、`energyDelegatedAmount`、`transactions[].transactionHash` |
  | 批量查单 / 取消 | `/all-orders-details`、`/order-cancel` |
- 订单状态：`PAID_BY_USER`（已支付，初始）→ `WAITING_DELEGATION` → `ENERGY_DELEGATED`（成功）/ `ERROR_DELEGATION` / `CANCELLED`。
- 档位：`1h / 1d / 3d / 30d`；**能量最小 15000**（远低于 GasStation 的 64400，小额归集更友好）；带宽最小 1000。
- 可选 `preActivateDestinationAddress=1`：未激活地址代付 1.5 TRX 激活（新充值地址首次归集可能用得上）。
- 同样是**预充值**模式（`depositAddress` 充 TRX）。
- ⚠️ **两个坑**：
  1. **下单接口没有幂等键**（无 `request_id`）。重试可能重复下单、重复扣款。对策：本地 `sweep_record` 上加「单飞锁 + 已下单标记」，**先落库 `provider=tronenergyrent, state=submitting` 再调用**；网络超时不重试下单，改为用 `/all-orders-details` 按 `destinationAddress + 时间窗` 反查有无已生成订单，确认没有才允许重发。
  2. **没有回调**，只能轮询 `/single-order-details`（官方称能量 1~10 秒到账，轮询 1s/次、上限 60s 即可）。

### 9.1c 实测比价（我刚跑通它的公开报价接口 + 读取链上参数）

链上参数（TronGrid `getchainparameters` 实测）：`getEnergyFee = 100 SUN/能量`、`getTransactionFee = 1000 SUN/字节`；TRX ≈ $0.329。

| 场景 | 所需能量 | 烧 TRX | TronEnergyRent 租 1h | GasStation（按其文档示例 38 SUN 估） |
|---|---|---|---|---|
| 归集（收款方=财务地址，已有 USDT） | ~32,000 | 3.2 TRX ≈ $1.05 | **1.69 TRX ≈ $0.56** | 最小起租 64,400 → ~2.45 TRX ≈ $0.80 |
| 出金（收款方可能零 USDT 余额） | ~65,000 | 6.5 TRX ≈ $2.14 | **2.925 TRX ≈ $0.96** | ~2.45 TRX ≈ $0.81 |
| 带宽 345 字节（免费额度不足时） | — | 0.345 TRX ≈ $0.11 | 最小 1000，0.637 TRX | 最小 5000 |

结论：
- 租赁比烧 TRX 省 **50%~55%**，方向正确。
- **小额度场景 TronEnergyRent 明显更优**（最小 15000 vs 64400）；大额/首次转账两家接近。→ 这正是要做 `cheapest` 自动比价的原因，而不是固定一家。
- **带宽不要租**：345 字节只烧 0.345 TRX，比最小起租（0.637 TRX）还便宜，且每账户每天有 600 免费带宽。带宽固定走烧 TRX。
- 注意 TronEnergyRent 有隐藏规则：**订单 < 55,000 能量时额外加收 250,000 SUN（0.25 TRX）**（其 `explanation` 字段实测返回），比价时必须以接口返回的 `totalPriceTrx` 为准，不能自己按单价乘。

### 9.1e 按 200 笔/日算的出金能量成本与容量（v0.5）

实测链上参数：`TotalEnergyLimit = 1.8e11 / 日`、`TotalEnergyWeight = 1.88e10 TRX` → **1 TRX 质押 ≈ 9.6 能量/天**。
出金 200 笔/日 × 65,000 能量 = **1,300 万能量/天**。

| 方案 | 月度成本 | 占用本金 | 结论 |
|---|---|---|---|
| 烧 TRX | 200×6.5=1,300 TRX/日 ≈ **39,000 TRX/月 ≈ $12.8k** | 0 | 最贵 |
| **租赁 1h（选定）** | 200×2.925=585 TRX/日 ≈ **17,550 TRX/月 ≈ $5.8k** | 仅预付余额 | 省 55% |
| 自质押 | 边际 0 | 需质押 **≈ 136 万 TRX ≈ $45万**，且 14 天解锁期 | 本金太重，你已否决 |
| 租 1d / 30d 长期档 | 65k·1d=6.825 TRX、1天只再生一次 → 等效 6.8 TRX/笔 | — | **比 1h 贵**，不采用 |

→ 确认用 **1h 档、按需租**。但出金热钱包是**单一固定地址**，可以比归集多一层优化：

**热钱包「能量池」模式（v0.5 新增）**
```
守护协程每 30s 查一次热钱包 GetAccountResource
   若 可用能量 < 低水位(如 3 笔×65k)
        → cheapest 比价，一次性租 k×65k（k 按未来 1 小时预估出金笔数，建议 k=10），期限 1h
        → 该小时内的出金直接消耗池中能量，不再逐笔下单
```
好处：单价不变但**订单数从 200/日 降到 ~20/日**（少一堆 API 失败面）、**避开 TronEnergyRent 「<55,000 能量加收 0.25 TRX」的小单附加费**、出金延迟从「等租赁到账 1~10s」变成「几乎为 0」。
风险：租期内没用完则浪费。中和办法：按**上一小时实际出金笔数 × 1.2** 动态定 k，并监控「租入能量/实际消耗」利用率，低于 70% 就调小 k。低峰期（如凌晨）自动退回逐笔模式。

> 归集不适用能量池：它是**成千上万个不同地址**，能量必须逐地址代理，只能逐笔租。

### 9.1d `min_sweep` 盈亏平衡模型（不写死，运行时算）

```
cost_trx   = 比价得到的最优能量成本 + 带宽成本(0.345 TRX，若免费额度不足)
cost_usd   = cost_trx × TRX_USD
min_sweep  = cost_usd / target_cost_ratio      // target_cost_ratio 建议 0.5%
还要满足   min_sweep ≥ cost_usd × 安全倍数(建议 20)   // 防极端行情下归集亏本
```
用当前实测值算：`cost = 1.69 + 0.345 = 2.035 TRX ≈ $0.67` → **`min_sweep ≈ 134 USDT`**（按 0.5%）。
落地方式：定时任务（如每 10 分钟）拉一次两家报价 + 链上 `getEnergyFee` + TRX 价格，重算并写回 `coin.min_sweep`，设上下限保护（如 clamp 到 50~500 USDT），每次变更记录审计日志。
**另加一条**：低于 `min_sweep` 的余额不是永远不归集 —— 加「陈旧兜底」规则，地址余额 > 0 且超过 N 天（如 30 天）未归集则强制归集一次，避免灰尘长期沉淀在数千个地址上。

### 9.2 归集流程（主网形态）

```
地址 USDT 余额 ≥ min_sweep（或大额即时触发）
        │  取该地址分布式锁
        ▼
预估能量：只读 TriggerConstantContract（energy_used × 1.15）为准
        │  （只读预执行不校验发起地址的能量/TRX，也不返回带 expiration 的交易）
        │  （归集收款方=财务地址，**常年留一点 USDT 余额**，能把能量从 131k 压到 65k）
        ▼
Provider 选路：cheapest 模式下并发报价
   GasStation /price + /estimate   ║   TronEnergyRent /calculate-energy-price
        │  任一家预付余额不足 / 库存不足 → 排除；都不行 → trx_burn
        ▼
下单（幂等：GasStation 用 request_id；TronEnergyRent 无幂等键→先落库再调用+反查）
        ▼
等代理到账：GasStation 回调(status=1) 为主 + 轮询兜底；TronEnergyRent 只能轮询
        统一再用链上 GetAccountResource 复核实际到账能量
        下单前先记下发起地址的可用能量作为基线（baseline_energy），复核时比
        「增量 ≥ 租赁量 * 95%」而不是比绝对值：热钱包本来就有能量，比绝对值会
        在委托根本没到账时误判为已到账，正好落进「能量未及时到账」那类失败
        先落库后下单意味着超时/进程重启会留下 created 状态的订单（可能已付款），
        由 energy.Manager.RunReconcile 反查平台并给出终态，无法确认的标
        abandoned 并打 error 日志等人工与平台对账
        │  超时 60~90s 或失败/部分成功 → 降级烧 TRX
        ▼
能量到账后才 triggersmartcontract 构造未签名交易（此刻起算 expiration ≈ 60s）
        ▼
签名 → 立刻广播（feeLimit 硬上限）→ 等确认 → 写 sweep_record（provider / fee_mode / 实际能耗 / 成本）
        ▼
资源到期自动回收（两家都是到期自动 undelegate，无需我方处理）
```

### 9.3 必须做的设计取舍（这几条是本节的核心）

1. **能量供给抽象成 Provider 接口**（见 9.0），实现 `gasstation` / `tronenergyrent` / `trx_burn` / `staking`。**Nile 阶段用 `trx_burn`，主网切 `cheapest` 比价**，全部配置切换。不这么做，测试网代码到主网必返工。
2. **构造必须排在租赁之后**：节点在 `triggersmartcontract` 里写入的 `expiration` 只有约 60s，而租赁派发常要几十秒到几分钟，先构造就会等出 `TRANSACTION_EXPIRATION_ERROR`。规则：**只读预执行估能量 → 下租赁单 → 链上确认到账 → 才构造、签名并立刻广播**；租期最短档 10 分钟，广播失败按 txid 定论并在过期前原样重播同一份签名字节，超 2 分钟未上链则记录「租金浪费」指标并告警。
3. **`request_id` 用我方 `sweep_record.id`**，天然幂等；重复下单返回 `110044` 视为已存在，转查询而非新建。
4. **回调 + 轮询双保险**：只靠回调会丢（公网、我方发布重启）；只靠轮询浪费且慢。回调接口要幂等、验签/IP 白名单、返回纯文本 `SUCCESS`。
5. **带宽（Net）单独考虑**：`/estimate` 明确不含带宽。一笔 TRC20 转账约需 345 字节带宽，账户每日有免费带宽额度，通常够 1~2 笔；不够则烧约 0.3~0.4 TRX。策略：**默认让带宽烧 TRX**（金额小），仅在批量归集高峰考虑单独租 net（最小 5000）。
6. **最小购买量 > 实际所需**：USDT 转账通常约 32k 能量（收款方已有余额）/ 约 65k（首次），但最小购买 64400 → **给"已有余额的归集地址"归集时会买多**。这会抬高小额归集的单位成本，所以 `min_sweep` 要按「归集成本 / 归集金额 < X%」动态定，建议初值先按当时能量单价算一遍再定。
7. **预付余额风险**：两家都是预充值模式。必须做**余额水位告警 + 自动停用降级**（某家余额低于阈值就从比价候选里排除，都不行走 TRX 兜底，不能卡死），并配合 9.4 的自动补给。
8. **第三方风险**：GasStation 挂了/跑路 → 兜底链路必须常态可用并定期演练（每周主动跑一次 `trx_burn` 路径，防止兜底代码腐烂）。
9. **成本看板**：每笔归集记录 `fee_mode / 租金 / 烧掉的 TRX / 归集金额`，日报对比「租赁 vs 烧 TRX」实际成本，验证租赁确实更省。

### 9.4 预付余额自动补给（v0.6 新增，带开关）

目标：两家平台的预付 TRX 余额自动从**财务地址**补齐，人不在现场也不会因欠费停摆。

**配置（每家平台独立）**
```yaml
energy:
  auto_topup:
    enabled: false            # 总开关，默认关；关闭时只告警不转账
    source_address: T...      # 资金来源 = 小额 gas 账户（非财务冷钱包）
    gas_account:
      target_trx: 20000       # gas 账户常备量（≈ 2 周总消耗，上限即最大失控损失）
      low_watermark_trx: 8000 # 低于此值告警，由人工从财务冷钱包定额补（永不自动）
    providers:
      gasstation:
        enabled: true
        low_watermark_trx: 2000    # 低于此值触发
        target_trx: 6000           # 补到此值（≈ 10 天量）
        max_single_topup_trx: 4000 # 单笔上限
        max_daily_topup_trx: 8000  # 单日上限（硬限，超过则停止并告警）
        deposit_address: T...      # 白名单：必须与平台接口返回的一致，否则拒绝
```

**流程**
```
守护任务（每 5 分钟）
  → 查两家余额（GasStation /balance；TronEnergyRent /account-info）
  → 低于 low_watermark ？
        否 → 结束
        是 → enabled=false → 只告警（并在接近耗尽时把该家从比价候选中摘除）
              enabled=true  → 风控校验 → 构造 TRX 转账 → sign-service 签名 → 广播 → 追确认
  → 记 topup_record，并轮询平台余额确认入账（链上确认 ≠ 平台入账）
```

**风控（这是一条自动出金通道，必须当提现一样对待）**
1. **收款地址硬白名单**：只能转到配置里写死的 `deposit_address`；**每次补给前重新调接口拉一次平台返回的充值地址并与白名单比对，不一致则拒绝 + 告警**（防平台被入侵/接口被劫持后把钱引到黑地址）。
2. **三道金额限制**：单笔上限、单日累计上限、单日最大补给次数（如 3 次）。任一触顶 → 停止自动补给并告警，转人工。
3. **幂等 + 锁**：`topup_record` 建 `uq(provider, date, seq)`，补给任务全局单飞；上一笔未确认到账前不开新笔（否则会因平台入账延迟重复转账）。
4. **资金来源隔离（已确定）**：资金链路为
   ```
   财务冷钱包 ──人工、定额、需审批──▶ gas 账户 ──自动、限额、白名单──▶ 租赁平台充值地址
   ```
   - gas 账户是**独立 HD 地址**（与热钱包、归集地址均不复用），私钥同样在 sign-service / KMS，但**只授予向白名单地址转 TRX 这一种签名用途**（sign-service 按 `purpose=topup` 校验：只允许 TRX 转账、只允许白名单 to、金额不超单笔上限，否则拒签）。
   - **风险敏感：即使自动补给逻辑完全失控，最大损失 = gas 账户余额（≈ 2 万 TRX）。**
   - gas 账户自身低水位**只告警、不自动补**，人工从财务冷钱包充（否则等于把冷钱包又接回自动通道）。
5. **全量审计 + 日报**：每笔补给记 `who(自动/人工) / 金额 / txid / 触发时余额`，日报推送。
6. **开关变更也要审计**：`enabled` 从 false→true 归为敏感操作，记操作人与时间。

**新增表 `topup_record`**：`id, provider, to_address, amount_trx, txid, trigger_balance_trx, status(created/broadcast/confirmed/credited/failed), operator(auto/人工), created_at`。

## 10. 提现（业务系统先扣款）

```
业务系统扣款+冻结 → 调 wallet-api 下单（biz_order_no 幂等）
   → 钱包侧校验：地址合法性 / 非本平台地址 / 热钱包余额 / 单笔单日限额 / 黑名单
   → （可选）大额人工审核
   → 构造交易(设 expiration) → sign-service 签名 → 落库 signed_raw + txid
   → chain-gateway 广播（主备，重发同一份签名）
   → 追确认 → 结果经 outbox 回推业务系统（成功销账 / 失败解冻退款）
```
- **最容易出双花的地方**：同一 `biz_order_no` 只允许存在一份已签名交易；过期前只重发这份；过期且链上查无才允许重构（旧 txid 入历史）。必须写成状态机 + CAS。
- **出金热钱包能量走租赁（v0.5 定）**：采用 9.1e 的「能量池 + 批量租」，不质押。能量池补不上时逐笔租，再不行烧 TRX，**三级降级，不允许因能量卡住出金**。
- **收款方零余额会翻倍**：出金目标地址若从未持有 USDT，能量从 65k 变 131k（成本翻倍）。构造前先查对方 USDT 余额并按两档估能量，否则能量买少了交易会 `OUT_OF_ENERGY` 失败并浪费已付费用。
- 热钱包余额不足 → 挂起 + 告警，不置失败。
- 提供「按 biz_order_no 主动查状态」接口，防回调丢失。

## 11. MQ 与部署

- Topic：`wallet.deposit.confirmed` / `wallet.withdraw.result` / `wallet.sweep.finished`，key = 幂等键。
- 可靠性：**outbox + 定时补偿**（与 MySQL 事务一致，比 MQ 事务消息易维护）。
- Docker Compose（dev/Nile）：mysql / redis / rocketmq / 各服务；主网加 FullNode。
- 生产建议 K8s，`deposit-scanner` 单副本 + Lease 选主。
- **固定出口 IP**（GasStation IP 白名单 + TronGrid Key 保护）。
- 告警：扫描落后块数、节点高度差与切换次数、outbox 积压、未确认充值数、归集失败率、**两家租赁平台的预付余额 / 下单失败率 / 平均单笔能量成本 / 租金浪费笔数**、热钱包质押能量水位、gas station TRX 水位。

## 12. 对原需求的 6 点修正（已并入上文）

1. `derive_path` 不用 uid 当 index → 独立自增 `address_index`。
2. 补确认位 + 链重组回滚，只有 confirmed 才推流水。
3. 校验 `receipt.result == SUCCESS` + 合约白名单，防假充值。
4. 归集手续费：双平台租赁自动比价 + TRX 兜底 + Provider 抽象；出金热钱包自质押。
5. 出金用独立热钱包，不用财务冷钱包签名。
6. 通知走 outbox，防进程崩溃丢消息。

## 13. 安全加固

私钥：仅 sign-service 可派生；KMS/HSM；内存清零；全链路脱敏；任何 API 不返回私钥/助记词。
网络：sign-service 独立网段 + mTLS + 白名单；对外 API HMAC + 时间戳防重放 + IP 白名单；GasStation `secret` 与 TronGrid Key 入 KMS/配置中心，不进代码库。
权限：大额提现双人复核，下单人与审核人分离，全量审计。
数据：DB 加密备份；助记词离线 Shamir 分片 + 定期恢复演练。
风控（钱包侧兜底，业务侧为主）：单笔/单日限额、速率熔断、黑名单（可接 Chainalysis/TRM）。

---

## 凭据与安全（重要）

你在聊天里直接贴了 GasStation 的 `app_id/app_secret` 和 TronEnergyRent 的 `apiKey`。它们已经留在会话记录里，**建议在两个平台各轮换一次**。代码侧的处理：全部以环境变量注入（`GASSTATION_APP_ID` / `GASSTATION_APP_SECRET` / `TRONENERGYRENT_API_KEY`），**不进代码库、不进配置文件、不打日志**；两家都是预充值模式，key 泄露 = 别人可以消耗你的余额（TronEnergyRent 的 key 甚至直接出现在 URL query 里，代理/网关日志要做脱敏）。GasStation 还要把我方出口 IP 加白。

---

## 还需要你确认（1 个）

1. **Nile 阶段用 `trx_burn` 跑通全流程，两家租赁平台等主网小额灰度再验** —— 认可吗？（两家都没有测试环境）


确认后即按 v0.5 落地代码（monorepo + 多 entrypoint；先出表结构 + chain-gateway + HD 派生 + 扫描器）。

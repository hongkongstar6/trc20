# TRC20-USDT 转账流程 / 失败处理策略评估

对照仓库 `hongkongstar6/trc20` 现有实现（`internal/withdraw/worker.go`、`internal/sweep/sweep.go`、`internal/energy/*`、`internal/chain/gateway.go`）。

结论：你的 5 步流程和 4 类失败分类方向正确，**步骤顺序（先估能量 → 再租 → 再构建签名广播）在代码里已经是这样了**（PR #22 修的就是这个）。主要问题不在流程设计，而在 4 个"看起来做了、实际没兜住"的地方，以及失败原因完全没有分类。

---

## 一、流程逐步对照

### 步骤 1 校验发起方余额 —— 部分缺失

| 检查项 | 归集 sweep | 提现 withdraw |
|---|---|---|
| USDT 余额 | ✅ `balanceOf` 链上查（sweep.go:193） | ❌ **完全不查**，直接拿订单金额构建交易 |
| TRX 余额 | ❌ | ❌ |
| 可用带宽 | ❌ | ❌ |

- `GetTRXBalance` 只在 gas 账户告警里用了（topup.go:98）；`AccountResource.AvailableBandwidth()`（gateway.go:524）**定义了但没有任何调用者**。
- 影响：提现只能靠链上 REVERT 才发现热钱包 USDT 不够（白烧一次手续费，正好是你说的失败原因 3）；TRX/带宽不足则直接 `CONTRACT_VALIDATE_ERROR`。
- 建议：在 `riskCheck` 里加热钱包 USDT 余额校验（含"本轮已签未确认的挂单占用"），以及 TRX ≥ (feeLimit 或 带宽烧币额) 的下限校验，不足时告警并**暂停整条提现队列**而不是逐笔失败。

### 步骤 2 模拟交易与能量计算 —— 系数与"扣减现有能量"有偏差

- `EstimateEnergy` 用的是 `triggerconstantcontract`，与你的设计一致；但安全系数是 **1.15**（manager.go:305），不是你写的 1.05。1.05 对 USDT 偏危险（动态能量上浮 + 收款方是否首次持币），我建议保留 1.1~1.15，不要降到 1.05。
- **没有减去发起方现有可用能量**：`need` 直接当作租赁量传给 `Acquire`（sweep.go:265、worker.go:137）。提现侧 `pool.HasEnergyFor(need)` 只是"够/不够"的全有全无判断，不够时仍按全额租 → 有零头能量时会重复付费。你公式里的 `- 现有可用能量` 是对的，代码需要补。
- `energy_penalty`（动态能量上浮）字段已在 `TriggerResultDetail` 里声明（gateway.go:377）但**没有解析使用**；`triggerResult` 结构里根本没这个字段。上浮期正是 OUT_OF_ENERGY 高发期，建议读出来叠加。
- `fee_limit` 是配置死值（`sweep_server.fee_limit_sun` / `withdraw_server.fee_limit_sun`），不随估算能量 × 实时能量单价联动。能量单价上调时会**先撞 feeLimit 上限**而不是 OUT_OF_ENERGY，表现形式不同但同样失败。建议 `feeLimit = max(配置值, need × energy_price × 1.2)`。
- 带宽：三家 provider 都支持 `ResourceBandwidth`，但 sweep/withdraw **从来只申请 energy**，带宽全靠免费额度/烧 TRX。用户地址通常 0 TRX，一天内第二笔归集就可能因带宽不足失败。

### 步骤 3 能量 & 带宽准备 —— 有一个会误判"已到账"的关键 bug

- 下单前先落库（`request_id` 幂等）、超时可对账，这个设计是对的（manager.go:148-179）。
- 轮询 provider + 链上二次校验 `confirmOnChain` 也做了 —— 但它只判断**绝对值** `AvailableEnergy() >= need*95%`（manager.go:259-266），**没有和下单前的基线做差值**。热钱包能量池场景下地址本来就有能量，会在委托根本没到账时立刻返回"已到账"，然后签名广播 → 就是你担心的失败原因 4。修法：`Acquire` 前记录 baseline，`confirmOnChain` 判断 `available - baseline >= need*95%`。
- `energy.PendingOrders`（manager.go:318）注释说"供对账任务检测已付款未确认的订单"，但**全仓库没有任何调用者**。即租赁超时（`wait` 返回 timeout）后订单停留在 created，没有任何后续对账/退款/复用流程 —— 钱可能白付。这是 4 类失败里唯一"真实资金损失"的路径，优先级最高。
- 提现侧租赁失败会**降级为烧 TRX**（worker.go:139）；在没有 TRX 余额校验的前提下，这个降级恰好制造你说的失败原因 4 的最坏情况。归集侧不降级（置 failed），是对的。

### 步骤 4 构建与广播 —— 顺序正确，过期时间是估算值

- 顺序正确：估能量 → 租 → 等到账 → 构建 → 签名 → 广播，签名走独立 sign-service。
- `expired_at` 是 `now + tx_expiration_sec`（默认 60s，worker.go:161、sweep.go:298）**估算**的，没有读交易 `raw_data.Expiration` 里节点写的真实值。两者不一致时：估得早 → 提前判死一笔其实还能上链的交易（归集会重复归集，提现是 at-most-once 有 CAS 保护，风险主要在归集）；估得晚 → 无谓等待。建议直接用 `Expiration` 字段。

### 步骤 5 上链确认 —— 归集缺确认数，且有一个多余标记的 bug

- 提现有 `confirm_blocks` 确认数要求（worker.go:332），归集**没有**，`Reconcile` 一见成功回执就置 confirmed（sweep.go:353-378）。归集是内部转账、危害较小，但遇分叉会误判。
- Bug：归集确认后把 `to_address = 该地址 AND status=confirmed AND swept=false` 的**全部**充值记录标 swept（sweep.go:372）。若在"构建交易"到"确认"之间该地址又收到一笔并确认，这笔会被误标已归集 → 资金留在用户地址且再也不会被 `candidates` 选中。修法：归集时把参与本次归集的 deposit id 集合记下来，只标这些 id（或按 `id <= 归集时最大 id` 约束）。

---

## 二、失败处理策略逐条对照

**总体缺口：没有失败原因分类。** 两条路径都只是把 `receipt.Result` 或 `resMessage` 写进 `fail_reason`（worker.go:328、sweep.go:356），下游没有任何按 `OUT_OF_ENERGY / REVERT / CONTRACT_VALIDATE_ERROR` 分流的逻辑；也没有 `retry_count` 字段、没有退避、没有告警通道（只有 logrus）。你策略文档里"分类处置"这一核心原则目前**没有落到代码**。

### 1. 能量不足 OUT_OF_ENERGY / REVERT

- 现状：提现 → 直接 failed + 通知业务系统退款（USDT 未动，正确，但**不重试**）；归集 → failed，下一轮因 `swept=false` 会重新拾起，但用**同样的估算系数**重来，可能反复烧手续费，且**没有失败次数上限**。
- 建议：新增 `retry_count` + 失败原因枚举；识别 OUT_OF_ENERGY 时按 1.15 → 1.3 → 1.5 递增系数重估，达到 N 次（建议 3）后置终态并告警人工介入。你写的 1.05~1.1 建议上调。

### 2. 广播失败 / 网络超时 —— 这块实现得最好

- 已实现：广播报错**不判失败**，置 `broadcast` 状态交给对账；对账用 `GetTxInfoByID` 查链；未过期只重播**原始字节**（不重建交易）；过期且链上查不到才判失败（worker.go:311-322、sweep.go:389-406）；`DUP_TRANSACTION_ERROR` 识别为成功（gateway.go:659）。与你的策略完全一致。
- 两处可改进：
  - **永久性拒绝也在等 60 秒**：`SIGERROR`、`CONTRACT_VALIDATE_ERROR`、`TAPOS_ERROR`、`BANDWIDTH_ERROR` 这类不可能因重播成功的 code，同样被置为 `broadcast` 并每轮重签重播到过期。应按 code 立即终结。
  - 重播依赖"同一 key + 同一 raw data 重新签名必然得到同一签名"（确定性 ECDSA）。代码有 `txid` 变化就拒绝重播的防护（worker.go:356），但一旦签名实现非确定性，这条重播路径会永久失效 —— 建议直接把签名（`signature`）存库，而不是重签。

### 3. 发起方余额不足 / 黑名单 REVERT

- 已实现：收款地址黑名单查 `address_blacklist` 表（PR #21）、内部地址拦截、单笔/日累计限额（worker.go:212-258）。
- 缺口：只查**收款方**黑名单，不查发起方状态；不查发起方 USDT 余额（见步骤 1）；REVERT 的多种原因（余额不足 / USDT 官方冻结）写进同一个 `fail_reason` 字符串，无法按你策略里"弹出具体提示 + 告警并终止重试"分流。
- 建议：解析 `resMessage`/revert reason 做归一化分类落库一个 `fail_code` 字段，业务通知事件里带上这个 code。

### 4. 能量租赁失败 / 未及时到账

- 已实现：轮询 provider + 链上 `AccountResource` 校验后才签名广播 —— 方向对。
- 但两个洞让它形同虚设：① `confirmOnChain` 无基线差值（见步骤 3），有存量能量时会误判到账；② 租赁超时后订单无对账（`PendingOrders` 无人调用），已付款的钱既不复用也不退。
- 另：提现降级烧 TRX 时没有 TRX 下限校验，正是你描述的"TRX 不够直接失败"。

---

## 三、按优先级建议的改动

1. `confirmOnChain` 改为基线差值判断（否则第 3 步的校验是假的）。
2. 接上 `energy.PendingOrders` 对账任务：超时/未确认的租赁订单要么复用要么标损失并告警（唯一真实资金损失路径）。
3. 归集确认时按 deposit id 精确标 swept，不要按地址全量标（会漏归集）。
4. 新增 `fail_code` + `retry_count`：OUT_OF_ENERGY 递增系数重试并设上限；永久性广播拒绝立即终结。
5. 提现前补 USDT / TRX 余额校验，不足则暂停队列并告警；租赁失败降级烧 TRX 前必须校验 TRX。
6. 能量租赁量减去现有可用能量；`feeLimit` 与实时能量单价联动；解析 `energy_penalty`。
7. 归集加确认数要求；`expired_at` 用交易 `raw_data.Expiration` 真实值。
8. 带宽：至少校验 `AvailableBandwidth()`，不足时按 `ResourceBandwidth` 下单（provider 已支持）。

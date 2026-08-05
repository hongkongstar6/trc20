# trc20

Custodial USDT-TRC20 wallet gateway: address generation, deposit scanning and
confirmation, event delivery, withdrawal signing and broadcasting, sweeping to
the finance wallet, and energy management.

The gateway deliberately **does not keep user balances**. It emits an
append-only stream of confirmed events; the business system owns the ledger,
the freezes, the debits and the refunds. See
[docs/DESIGN.md](docs/DESIGN.md) for the full design and the
reasoning behind each decision.

## Services

One binary per process, all built from the same image:

| Binary          | Role |
| --------------- | ---- |
| `cmd/api`       | business-facing HTTP API: address allocation, withdrawal submission, reconciliation queries |
| `cmd/scanner`   | block scanning, TRC20 `Transfer` parsing, confirmations, reorg rollback, outbox enqueue |
| `cmd/withdraw`  | withdrawal state machine: build, sign, broadcast, confirm, report back |
| `cmd/sweep`     | sweeps user deposit addresses into the finance wallet, rents the energy for it |
| `cmd/sign`      | the only process that touches key material |

`internal/chain` is the node gateway: a priority ordered list of nodes
(self-hosted FullNode and/or TronGrid) with query failover, and a broadcast path
that sends the *same signed bytes* to the fallback node instead of rebuilding
the transaction.

## Quick start (Nile)

```bash
cp .env.example .env      # fill in the mnemonic, HMAC secrets and addresses
docker compose up -d mysql redis
docker compose up --build api scanner withdraw sweep sign
```

`docker compose` applies `deploy/migrations/0001_init.sql` on first start of the
MySQL container. For an existing database, apply the file by hand, or run
`make migrate` to use GORM `AutoMigrate` (development only: production should
apply reviewed SQL so index changes stay visible).

Without Docker:

```bash
make build
CONFIG_PATH=configs/config.nile.yaml ./bin/sign
CONFIG_PATH=configs/config.nile.yaml ./bin/api
```

## Configuration

`configs/config.nile.yaml` is the testnet default;
`configs/config.mainnet.example.yaml` documents the mainnet deltas. Every secret
is a `${ENV}` or `${ENV:-default}` reference, so no credential is ever committed.
Required environment variables are listed in `.env.example`.

Two properties of the Nile config are intentional:

* `energy.mode: fixed` with `energy.fixed: trx_burn`, and both rental providers
  disabled. Neither GasStation nor TronEnergyRent has a test environment, so on
  Nile they cannot be exercised at all; they are validated with small amounts on
  mainnet.
* `energy.auto_topup.enabled: false`. When disabled, a low prepaid balance only
  raises an alert.

## Energy

Sweeps and withdrawals both rent energy. Providers are plugins behind
`internal/energy.Provider`; adding a platform means one implementation plus a
config block, with no change to the sweep or withdrawal logic.

* `mode: cheapest` quotes every enabled provider and picks the cheapest for the
  actual energy requirement, including each platform's minimum order.
* A rental failure or timeout falls back to burning TRX, so a platform outage
  cannot stall payouts.
* `min_sweep` is derived at runtime from live quotes and the on-chain
  `getEnergyFee`, never hard coded: a sweep that costs more than it collects is
  not worth broadcasting.
* The hot wallet uses an energy pool: at a low watermark it rents a batch
  covering roughly an hour of withdrawals, which turns ~200 daily orders into
  ~20 and removes the per-withdrawal rental latency.

Automatic prepaid refills are funded by a dedicated small gas account, never by
the finance cold wallet. Each refill re-reads the deposit address from the
provider API and compares it against the hard-coded whitelist, is bounded by a
per-transfer, per-day and per-count limit, and never starts while a previous
refill has not been credited.

## Keys

`cmd/sign` is the only process with the seed. Everything else holds derivation
paths, which are useless without it. Addresses are allocated from a monotonic
index allocator; the uid is never used as a derivation index.

sign-service enforces policy per purpose before signing, and the declared
intent is checked against the calldata that will actually execute:

* `withdraw` — must originate from the hot wallet, allowlisted token contract
* `sweep` — must pay the finance wallet, allowlisted token contract
* `topup` — must originate from the gas account, must be a plain TRX transfer to
  a whitelisted provider deposit address, below the per-transfer cap

For production, back the key with KMS/HSM/Vault Transit instead of a mnemonic in
the environment, and put sign-service behind mTLS on its own network segment.

## Event delivery

State changes and their outbox rows are written in one database transaction, so
an event cannot be lost by a crash between the two. Delivery is at-least-once
over HTTP callback and/or RocketMQ, with retry and a dead-letter state. Event ids
are stable — a deposit event is `<txid>:<event_index>` — so the business system
deduplicates by id. `/v1/deposits` and `/v1/events` allow replaying a single
event or a time range for reconciliation.

## Development

```bash
make fmt vet test build
```

## Status

Unit tests cover address encoding, HD derivation, transaction signing, node
failover and broadcast semantics, `Transfer` log validation, signing policy,
provider quoting/ordering/polling against recorded API shapes, config loading,
and outbox signing.

Not yet verified: an end-to-end run on Nile against a live node and database,
and the mainnet grayscale validation of GasStation and TronEnergyRent.

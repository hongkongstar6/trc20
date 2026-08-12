-- ----------------------------
-- A merchant is opened for one token on one chain. An address request that
-- carries anything else is refused instead of being served an address the
-- merchant does not settle on.
-- Existing rows are USDT on TRON, which is all this gateway handled so far.
-- ----------------------------
ALTER TABLE `merchant`
  ADD COLUMN `symbol` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `callback_url`,
  ADD COLUMN `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `symbol`;

UPDATE `merchant` SET `symbol` = 'USDT' WHERE `symbol` = '';
UPDATE `merchant` SET `chain` = 'TRON' WHERE `chain` = '';

-- ----------------------------
-- One address holds one balance per token, and each of them crosses the sweep
-- threshold on its own, so the skip counter is per address and contract.
-- ----------------------------
ALTER TABLE `sweep_skip`
  ADD COLUMN `contract` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `address`,
  DROP PRIMARY KEY,
  ADD PRIMARY KEY (`address`, `contract`) USING BTREE;

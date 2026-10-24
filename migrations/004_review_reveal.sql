-- 004_review_reveal.sql — 裁定①双向往评「同时揭盲」的库侧落地
-- 依赖：schema.sql 已含 review 表（新库由 schema.sql 直接建出, 本文件对它是 no-op）
-- 幂等：用 information_schema 判断后再决定要不要 ALTER, 可重复执行
-- 日期：2026-10-24
--
-- 为什么必须是真幂等而不是"重复报 1060 属预期":
-- db_reset.py 每跑一次都会先 schema.sql 再跑本文件。schema.sql 现在已经带上了
-- 这两样, 如果这里写裸 ALTER, 每次重置都会 1060 并让 db_reset 直接 exit 1,
-- 导致 --demo 的合规门控预置也一起没执行——用错误码当噪音, 迟早盖住真错误。
--
-- 为什么需要 revealed_at：
-- 互评最大的现实问题是报复性评价。谁先写谁先暴露——先写的人把把柄交出去，
-- 后写的人可以照着报复。这会让所有人拖到最后一天才写，或者只写好话。
-- 解法是同时揭盲：双方都提交后才互相可见。单方提交满 7 天自动可见（派生态
-- 计算，不新增调度器），因为一直不写等于赖账，不能让对方永远看不到分。
--
-- 为什么需要 uk_order_reviewer：
-- 一个人对同一单只能评一次。没有这个唯一键，重复提交会插出第二条，
-- 而 AvgRating 是直接对全部记录求均值的——重复评一次就等于给自己刷了一次分。

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'review' AND COLUMN_NAME = 'revealed_at');
SET @ddl = IF(@has_col = 0,
  'ALTER TABLE `review` ADD COLUMN `revealed_at` BIGINT NOT NULL DEFAULT 0',
  'DO 0');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_idx = (SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'review' AND INDEX_NAME = 'uk_order_reviewer');
SET @ddl = IF(@has_idx = 0,
  'ALTER TABLE `review` ADD UNIQUE KEY `uk_order_reviewer` (`order_id`, `reviewer_id`)',
  'DO 0');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

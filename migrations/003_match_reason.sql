-- 003_match_reason.sql — M2 增量迁移：推荐理由表
-- 依赖：先执行 001_init（migrations/schema.sql）；schema.sql 已同步含本表，本文件只给存量库补
-- 幂等：CREATE TABLE IF NOT EXISTS，可重复执行
-- 执行：mysql -h<host> -P<port> -u<user> -p -D<db> < migrations/003_match_reason.sql
-- 日期：2026-10-22
--
-- 为什么理由要落库而不是算完就丢：
-- 非功能需求要求"给定任一已完成订单，能查出当时的匹配理由（含因子分）"。
-- 匹配分随时会变（对方接了新单、信用分涨了、技能标签改了），
-- 事后重算得到的不是"当时为什么推荐他"，而是"现在他值多少分"——这两件事不能混。

CREATE TABLE IF NOT EXISTS `match_reason` (
  `id`           VARCHAR(32)  NOT NULL,
  `match_id`     VARCHAR(32)  NOT NULL,
  `rank_no`      TINYINT      NOT NULL DEFAULT 0,
  `text`         VARCHAR(200) NOT NULL DEFAULT '',
  `anchor_type`  VARCHAR(16)  NOT NULL DEFAULT '',
  `anchor_ref`   VARCHAR(64)  NOT NULL DEFAULT '',
  `anchor_label` VARCHAR(120) NOT NULL DEFAULT '',
  `factor`       VARCHAR(16)  NOT NULL DEFAULT '',
  `factor_score` INT          NOT NULL DEFAULT 0,
  `created_at`   BIGINT       NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  KEY `idx_match` (`match_id`, `rank_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

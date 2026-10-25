-- 005: 邀请链接命中表。
--
-- 这张表只有一个用途: 回答「发出去的单, 有人点进来吗」。
-- 口径是「发单后 24 小时内是否至少被打开一次」, 所以存的是时间戳而不是计数器——
-- 计数器只能告诉你被点了 3 次, 告诉不了你第 4 天才开始有人点。
--
-- 为什么要有这个数: 第一阶段佣金恒 0, "谁付钱"问不出答案, 唯一能问的是
-- 「这条需求有没有从发单方传到接单方」。传不过去, 后面所有转化都是 0,
-- 而只看成交数分不清是"没人要"还是"没人知道"。
--
-- 用 CREATE TABLE IF NOT EXISTS 而不是裸 CREATE: schema.sql 里已经有这张表,
-- 裸 CREATE 会在 db_reset 时 1050 报错, 连带 --demo 的合规四门预置一起失败。
CREATE TABLE IF NOT EXISTS `invite_hit` (
  `id`        VARCHAR(32) NOT NULL,
  `task_id`   VARCHAR(32) NOT NULL,
  `opened_at` BIGINT      NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  KEY `idx_task` (`task_id`, `opened_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

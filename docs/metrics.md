# zanbuddy 指标口径

> 签发：张一鸣（负责人）｜2026-10-24
> **这个文件的作用是禁止换算法。** 口径一变，团队就开始管理预期；口径写死，谁都能查，争论才有共同语言。
> 数据库：`192.168.3.18:3306/zanbuddy`（唯一真源）。

---

## 一、唯一 KPI：成交单数

**定义**：走完六步、`order.status = 'completed'` 的订单数，排除种子单。

```sql
SELECT COUNT(*) AS gmv_orders
FROM `order`
WHERE status = 'completed'
  AND task_id NOT IN ('T-DEMO-001','T-DEMO-002','T-DEMO-003');
```

**排除种子的理由**：`T-DEMO-*` 是启动时预置的演示需求，每张能被认领一次。把它算进成交，等于自己给自己刷单。测试用例会反复重置库，不排除的话这个数字完全不可信。

**为什么不用「发单数」当 KPI**：发单不要钱，可以发一百条假需求。成交要走完六步，每一步对方都得真动手。

---

## 二、发单数

```sql
SELECT COUNT(*) AS published_tasks
FROM task
WHERE deleted_at IS NULL
  AND id NOT IN ('T-DEMO-001','T-DEMO-002','T-DEMO-003');
```

**注意**：`task.status='withdrawn'` 也计入。撤单率是另算的指标——「发出去的单有没有人要」，撤掉的单本身就回答了这个问题。

---

## 三、有互评的成交单占比

```sql
SELECT
  (SELECT COUNT(DISTINCT ref_id) FROM credit_event WHERE kind = 'review_received') AS reviewed_orders,
  (SELECT COUNT(*) FROM `order`
     WHERE status = 'completed'
       AND task_id NOT IN ('T-DEMO-001','T-DEMO-002','T-DEMO-003'))            AS completed_orders;
```

**现状**：分子恒为 0。`review_received` 要等第 2 周任务 F（评分揭盲）落地才会写。

**这个数为什么重要**：它衡量「成交之后，双方愿不愿意留下痕迹」。飞轮要转，这一步不能是 0——0 意味着每单都是陌生人一次性交易，评分永远不沉淀，匹配也就永远不会变准。

---

## 四、辅助口径（只在需要解释异常时查）

### 4.1 信用留痕覆盖（验证飞轮是否通电）

```sql
SELECT kind, COUNT(*) FROM credit_event GROUP BY kind;
```

预期：`order_completed` 条数 = 成交单数 × 2（需求方 + 接单方各一条）。**不等就说明有验收路径绕过了 `ReviewOrder`**，去查是不是有人在 handler 里另开了一条「完成订单」的路。

### 4.2 卡在哪一步（这单为什么没成交）

```sql
SELECT status, COUNT(*) FROM `order` GROUP BY status;
```

`delivered` 堆积 = 需求方不回来看（验收这一步太重）。
`pending_escrow` 堆积 = 合规闸门没过。
`disputed` = 出事了，逐单看。

---

## 五、执行约定

| 约定 | 内容 |
|---|---|
| 频率 | 每天一个数：成交单数。不报进度、不写日报 |
| 评审 | 每周 30 分钟，只看 4.1 的三个数（发单数 / 成交单数 / 有互评占比） |
| 变更 | 改口径必须写进本文件并注明日期与理由，不许口头改 |
| 禁止 | 不用注册数、发单数、页面浏览量代替成交单数——它们都不会让别人真的付钱 |

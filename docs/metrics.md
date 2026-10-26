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

**分子**（按订单数，不按评价条数）：

```sql
SELECT COUNT(DISTINCT r.order_id) AS reviewed_orders
FROM credit_event e
JOIN review r ON r.id = e.ref_id
WHERE e.kind = 'review_received';
```

**分母**：

```sql
SELECT COUNT(*) FROM `order`
WHERE status = 'completed'
  AND task_id NOT IN ('T-DEMO-001','T-DEMO-002','T-DEMO-003');
```

**为什么分子要连 `review` 表**：`review_received` 的 `ref_id` 存的是**评价 id**（`RV-*`），
一张成交单最多产生两条（需求方评一条、接单方评一条）。直接 `COUNT(DISTINCT ref_id)`
会把同一单算两次，占比凭空翻倍。**第一版口径就是这么写错的，已改。**

**现状（2026-10-24）**：分子已可产生。`POST /orders/{id}/reviews` 落地后，
双方都提交（或单方提交满 7 天）即写一条 `review_received`。

**`delta` 恒为 0 是有意的**：它不代表"这条评价没影响"。分数由 `AvgRating`
（已揭盲评价的均值）派生，`credit_event` 只负责回答"他这个分是从哪条评价来的"。
看到 delta 全是 0 不要当 bug 修。

**这个数为什么重要**：它衡量「成交之后，双方愿不愿意留下痕迹」。飞轮要转，这一步不能是 0——0 意味着每单都是陌生人一次性交易，评分永远不沉淀，匹配也就永远不会变准。

---

## 四、辅助口径（只在需要解释异常时查）

### 4.1 信用留痕覆盖（验证飞轮是否通电）

```sql
SELECT kind, COUNT(*) FROM credit_event GROUP BY kind;
```

预期：`order_completed` 条数 = 成交单数 × 2（需求方 + 接单方各一条）。**不等就说明有验收路径绕过了 `ReviewOrder`**，去查是不是有人在 handler 里另开了一条「完成订单」的路。

`review_received` 条数 ≤ `order_completed` 条数（每次互评揭盲只写一条，且可能有人一直不评）。

### 4.2 卡在哪一步（这单为什么没成交）

```sql
SELECT status, COUNT(*) FROM `order` GROUP BY status;
```

`delivered` 堆积 = 需求方不回来看（验收这一步太重）。
`pending_escrow` 堆积 = 合规闸门没过。
`disputed` = 出事了，逐单看。

### 4.3 揭盲状态（评价有没有被压着）

```sql
SELECT
  SUM(revealed_at > 0)                AS revealed,
  SUM(revealed_at = 0)                AS waiting,
  SUM(revealed_at = 0 AND created_at <= ?) AS overdue_auto
FROM review;
```

`waiting` 长期不降 = 一方提交后另一方拖着不评。这类单在 7 天后会被自动揭盲（派生态，不落库），
所以 `overdue_auto` 大于 0 是正常的，它说明兜底生效了。

### 4.4 传到了没有（邀请链接 24 小时内被打开的比例）· 2026-10-25 新增

**为什么需要它**：唯一 KPI 是成交单数，但成交单数为 0 时分不清两种情况——
**没人要**（需求本身不成立）和**没人知道**（单子发出去但没送到能接的人手上）。
这两种情况的下一步动作完全相反：前者改产品，后者改触达。只看成交数是看不出来的。

口径：分母 = 这个人的全部单；分子 = 其中「首次打开时刻 − 发单时刻 ≤ 24 小时」的单；
**没满 24 小时且没人打开的单不进分母**（算进去会系统性压低比例，看起来像没人感兴趣）。

```sql
-- 分母（已到期的单）
SELECT COUNT(*) AS decidable
FROM task t
WHERE t.requester_id = ?
  AND t.created_at <= ? - 86400000;          -- now - 24h

-- 分子（24h 内至少被打开过一次的单）
SELECT COUNT(*) AS opened_in_time
FROM task t
WHERE t.requester_id = ?
  AND EXISTS (
    SELECT 1 FROM invite_hit h
    WHERE h.task_id = t.id
      AND h.opened_at >= t.created_at
      AND h.opened_at <= t.created_at + 86400000
  );
```

接口版：`GET /api/v2/me/invite-stats` → `{window_hours, published, opened_in_time, too_early, ratio}`。

**怎么读这个数**：
- `ratio` 低（< 1/3）→ 话术没人愿意发，或者发给了错的人。先看话术本身，不要先改产品。
- `ratio` 高但成交仍为 0 → 传到了，但对方看了不接。问题在需求或价格，这时候才该动产品。
- `too_early` 长期 > 0 而 `ratio` 不动 → 单子刚发出去，还没有结论，别急着下判断。

**不记 IP、不记 UA、不去重**：现在只需要知道「点了没」。同一个人点开三次不是噪音，
那正是他在犹豫。多记一项就多一份隐私债，而这一单成不成立都还不知道。

**这个数必须长在发单方眼前（2026-10-26 补）**：`GET /api/v2/me/tasks` 的每条需求带
`open_count`，在「我的单」卡片上显示成 `挂出 3 小时 · 还没人接 · 链接被点开 2 次`。
只存在接口里的数不是指标，是日志——发单方看不见，就不会因为「没人点开」而去改话术或换人，
这个数也就永远只是我在 curl 里给自己看的东西。

**卡片上的 `open_count` 与这里的 `ratio` 是两个问题，别混**：
- `open_count` = 这条链接被点开过几次（事实，不分时间窗）；
- `ratio` = 24 小时内首次打开的比例（口径，要时间窗才有分辨力）。

两处各说各的事，标签也不同。**同名的东西有两个含义才会出事**，不同名就没事。

---

## 五、执行约定

| 约定 | 内容 |
|---|---|
| 频率 | 每天一个数：成交单数。不报进度、不写日报 |
| 评审 | 每周 30 分钟，只看 4.1 的三个数（发单数 / 成交单数 / 有互评占比） |
| 变更 | 改口径必须写进本文件并注明日期与理由，不许口头改 |
| 禁止 | 不用注册数、发单数、页面浏览量代替成交单数——它们都不会让别人真的付钱 |

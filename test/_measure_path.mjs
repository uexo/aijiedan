/**
 * 六步路径实测（任务 B / 任务 C 的共用证据）。
 *
 * 目的不是"验证功能能用"——那是 test_m0_*.py 的事。
 * 这里只回答两个问题:
 *   1) 从落地页到一发单成功, 需求方到底动了几次手? (任务 C)
 *   2) 走完一整单, 每一方各动几次手? 哪一步可以省掉? (任务 B)
 *
 * 所有数字都是真浏览器点出来的, 不是数代码行数估的。
 * 只读 + 只走公开接口, 不改任何业务代码。
 *
 * 跑法(服务需已起来):
 *   BASE=http://127.0.0.1:8083 node test/_measure_path.mjs
 */
const BASE = process.env.BASE || 'http://127.0.0.1:8083';
const { chromium } = await import(
  'file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs'
);

const browser = await chromium.launch({
  executablePath:
    'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});

// 计数口径: 只数"用户必须主动做的动作"——点击、输入、选择。
// 页面跳转本身不算一次动作(用户没动手), 但单独统计, 因为跳转会丢上下文。
const tally = { clicks: 0, fills: 0, jumps: 0 };

function mkCounter(name) {
  return { name, clicks: 0, fills: 0, jumps: 0, actions: [], jumpList: [] };
}

function wrapPage(page, c) {
  page.on('framenavigated', (f) => {
    if (f !== page.mainFrame()) return;
    const u = f.url();
    if (!u || u === 'about:blank') return;
    c.jumps++;
    c.jumpList.push(u.replace(BASE, ''));
  });
  return page;
}

/** 计一次点击 + 一次动作记录 */
async function tap(page, c, selector, label) {
  await page.waitForSelector(selector, { state: 'visible', timeout: 10000 });
  await page.click(selector);
  c.clicks++;
  c.actions.push(`click  ${label}`);
}

/** 计一次输入（填表也是用户动作） */
async function type(page, c, selector, value, label) {
  await page.fill(selector, value);
  c.fills++;
  c.actions.push(`input  ${label}`);
}

async function login(ctx, role, phone) {
  const page = await ctx.newPage();
  const c = mkCounter(role);
  wrapPage(page, c);

  // 两侧走同一个入口: 落地页 → CTA(?role=) → 登录页。
  // 直接开 login.html 会少算一次 CTA 点击, 两边就不对称了。
  const want = role === 'requester' ? 'demand' : 'supply';
  await page.goto(BASE + '/static/landing.html', { waitUntil: 'networkidle' });
  c.jumps = 0;
  c.jumpList = [];
  await tap(page, c, `a[href="app/login.html?role=${want}"]`, `落地页 CTA(${role})`);
  await page.waitForLoadState('networkidle');

  // 手机号 → 下一步
  await type(page, c, '#phone', phone, '手机号');
  await tap(page, c, '#to-role', '下一步(手机号→身份)');

  // 身份 → 下一步（带 ?role= 时已预选, 省掉这次点击）
  await page.waitForSelector('[data-pane="2"]:not([hidden])');
  const preselected = await page.evaluate(() => {
    const b = document.getElementById('to-code');
    return !!b && !b.disabled;
  });
  if (!preselected) {
    await tap(page, c, `.pick[data-role="${want}"]`, `身份卡:${role}`);
  } else {
    c.actions.push('skip  身份卡已由 CTA 参数预选');
  }
  await tap(page, c, '#to-code', '下一步(身份→验证码)');

  // 验证码 → 提交
  await page.waitForSelector('[data-pane="3"]:not([hidden])');
  await type(page, c, '#code', '000000', '验证码');
  await Promise.all([
    page.waitForURL(/(hall|profile)\.html/, { timeout: 15000 }),
    page.click('#submit'),
  ]);
  c.clicks++;
  c.actions.push('click  登录');
  await page.waitForLoadState('networkidle');

  return { page, c };
}

const rand = () => '139' + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');

// ---------------------------------------------------------------- 需求方
const ctxA = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const pageA = await ctxA.newPage();
const A = mkCounter('requester');
wrapPage(pageA, A);

// 起点: 落地页
await pageA.goto(BASE + '/static/landing.html', { waitUntil: 'networkidle' });
A.jumps = 0;
A.jumpList = [];
A.actions.push('open   落地页(起点, 不计)');

// 落地页 CTA → 登录页（必须点带 ?role=demand 的那个, 否则身份不会被预选）
const CTA = 'a[href="app/login.html?role=demand"]';
const cta = await pageA.$(CTA);
if (!cta) throw new Error('落地页找不到带 ?role=demand 的「我是需求方」CTA');
await pageA.click(CTA);
A.clicks++;
A.actions.push('click  落地页 CTA「我是需求方」');
await pageA.waitForLoadState('networkidle');

// 预选是否生效由下面的「下一步按钮是否可用」判断
await type(pageA, A, '#phone', rand(), '手机号');
await tap(pageA, A, '#to-role', '下一步(手机号→身份)');
await pageA.waitForSelector('[data-pane="2"]:not([hidden])');

// 若登录页已预选身份, 下一步按钮会直接可用; 用它判断, 别去猜 class/aria 名。
const rolePreselected = await pageA.evaluate(() => !document.getElementById('to-code').disabled);
const needRoleClick = !rolePreselected;
if (needRoleClick) {
  await tap(pageA, A, '.pick[data-role="demand"]', '身份卡:需求方(未被预选)');
} else {
  A.actions.push('skip  身份卡已被落地页参数预选, 省掉一次点击');
}
await tap(pageA, A, '#to-code', '下一步(身份→验证码)');
await pageA.waitForSelector('[data-pane="3"]:not([hidden])');
await type(pageA, A, '#code', '000000', '验证码');
await Promise.all([
  pageA.waitForURL(/(hall|profile)\.html/, { timeout: 15000 }),
  pageA.click('#submit'),
]);
A.clicks++;
A.actions.push('click  登录');
await pageA.waitForLoadState('networkidle');

const landedOn = pageA.url().replace(BASE, '');

// 大厅 → 发单页
await tap(pageA, A, '#nav-publish', '「发一条需求」');

// 填表（必填 4 项 + 选类目）
await pageA.waitForFunction(() => {
  const s = document.getElementById('category');
  return s && s.options.length > 1;
}, null, { timeout: 10000 });
await type(pageA, A, '#title', '把客服工单自动分类并派给对应的人', '标题');
await pageA.selectOption('#category', { index: 1 });
A.clicks++;
A.actions.push('select 类目');
await type(pageA, A, '#budget', '800', '预算');
await type(
  pageA,
  A,
  '#spec',
  '现在客服每天手动看几百条工单再转给对应小组，想要一个能读工单内容、判断类型、按规则派给对应组的自动化流程，最好能给出判断理由。',
  '需求说明'
);
await type(pageA, A, '#deliverables', '可运行脚本 + 使用说明', '交付物');

// 提交
await pageA.click('#submit');
A.clicks++;
A.actions.push('click  「发布需求」');

// 发布成功不自动跳转, 而是就地展开一块"已发布"卡片, 用户还得自己点一下去大厅。
// 这一步是真实存在的额外动作, 不能算成"系统自己跳的"。
let publishOk = false;
try {
  await pageA.waitForSelector('#done:not([hidden])', { state: 'visible', timeout: 10000 });
  publishOk = true;
} catch {
  publishOk = false;
}
A.actions.push('land   发布成功 → 停留在发单页(未自动跳转)');
await tap(pageA, A, '#done-go', '「去大厅看这一单」');
await pageA.waitForLoadState('networkidle');

// ---------------------------------------------------------------- 接单方
const ctxB = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const { page: pageB, c: B } = await login(ctxB, 'provider', rand());
const providerLanding = pageB.url().replace(BASE, '');
B.actions.unshift(`land   登录后落点: ${providerLanding}`);

// 新接单方必须先建档, 否则大厅里连"认领"按钮都不出现(canClaim 要求 onboarded)。
if (!/profile\.html/.test(pageB.url())) {
  await pageB.goto(BASE + '/static/app/profile.html', { waitUntil: 'networkidle' });
  B.actions.unshift('jump   落点不是档案页, 自己去导航栏找(多一跳)');
}
{
  await pageB.waitForFunction(
    () => {
      const b = document.getElementById('save');
      return !!b && !b.disabled;
    },
    null,
    { timeout: 8000 }
  );
  await type(pageB, B, '#name', '算法老王', '姓名');
  await type(pageB, B, '#headline', '把重复流程做成能跑的 Agent', '一句话定位');
  await type(pageB, B, '#bio', '做过 7 个客服工单类 Agent，交付前先给一份能跑的最小版本。', '补充说明');
  await pageB.click('.chip[data-skill="python"]');
  B.clicks++;
  B.actions.push('click  技能:python');
  await pageB.click('.chip[data-skill="rag"]');
  B.clicks++;
  B.actions.push('click  技能:rag');
  await pageB.click('.chip[data-skill="automation"]');
  B.clicks++;
  B.actions.push('click  技能:automation');
  await pageB.click('#save');
  B.clicks++;
  B.actions.push('click  保存档案');
  await pageB.waitForURL(/hall\.html/, { timeout: 10000 });
  await pageB.waitForLoadState('networkidle');
}

// 认领。必须认领 A 刚发的那一条, 不能随手认领种子单——
// 否则后面需求方看不到待确认的单, 整条链路量不下去。
const TITLE = '把客服工单自动分类并派给对应的人';
await pageB.waitForSelector('[data-claim]', { state: 'visible', timeout: 10000 });
const myClaim = await pageB.evaluate((title) => {
  const cards = [...document.querySelectorAll('li, article, .card')];
  for (const c of cards) {
    if (!c.textContent.includes(title)) continue;
    const btn = c.querySelector('[data-claim]');
    if (btn) return btn.getAttribute('data-claim');
  }
  return null;
}, TITLE);
if (!myClaim) throw new Error('接单方在大厅里找不到刚发的这条需求');
await pageB.click(`[data-claim="${myClaim}"]`);
B.clicks++;
B.actions.push('click  「认领这一单」(目标单)');
await tap(pageB, B, `[data-confirm="${myClaim}"] [data-do]`, '确认接单');

// ---------------------------------------------------------------- 需求方确认
await pageA.reload({ waitUntil: 'networkidle' });
try {
  await pageA.waitForSelector('[data-ask-confirm]', { state: 'visible', timeout: 10000 });
} catch (e) {
  const diag = await pageA.evaluate(() => ({
    url: location.href,
    body: (document.body ? document.body.textContent : '').replace(/\s+/g, ' ').slice(0, 160),
    hasMine: !!document.getElementById('mine'),
    hasList: !!document.getElementById('mine-list'),
    html: ((document.getElementById('mine-list') || {}).innerHTML || '').slice(0, 300),
  }));
  console.log('\n[诊断] 需求方看不到「确认开工」\n', JSON.stringify(diag, null, 2).slice(0, 1800));
  throw e;
}
await tap(pageA, A, '[data-ask-confirm]', '「确认开工」');
await tap(pageA, A, '[data-do-confirm]', '确认开工(二次确认)');

// ---------------------------------------------------------------- 接单方交付
await pageB.reload({ waitUntil: 'networkidle' });
await pageB.waitForSelector('[data-ask-deliver]', { state: 'visible', timeout: 10000 });
await tap(pageB, B, '[data-ask-deliver]', '「提交交付」');
await pageB.waitForSelector('[data-box^="deliver-"] textarea, [data-box^="deliver-"] input', { timeout: 8000 });
const anyField = await pageB.$('[data-box^="deliver-"] textarea, [data-box^="deliver-"] input');
if (anyField) {
  await anyField.fill('脚本已跑通三类工单，附使用说明。');
  B.fills++;
  B.actions.push('input  交付说明');
}
await tap(pageB, B, '[data-do-deliver]', '确认交付');

// ---------------------------------------------------------------- 需求方验收
await pageA.reload({ waitUntil: 'networkidle' });
await pageA.waitForSelector('[data-ask-accept]', { state: 'visible', timeout: 10000 });
await tap(pageA, A, '[data-ask-accept]', '「验收通过」');
await tap(pageA, A, '[data-do-accept]', '确认通过(二次确认)');

// ---------------------------------------------------------------- 汇总
// 终态校验: 光"每一下都点成功了"不等于"这单真的结了"。
// 点击只是派发事件, 请求还在飞, 所以这里轮询等, 别读一次就下结论。
let finalOrders = [];
let doneOrder = null;
for (let i = 0; i < 20; i++) {
  finalOrders = await pageA.evaluate(async () => {
    const r = await fetch('/api/v2/me/orders', { credentials: 'same-origin' });
    const j = await r.json();
    return ((j && j.data && j.data.items) || []).map((o) => ({
      id: o.id,
      role: o.role,
      state: o.state,
      status: o.status,
    }));
  });
  doneOrder = finalOrders.find(
    (o) => o.role === 'requester' && (o.state === 'done' || o.status === 'completed')
  );
  if (doneOrder) break;
  await pageA.waitForTimeout(400);
}

const rows = [
  ['需求方', A.clicks, A.fills, A.jumps],
  ['接单方', B.clicks, B.fills, B.jumps],
];
const totalClicks = A.clicks + B.clicks;
const totalFills = A.fills + B.fills;

console.log('\n=== 六步路径实测（真浏览器）===\n');
console.log(`发布是否成功: ${publishOk ? '是' : '否'}`);
console.log(`落地页 -> 登录后落点: ${landedOn}`);
console.log(`登录页是否预选身份: ${JSON.stringify(rolePreselected)}`);
console.log(`身份卡是否还需再点一次: ${needRoleClick}`);
console.log('');
console.log('角色    点击  输入  跳转');
for (const [n, a, b, c] of rows) console.log(`${n}  ${String(a).padStart(4)}  ${String(b).padStart(4)}  ${String(c).padStart(4)}`);
console.log(`合计    ${String(totalClicks).padStart(4)}  ${String(totalFills).padStart(4)}`);
console.log('');
console.log('--- 需求方动作序列 ---');
A.actions.forEach((x, i) => console.log(`${String(i + 1).padStart(2)}. ${x}`));
console.log('');
console.log('--- 接单方动作序列 ---');
B.actions.forEach((x, i) => console.log(`${String(i + 1).padStart(2)}. ${x}`));
console.log('');
console.log('--- 需求方跳转序列 ---');
A.jumpList.forEach((x, i) => console.log(`${String(i + 1).padStart(2)}. ${x}`));
console.log('');
console.log(`订单终态: ${doneOrder ? `已完成 (${doneOrder.id})` : '未完成 —— ' + JSON.stringify(finalOrders)}`);
console.log(`需求方总计: ${A.clicks} 次点击 / ${A.fills} 次输入 / ${A.jumps} 次跳转`);
console.log(`接单方总计: ${B.clicks} 次点击 / ${B.fills} 次输入 / ${B.jumps} 次跳转`);

const fails = [];
if (!publishOk) fails.push('发单没有成功');
if (!doneOrder) fails.push('订单没有走到已完成');
if (!rolePreselected) fails.push('落地页参数未预选身份');

await browser.close();
if (fails.length) {
  console.log('\nFAIL: ' + fails.join('; '));
  process.exit(1);
}
console.log('\nPASS: 六步全通，落地页参数预选生效');

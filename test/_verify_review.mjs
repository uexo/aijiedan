/**
 * 互评界面的真浏览器验收。
 *
 * 为什么不能只靠接口用例: 接口 62 项只证明"后端说得对", 证明不了"用户看得见、
 * 点得下去"。评价这条链最容易出的问题是——后端通了, 但大厅里压根没有那个按钮,
 * 或者按钮点了没反应。这类问题接口用例一条都抓不到。
 *
 * 造数据走接口(快), 验交互走浏览器(真)。最后那三段断言全部基于页面上的文字。
 *
 * 跑法: BASE=http://127.0.0.1:8083 node test/_verify_review.mjs
 */
import fs from 'fs';

const { chromium } = await import(
  'file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs'
);

const BASE = process.env.BASE || 'http://127.0.0.1:8083';
const OUT = 'E:/workspace/project/aijiedan/zanbuddy/web/app/_shots';
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  executablePath:
    'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});

let pass = 0;
let fail = 0;
function step(name, ok, extra = '') {
  if (ok) {
    pass++;
    console.log('PASS  ' + name + (extra ? '  ' + extra : ''));
  } else {
    fail++;
    console.log('FAIL  ' + name + (extra ? '  ' + extra : ''));
  }
}

const randPhone = (p) => p + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');

async function call(page, method, path, body) {
  return page.evaluate(
    async (a) => {
      const pair = document.cookie.split('; ').map((s) => s.split('=')).find(([k]) => k === 'csrf_token');
      const headers = { 'Content-Type': 'application/json' };
      if (pair) headers['X-CSRF-Token'] = pair.slice(1).join('=');
      const opt = { method: a.method, headers, credentials: 'same-origin' };
      if (a.body !== null) opt.body = JSON.stringify(a.body);
      const r = await fetch(a.path, opt);
      let j = null;
      try {
        j = await r.json();
      } catch (e) {
        j = null;
      }
      return { status: r.status, json: j };
    },
    { method, path, body: body === undefined ? null : body }
  );
}

async function login(page, phone, role) {
  await page.goto(BASE + '/static/app/login.html', { waitUntil: 'networkidle' });
  await page.fill('#phone', phone);
  await page.click('#to-role');
  await page.waitForSelector('[data-pane="2"]:not([hidden])');
  await page.click('.pick[data-role="' + role + '"]');
  await page.click('#to-code');
  await page.waitForSelector('[data-pane="3"]:not([hidden])');
  await page.fill('#code', '000000');
  await Promise.all([page.waitForURL(/(hall|profile)\.html/, { timeout: 15000 }), page.click('#submit')]);
  await page.waitForLoadState('networkidle');
}

/* ---------- 造数据: 需求方发单, 接单方接完走完六步 ---------- */
const ctxA = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const ctxB = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const pageA = await ctxA.newPage();
const pageB = await ctxB.newPage();
const errs = [];
for (const p of [pageA, pageB]) {
  p.on('pageerror', (e) => errs.push(String(e)));
  p.on('console', (m) => {
    if (m.type() === 'error') errs.push(m.text());
  });
}

await login(pageA, randPhone('136'), 'demand');
step('需求方登录', /hall\.html/.test(pageA.url()), pageA.url());

const pub = await call(pageA, 'POST', '/api/v2/tasks', {
  title: '给茶叶店做一个动销分析脚本',
  category: 'data',
  spec: '每天导出订单表，想要一个脚本算出哪几个 SKU 动销最快、哪些在压货，输出周报。',
  budget_cents: 80000,
  deliverables: '1 个 Python 脚本 + 一份使用说明',
  required_format: 'py + md',
  items: ['能读导出的 xlsx', '算出动销排名', '输出周报 markdown'],
});
const taskId = (((pub.json || {}).data || {}).id) || '';
step('需求方发单成功', pub.status === 201 && !!taskId, pub.status + ' ' + taskId);

await login(pageB, randPhone('137'), 'supply');
const prof = await call(pageB, 'PUT', '/api/v2/me/profile', {
  headline: '把重复流程做成能跑的 Agent',
  bio: '做过 7 个客服工单类 Agent，交付前先给一份能跑的最小版本。',
  skills: ['python', 'rag', '自动化'],
  portfolio: [{ label: '工单分类器', url: 'https://example.com/case' }],
  availability: 1,
});
step('接单方建档', prof.status === 200, prof.status);

const claim = await call(pageB, 'POST', '/api/v2/tasks/' + taskId + '/take', {});
const orderId = (((claim.json || {}).data || {}).id) || '';
step('接单方认领', claim.status === 201 && !!orderId, claim.status + ' ' + orderId);

await call(pageA, 'POST', '/api/v2/orders/' + orderId + '/confirm', {});
await call(pageB, 'POST', '/api/v2/orders/' + orderId + '/deliveries', {
  content: '脚本和说明都放进链接了',
  items: [
    { item_id: 'R1', done: true, note: 'xlsx 读取已通' },
    { item_id: 'R2', done: true, note: '按 7 日动销排' },
    { item_id: 'R3', done: true, note: '输出 md 周报' },
  ],
});
const acc = await call(pageA, 'POST', '/api/v2/orders/' + orderId + '/accept', {});
step('这一单走完', acc.status === 200 && (((acc.json || {}).data || {}).state === 'done'), acc.status);

/* ---------- 验交互: 需求方在大厅里看到评价入口 ---------- */
await pageA.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await pageA.waitForSelector('[data-order="' + orderId + '"]', { state: 'attached', timeout: 15000 });
await pageA.screenshot({ path: OUT + '/review-1-done-order.png', fullPage: true });

const askSel = '[data-order="' + orderId + '"] [data-ask-review]';
step('完成的单上有「评价」按钮', (await pageA.$(askSel)) !== null);

await pageA.click(askSel);
await pageA.waitForSelector('[data-box="review-' + orderId + '"]:not([hidden])', { timeout: 8000 });
step('评价框能打开', true);

// 不选星直接提交, 必须被拦下(默认给 5 星等于替用户表态)
await pageA.click('[data-box="review-' + orderId + '"] [data-do-review]');
const tipAfterEmpty = await pageA.textContent('body');
step('没选星提交被拦下', /先选 1 到 5 星/.test(tipAfterEmpty));

await pageA.click('[data-box="review-' + orderId + '"] [data-star="4"]');
const pressed = await pageA.evaluate((id) =>
  Array.from(document.querySelectorAll('[data-box="review-' + id + '"] [data-star]')).map((b) =>
    b.getAttribute('aria-pressed')
  ), orderId);
step('选 4 星后前四个点亮', JSON.stringify(pressed) === JSON.stringify(['true', 'true', 'true', 'true', 'false']), pressed.join(','));

await pageA.fill('#rc-' + orderId, '交付很干净，启动说明写得比我自己写的还清楚。');
await pageA.screenshot({ path: OUT + '/review-2-form.png', fullPage: true });
await pageA.click('[data-box="review-' + orderId + '"] [data-do-review]');
await pageA.waitForFunction(
  (id) => {
    const li = document.querySelector('[data-order="' + id + '"]');
    return !!li && /你已评价/.test(li.textContent || '');
  },
  orderId,
  { timeout: 15000 }
);
step('提交后显示「你已评价」', true);
const hint = await pageA.textContent('[data-order="' + orderId + '"]');
step('并且说明「等对方评价」', /等对方评价/.test(hint));
await pageA.screenshot({ path: OUT + '/review-3-waiting.png', fullPage: true });

/* ---------- 关键: 接单方这时看不到对方打了几分 ---------- */
await pageB.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await pageB.waitForSelector('[data-order="' + orderId + '"]', { state: 'attached', timeout: 15000 });
const seenByB = await pageB.textContent('[data-order="' + orderId + '"]');
step('接单方看到「对方先评了」的提示', /他先评了/.test(seenByB));
step('接单方看不到对方打了几分', !/★/.test(seenByB));

// 从接口侧面核对: 此刻 theirs 必须是空的
const before = await call(pageB, 'GET', '/api/v2/orders/' + orderId + '/reviews');
step(
  '接口层: 未揭盲时 theirs 为空但告知对方已提交',
  (((before.json || {}).data || {}).theirs || []).length === 0 &&
    ((before.json || {}).data || {}).counterpart_submitted === true,
  JSON.stringify((before.json || {}).data || {})
);

/* ---------- 接单方也评 → 双方揭盲 ---------- */
await pageB.click('[data-order="' + orderId + '"] [data-ask-review]');
await pageB.waitForSelector('[data-box="review-' + orderId + '"]:not([hidden])', { timeout: 8000 });
await pageB.click('[data-box="review-' + orderId + '"] [data-star="5"]');
await pageB.fill('#rc-' + orderId, '需求方判断很准，验收也快。');
await pageB.click('[data-box="review-' + orderId + '"] [data-do-review]');
await pageB.waitForFunction(
  (id) => {
    const li = document.querySelector('[data-order="' + id + '"]');
    return !!li && /看双方评价/.test(li.textContent || '');
  },
  orderId,
  { timeout: 15000 }
);
step('双方都评完后出现「看双方评价」', true);

await pageB.click('[data-order="' + orderId + '"] [data-show-reviews]');
await pageB.waitForSelector('[data-box="show-' + orderId + '"]:not([hidden])', { timeout: 8000 });
await pageB.waitForFunction(
  (id) => {
    const box = document.querySelector('[data-reviews="' + id + '"]');
    return !!box && /★/.test(box.textContent || '');
  },
  orderId,
  { timeout: 8000 }
);
const shown = await pageB.textContent('[data-reviews="' + orderId + '"]');
step('评价正文看得见', /需求方判断很准/.test(shown));
step('自己那条也在里面', /交付很干净/.test(shown));
step('两条都带星级', (shown.match(/★/g) || []).length >= 8, String((shown.match(/★/g) || []).length));
await pageB.screenshot({ path: OUT + '/review-4-both.png', fullPage: true });

step('全程无 JS 报错', errs.length === 0, errs.slice(0, 2).join(' | '));

await browser.close();
console.log('');
console.log('互评界面: ' + pass + ' 通过, ' + fail + ' 失败');
process.exit(fail ? 1 : 0);

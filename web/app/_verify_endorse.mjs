/**
 * 背书这一格的浏览器验收：真人在页面上能不能把一段背书交上去。
 *
 * 接口用例（test/test_m0_endorse.py）证明的是规则对；这一支证明的是
 * 「按钮在、字能填、提交后有反馈」，以及那三个最容易只看接口看不出来的洞：
 *   1. 三问是不是真的渲染出来了（接口返回 3 条 ≠ 页面上有 3 个输入框）
 *   2. 字数提示是不是按"去空白后的字符"算（前端 .length 会和服务端对不上）
 *   3. 提交成功后那一格是不是变成"已写好 + 能追加"，而不是还停在表单上
 *
 * 跑法：node web/app/_verify_endorse.mjs   （服务需已在 8082 起来，库为 demo 状态）
 */
import fs from 'fs';

const { chromium } = await import(
  'file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs'
);

const BASE = process.env.BASE || 'http://127.0.0.1:8082';
const OUT = 'E:/workspace/project/aijiedan/zanbuddy/web/app/_shots';
fs.mkdirSync(OUT, { recursive: true });

let pass = 0;
const fails = [];
const step = (name, ok, extra = '') => {
  if (ok) pass += 1;
  else fails.push(`${name} :: ${extra}`);
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  ' + extra : ''}`);
};

/* ---------- 一个够用的 API 客户端：手管 cookie + CSRF ---------- */
class Api {
  constructor() {
    this.cookies = new Map();
  }
  async call(method, path, body) {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const csrf = this.cookies.get('csrf_token');
    if (csrf) headers['X-CSRF-Token'] = csrf;
    const ck = [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ');
    if (ck) headers.Cookie = ck;
    const res = await fetch(BASE + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    for (const sc of res.headers.getSetCookie?.() || []) {
      const kv = sc.split(';')[0];
      const i = kv.indexOf('=');
      this.cookies.set(kv.slice(0, i), kv.slice(i + 1));
    }
    let json = null;
    try {
      json = await res.json();
    } catch (e) {
      json = null;
    }
    return { st: res.status, d: json };
  }
  async login(phone, role) {
    await this.call('POST', '/api/v2/auth/code', { phone });
    return this.call('POST', '/api/v2/auth/login', { phone, code: '000000', role });
  }
}

const dataOf = (x) => (x && x.d && x.d.data) || {};
// 默认随机手机号（每次跑都是一个新接单方，用例之间不互相污染）。
// 演示时传 PROVIDER_PHONE 固定下来，否则每次演示页面上的人名都变。
const phone =
  process.env.PROVIDER_PHONE ||
  '157' + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');

/* 1. 先用接口把一张单走到"已完成"——背书只对做完的活开放 */
const P = new Api();
let r = await P.login(phone, 'supply');
step('接单方能登录', r.st === 200, JSON.stringify(r.d).slice(0, 120));
r = await P.call('PUT', '/api/v2/me/profile', {
  headline: '把重复流程做成能跑的 Agent',
  bio: '做过客服工单自动化，交付前会先跑一遍。',
  skills: ['python', 'rag', '自动化'],
  portfolio: [{ label: '工单分类器', url: 'https://example.com/case' }],
  availability: 1,
});
step('接单方档案能存', r.st === 200, r.st);

const R = new Api();
r = await R.login('13800000001', 'demand');
step('需求方 R001 能登录', r.st === 200, r.st);

r = await P.call('POST', '/api/v2/tasks/T-DEMO-001/take', {});
step('接单方认领主单', r.st === 201, JSON.stringify(r.d).slice(0, 140));
const orderId = dataOf(r).id || '';
step('拿到订单号', /^O-/.test(orderId), orderId);

r = await R.call('POST', `/api/v2/orders/${orderId}/confirm`, {});
step('需求方确认开工', r.st === 200, JSON.stringify(r.d).slice(0, 140));
r = await P.call('POST', `/api/v2/orders/${orderId}/deliveries`, {
  content: '做完了，启动脚本和说明都放进链接了',
  items: [
    { item_id: 'R1', done: true, note: 'PDF/Word 都通了' },
    { item_id: 'R2', done: true, note: '返回带引用片段' },
    { item_id: 'R3', done: true, note: '启动脚本已补 Windows 说明' },
  ],
});
step('接单方交付', r.st === 201, JSON.stringify(r.d).slice(0, 140));
r = await R.call('POST', `/api/v2/orders/${orderId}/accept`, {});
step('需求方验收通过', r.st === 200, JSON.stringify(r.d).slice(0, 140));

// SEED_ONLY=1 时到此为止：留一个"单已做完、背书还没写"的现场，
// 人打开页面就能看到空的可填背书框。演示和验收用同一段准备代码，
// 免得演示环境里手点一遍六步、每次都点歪。
if (process.env.SEED_ONLY === '1') {
  console.log('');
  console.log('演示现场已就绪: 13800000001 登录 → 大厅 → 我的单 → 写背书');
  console.log(`ORDER=${orderId}`);
  process.exit(0); // 浏览器还没起，不必关
}

/* 2. 浏览器：需求方登录，进大厅，打开「我的单」 */
const browser = await chromium.launch({
  executablePath:
    'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});
const ctx = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(String(e)));
page.on('console', (m) => {
  if (m.type() === 'error') errors.push(m.text());
});

await page.goto(BASE + '/static/app/login.html', { waitUntil: 'networkidle' });
await page.fill('#phone', '13800000001');
await page.click('#to-role');
await page.waitForSelector('[data-pane="2"]:not([hidden])');
await page.click('.pick[data-role="demand"]');
await page.click('#to-code');
await page.waitForSelector('[data-pane="3"]:not([hidden])');
await page.fill('#code', '000000');
await page.click('#submit');
await page.waitForURL(/hall\.html/, { timeout: 12000 });
step('需求方登录后进大厅', /hall\.html/.test(page.url()), page.url());

await page.waitForSelector('#mine-list li', { timeout: 10000 }).catch(() => {});
const myCards = await page.locator('#mine-list li').count();
step('「我的单」里有卡片', myCards >= 1, `${myCards} 张`);

/* 3. 打开背书那一格 */
const ask = page.locator('[data-ask-endorse]').first();
await ask.waitFor({ state: 'attached', timeout: 8000 });
await page.locator('[data-act]').first().evaluate((n) => (n.hidden = false));
await ask.click({ force: true });
const box = page.locator('[data-box^="endorse-"]').first();
await box.waitFor({ state: 'visible', timeout: 8000 });
step('背书盒子能打开', await box.isVisible());

await page.waitForSelector('[data-ebody]', { timeout: 8000 }).catch(() => {});
const taCount = await page.locator('[data-ebody]').count();
step('正文输入框渲染出来', taCount === 1, taCount);
const qCount = await page.locator('[data-eans]').count();
step('三问渲染出 3 个输入框', qCount === 3, qCount);
const qText = (await box.textContent()) || '';
step('三问的问句是中文原话', /交付的东西/.test(qText) && /还会找他吗/.test(qText), qText.slice(0, 80));
await page.screenshot({ path: `${OUT}/endorse-form.png`, fullPage: true });

/* 4. 字数提示按"去空白后的字符"算 */
const ta = page.locator('[data-ebody]').first();
await ta.fill('这个脚本');
await page.waitForTimeout(120);
let count = (await page.textContent('[data-ecount]')) || '';
step('4 字时提示 4 字', /^4 字/.test(count.trim()), count.trim());
await ta.fill('这个脚本\n\n   '); // 4 字 + 换行空格
await page.waitForTimeout(120);
count = (await page.textContent('[data-ecount]')) || '';
step('换行和空格不算字数', /^4 字/.test(count.trim()), count.trim());
const warnCls = await page.locator('[data-ecount]').first().getAttribute('class');
step('字数不够时给 warn 提示', /is-warn/.test(warnCls || ''), warnCls);

/* 5. 填完整并提交 */
await ta.fill('这个脚本能读抖音导出的表，我拿上周的数据跑了一遍，动销排在前三的那个 SKU 和我自己的判断一致。');
await page.locator('[data-eans="delivered"]').fill('对得上，三条都做了');
await page.locator('[data-eans="again"]').fill('会，下次复盘还找他');
await page.screenshot({ path: `${OUT}/endorse-filled.png`, fullPage: true });
await page.click('[data-do-endorse]');

await page.waitForFunction(
  () => /写的背书/.test(document.body.textContent || ''),
  null,
  { timeout: 10000, polling: 100 }
).catch(() => {});
const afterText = (await page.locator('[data-box^="endorse-"]').first().textContent()) || '';
step('提交后显示「你给他写的背书」', /写的背书/.test(afterText), afterText.slice(0, 100));
step('原文被摆出来了', /能读抖音导出的表/.test(afterText), afterText.slice(0, 120));
step('答过的问题也摆出来了', /对得上，三条都做了/.test(afterText), afterText.slice(0, 160));
step('给出追加输入框', (await page.locator('[data-box^="endorse-"] textarea').count()) >= 1, '');
await page.screenshot({ path: `${OUT}/endorse-done.png`, fullPage: true });

/* 6. 追加一句 */
const apTa = page.locator('[data-box^="endorse-"] textarea').first();
await apTa.fill('补一句：后来我又找他改过一次，没另外收费。');
await page.click('[data-do-append]');
await page.waitForFunction(
  () => /没另外收费/.test(document.body.textContent || ''),
  null,
  { timeout: 10000, polling: 100 }
).catch(() => {});
const appendText = (await page.locator('[data-box^="endorse-"]').first().textContent()) || '';
step('追加内容出现在原文下面', /没另外收费/.test(appendText), appendText.slice(0, 160));
step('追加后原文没被改', /能读抖音导出的表/.test(appendText), '');
await page.screenshot({ path: `${OUT}/endorse-appended.png`, fullPage: true });

/* 7. 刷新后仍然在（说明不是内存里的一次性渲染） */
await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('#mine-list li', { timeout: 10000 }).catch(() => {});
await page.locator('[data-ask-endorse]').first().evaluate((n) => (n.hidden = false)).catch(() => {});
await page.locator('[data-ask-endorse]').first().click({ force: true }).catch(() => {});
await page.waitForSelector('[data-box^="endorse-"]', { timeout: 8000 }).catch(() => {});
await page.waitForTimeout(600);
const reloadText = (await page.locator('[data-box^="endorse-"]').first().textContent()) || '';
step('刷新后背书还在', /能读抖音导出的表/.test(reloadText) && /没另外收费/.test(reloadText), reloadText.slice(0, 120));

step('无 JS 报错', errors.length === 0, errors.slice(0, 3).join(' | '));

await browser.close();
console.log('');
console.log(`${pass} passed, ${fails.length} failed`);
if (fails.length) {
  for (const f of fails) console.log('  - ' + f);
}
process.exit(fails.length ? 1 : 0);

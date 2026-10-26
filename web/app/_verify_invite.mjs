/**
 * "发出去"这一条的浏览器验收：一个没登录的人，点开别人发来的短链，能不能接下这一单。
 *
 * 接口用例证明的是规则对；这一支证明的是那条路径真的通：
 *   1. 发单成功后，页面上真的出现了"一段能直接发出去的话"，复制出来的就是这段话
 *   2. 那段话里的链接点开，落到的是认领页，不是登录页，也不是 404
 *   3. 没登录时按钮说的是"登录后接这一单"，而不是点下去才报错
 *   4. 登录后同一个链接再打开，按钮变成"接这一单"，点完真的成了
 *   5. 这两次打开都被记下来了（否则那个唯一的数就是编的）
 *
 * 跑法：node web/app/_verify_invite.mjs   （服务需已在 8082 起来，库为 demo 状态）
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
    } catch {
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
const rnd = () => String(Math.floor(Math.random() * 1e8)).padStart(8, '0');
const demandPhone = '138' + rnd();
const supplyPhone = '157' + rnd();

const browser = await chromium.launch({
  executablePath:
    'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});

async function login(page, phone, role) {
  await page.goto(BASE + '/static/app/login.html', { waitUntil: 'networkidle' });
  await page.fill('#phone', phone);
  await page.click('#to-role');
  await page.waitForSelector('[data-pane="2"]:not([hidden])');
  await page.click(`.pick[data-role="${role}"]`);
  await page.click('#to-code');
  await page.waitForSelector('[data-pane="3"]:not([hidden])');
  await page.fill('#code', '000000');
  await page.click('#submit');
}

/* ---------- 接单方先建好档案：没档案的人在认领页会被拦，那是另一条用例的事 ---------- */
const P = new Api();
await P.login(supplyPhone, 'supply');
const pr = await P.call('PUT', '/api/v2/me/profile', {
  headline: '把重复流程做成能跑的 Agent',
  bio: '做过客服工单自动化，交付前会先跑一遍。',
  skills: ['python', 'rag', '自动化'],
  portfolio: [{ label: '工单分类器', url: 'https://example.com/case' }],
  availability: 1,
});
step('接单方档案建好（后面用它验"能接"）', pr.st === 200, pr.st);

/* ---------- 1. 发单方在页面上发出一条单，看那句话是不是真的出现了 ---------- */
const ctx1 = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
await ctx1.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: BASE });
const dp = await ctx1.newPage();
const errors = [];
dp.on('pageerror', (e) => errors.push(String(e)));

await login(dp, demandPhone, 'demand');
await dp.waitForURL(/hall\.html|profile\.html/, { timeout: 12000 });

const title = '把抖音订单表做成周报脚本';
await dp.goto(BASE + '/static/app/publish.html', { waitUntil: 'networkidle' });
await dp.waitForSelector('#category option[value="data"]', { state: 'attached', timeout: 8000 });
await dp.fill('#title', title);
await dp.selectOption('#category', 'data');
await dp.fill('#budget', '1200');
await dp.fill('#spec', '店铺做茶叶，抖音小店每天导出订单表。想要一个脚本读表，算出哪几个 SKU 动销最快、哪些在压货，输出一张周报。跑在自己的 Windows 机器上。');
await dp.fill('#deliverables', '1 个 Python 脚本 + 一份使用说明');
await dp.fill('#format', 'py 文件 + md');
await dp.click('#submit');
await dp.waitForSelector('#done:not([hidden])', { timeout: 10000 });
const taskId = ((await dp.textContent('#done-id')) || '').trim();
step('发单成功并拿到单号', /^T/.test(taskId), taskId);

await dp.waitForSelector('#done-send:not([hidden])', { timeout: 8000 });
const sentence = ((await dp.textContent('#send-box')) || '').trim();
step('页面上出现了一段"能直接发出去的话"', sentence.length > 20, sentence.slice(0, 40));
step('这段话里有标题', sentence.includes(title), sentence.slice(0, 60));
step('这段话里有钱，且整钱不带小数', /1200 元/.test(sentence) && !/1200\.00/.test(sentence), sentence);
step('这段话里带短链', /\/t\/T-/.test(sentence), sentence);
await dp.screenshot({ path: `${OUT}/send-sentence.png`, fullPage: true });

/* 复制按钮：复制出来的必须就是这段话，不是别的什么 */
await dp.click('#send-copy');
await dp.waitForTimeout(300);
const clip = await dp.evaluate(() => navigator.clipboard.readText().catch(() => ''));
step('复制按钮复制的是那段话', clip.trim() === sentence, `clip=${clip.slice(0, 30)}`);
const hint = ((await dp.textContent('#send-hint')) || '').trim();
step('复制后给了反馈', /已复制|选中/.test(hint), hint);

const shortURL = (sentence.match(/https?:\/\/\S+/) || [''])[0];
step('从话术里能取出一个完整链接', /^https?:\/\/.+\/t\/T-/.test(shortURL), shortURL);

/* ---------- 2. 一个完全没登录的人点开这条链接 ---------- */
const ctx2 = await browser.newContext({ viewport: { width: 430, height: 900 } });
const anon = await ctx2.newPage();
anon.on('pageerror', (e) => errors.push(String(e)));

const res = await anon.goto(shortURL, { waitUntil: 'networkidle' });
step('短链能打开（不是 404、不是登录页）', res.status() === 200, res.status());
step('落到了认领页', /take\.html/.test(anon.url()), anon.url());
await anon.waitForSelector('#main:not([hidden])', { timeout: 8000 }).catch(() => {});

const anonTitle = ((await anon.textContent('#title')) || '').trim();
step('未登录也能看到这一单的标题', anonTitle === title, anonTitle);
const anonMoney = ((await anon.textContent('#budget')) || '').trim();
step('金额摆在最显眼的地方', anonMoney === '¥1,200', anonMoney);
const anonBtn = ((await anon.textContent('#claim')) || '').trim();
step('未登录时按钮是「登录后接这一单」', anonBtn === '登录后接这一单', anonBtn);
await anon.screenshot({ path: `${OUT}/take-anon.png`, fullPage: true });

/* 手机上也不能横向溢出：这条链接多半是在微信里点开的 */
const overflow = await anon.evaluate(
  () => document.documentElement.scrollWidth - document.documentElement.clientWidth
);
step('手机宽度下没有横向溢出', overflow <= 1, overflow);

/* ---------- 3. 登录之后，同一个链接再打开 ---------- */
const ctx3 = await browser.newContext({ viewport: { width: 430, height: 900 } });
const sp = await ctx3.newPage();
sp.on('pageerror', (e) => errors.push(String(e)));

await login(sp, supplyPhone, 'supply');
await sp.waitForURL(/hall\.html|profile\.html/, { timeout: 12000 });
await sp.goto(shortURL, { waitUntil: 'networkidle' });
await sp.waitForSelector('#main:not([hidden])', { timeout: 8000 });

const loginBtn = ((await sp.textContent('#claim')) || '').trim();
step('登录且有档案时按钮是「接这一单」', loginBtn === '接这一单', loginBtn);
await sp.screenshot({ path: `${OUT}/take-ready.png`, fullPage: true });

await sp.click('#claim');
await sp.waitForSelector('#blocked:not([hidden])', { timeout: 10000 });
const doneH = ((await sp.textContent('#bk-h')) || '').trim();
step('点完真的接下了', doneH === '接下了', doneH);
const doneP = ((await sp.textContent('#bk-p')) || '').trim();
step('接下来会发生什么说清楚了', /确认开工/.test(doneP), doneP.slice(0, 50));
await sp.screenshot({ path: `${OUT}/take-done.png`, fullPage: true });

/* ---------- 4. 两次打开都被记下来了 ---------- */
const inv = await dp.evaluate(
  (id) => fetch(`/api/v2/tasks/${id}/invite`).then((r) => r.json()),
  taskId
);
const hits = (inv && inv.data && inv.data.opens) || 0;
step('两次打开都被记下来（那个数不是编的）', hits >= 2, hits);
step('链接被打开过之后，话术仍拿得到', !!(inv && inv.data && inv.data.sentence), hits);
step('这一单已被接走，话术提示不能再发', inv.data.can_redo === false, inv.data.can_redo);

/* ---------- 5. 发单方回到「我的单」能看到它动了 ---------- */
await dp.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await dp.waitForSelector('#mine-list li', { timeout: 10000 }).catch(() => {});
const mineText = (await dp.locator('#mine-list').textContent()) || '';
step('发单方能看到这一单已被人接', /已有人接|待确认/.test(mineText), mineText.slice(0, 60));

/* ---------- 6. 那个数要长在发单方眼前 ----------
   前面几段验的是"传出去了没有"这个事实存在; 这一段验的是发单方自己能不能看见。
   看不见的数不是指标, 是日志。 */
const ctx4 = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const dp2 = await ctx4.newPage();
dp2.on('pageerror', (e) => errors.push(String(e)));
await login(dp2, demandPhone, 'demand');
await dp2.waitForURL(/hall\.html|profile\.html/, { timeout: 12000 });

const title2 = '把客服话术过一遍';
const made = await dp2.evaluate(async (t) => {
  // 写请求要自己带 CSRF 双提交里的那个头: runtime 的 api() 会带, 裸 fetch 不会,
  // 少了它服务端回 403, 看起来像"没权限发单"。
  const m = document.cookie.match(/(?:^|; )csrf_token=([^;]+)/);
  const r = await fetch('/api/v2/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': m ? m[1] : '' },
    body: JSON.stringify({
      title: t,
      category: 'nlp',
      spec: '需要 python + rag，支持文档上传与中文问答，交付要能跑起来。',
      budget_cents: 50000,
      deliverables: '1 套源码 + 部署文档',
      required_format: 'Python 项目(zip) + README',
      style_ref: '生产级可维护代码',
      skills: ['python', 'rag', '自动化'],
      items: ['能读 PDF', '返回带引用片段'],
    }),
  });
  const j = await r.json();
  return { st: r.status, id: (j.data && j.data.id) || '' };
}, title2);
step('另发一条还挂着的单（用它验发单方看到的那一格）', made.st === 201 && /^T/.test(made.id), JSON.stringify(made));

await dp2.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await dp2.waitForSelector(`[data-open-task="${made.id}"]`, { timeout: 10000 });
const cardText = async () =>
  ((await dp2.locator(`[data-open-task="${made.id}"]`).textContent()) || '').trim();

step('还没人点开时，卡片上就写着', /链接还没人点开/.test(await cardText()), (await cardText()).slice(0, 60));

/* 让另一个人（无痕上下文）点一次短链 */
await anon.goto(`${BASE}/t/${made.id}`, { waitUntil: 'networkidle' });

await dp2.reload({ waitUntil: 'networkidle' });
await dp2.waitForSelector(`[data-open-task="${made.id}"]`, { timeout: 10000 });
step('被点开一次之后，卡片上的数变了', /链接被点开 1 次/.test(await cardText()), (await cardText()).slice(0, 60));
await dp2.locator(`[data-open-task="${made.id}"]`).screenshot({ path: `${OUT}/card-opened.png` });

/* "再发一次"必须有落脚处：那段话不能只在发单成功那一屏存在 */
await dp2.click(`[data-ask-send="${made.id}"]`);
await dp2.waitForSelector(`[data-box="send-${made.id}"] .send-box`, { timeout: 8000 });
const again = ((await dp2.textContent(`[data-box="send-${made.id}"] .send-box`)) || '').trim();
step('「发出去」能重新拿到那段话', again.includes(title2) && /\/t\/T-/.test(again), again.slice(0, 60));
step('重新拿到的金额写法一致', /500 元/.test(again) && !/500\.00/.test(again), again);
const copyBtn = await dp2.locator(`[data-copy-send="${made.id}"]`).count();
step('有「复制这段话」按钮', copyBtn === 1, copyBtn);
await dp2.click(`[data-copy-send="${made.id}"]`);
// 复制的落点反馈是异步写的（clipboard 要 await），点完立刻读会读到空字符串。
// 等它出现，而不是 sleep 一个拍脑袋的毫秒数。
const hintSel = `[data-box="send-${made.id}"] [data-send-hint]`;
await dp2.waitForFunction(
  (sel) => /已复制|长按/.test((document.querySelector(sel) || {}).textContent || ''),
  hintSel,
  { timeout: 4000 }
).catch(() => {});
const hint2 = ((await dp2.textContent(hintSel)) || '').trim();
step('复制后有反馈', /已复制|长按/.test(hint2), hint2);
await dp2.screenshot({ path: `${OUT}/send-again.png`, fullPage: true });

/* 已被人接走的单不该还给一个"发出去"的按钮：对方点开只会看到"不在了" */
await dp.evaluate(
  (args) => fetch(`/api/v2/tasks/${args.id}/invite`).then((r) => r.json()),
  { id: taskId }
).then((j) => step('被接走的单不再给"再发一次"的口子', j && j.data && j.data.can_redo === false, j && j.data && j.data.can_redo));

step('无 JS 报错', errors.length === 0, errors.slice(0, 2).join(' | '));

await browser.close();
console.log('');
console.log(`${pass} passed, ${fails.length} failed`);
if (fails.length) for (const f of fails) console.log('  - ' + f);
process.exit(fails.length ? 1 : 0);

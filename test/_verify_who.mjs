/**
 * 「谁能接」三卡的真浏览器验收。
 *
 * 为什么必须走浏览器: 接口用例只证明 GET /tasks/{id}/matches 说得对,
 * 证明不了需求方发完单那一刻真的看见了三张卡、理由点得开、凭据写得清。
 * 这一块最容易出的问题恰恰是"后端通了, 前端压根没接上"——接口用例一条都抓不到。
 *
 * 造数据走接口(快), 发单走真实表单(真), 断言全部基于页面文字。
 * 跑法: BASE=http://127.0.0.1:8082 node test/_verify_who.mjs
 */
import fs from 'fs';

const { chromium } = await import(
  'file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs'
);

const BASE = process.env.BASE || 'http://127.0.0.1:8082';
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

const HEAD_B = '把重复流程做成能跑的脚本';
const HEAD_C = '写点 Python 小工具';

/* ---------- 造两个接单方: 一个强对口, 一个只有一项对得上 ---------- */
const ctxB = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const ctxC = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const ctxA = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const pageB = await ctxB.newPage();
const pageC = await ctxC.newPage();
const pageA = await ctxA.newPage();

const errs = [];
for (const p of [pageA, pageB, pageC]) {
  p.on('pageerror', (e) => errs.push(String(e)));
  p.on('console', (m) => {
    // 资源 404/403 由下面的断言各自负责, 不算"脚本挂了";
    // 这里只收真正把页面搞坏的错误。
    if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errs.push(m.text());
  });
}

await login(pageB, randPhone('137'), 'supply');
const profB = await call(pageB, 'PUT', '/api/v2/me/profile', {
  headline: HEAD_B,
  bio: '做过 7 个电商数据脚本，交付前先给一份能跑的最小版本。',
  skills: ['python', 'xlsx', 'etl'],
  portfolio: [{ label: '动销周报脚本', url: 'https://example.com/case' }],
  availability: 1,
});
step('接单方 B 建档', profB.status === 200, profB.status);

await login(pageC, randPhone('135'), 'supply');
// 技能必须 3-5 个(identity 层的硬规则), 所以这里给 3 个、只有 python 对得上——
// 这样才能看出"对口多的排在前面", 而不是两个人都全中。
const profC = await call(pageC, 'PUT', '/api/v2/me/profile', {
  headline: HEAD_C,
  bio: '写点小工具，前端和作图也接。',
  skills: ['python', 'vue', 'photoshop'],
  portfolio: [],
  availability: 1,
});
step('接单方 C 建档', profC.status === 200, profC.status);

/* ---------- 需求方: 走真实表单发单 ---------- */
await login(pageA, randPhone('136'), 'demand');
await pageA.goto(BASE + '/static/app/publish.html', { waitUntil: 'networkidle' });
await pageA.waitForSelector('#category option[value="data"]', { state: 'attached', timeout: 15000 });

const TITLE = '写一个 python 脚本读 xlsx 算动销';
await pageA.fill('#title', TITLE);
await pageA.selectOption('#category', 'data');
await pageA.fill('#budget', '800');
await pageA.fill('#spec', '每天从后台导出订单 xlsx，想要一个 python 脚本算出哪几个 SKU 动销最快、哪些在压货，输出周报 markdown。');
await pageA.fill('#deliverables', '1 个 python 脚本 + 一份使用说明');
await pageA.fill('#format', 'py + md');
await pageA.click('#submit');

await pageA.waitForSelector('#done:not([hidden])', { timeout: 15000 });
step('发单成功', true);

// 澄清提示曾经被当成字符串塞进 html``, 整串 <li> 被转义成可见文字。
// 这一条钉的就是它: 页面上看到的必须是列表, 不是标签源码。
const clarShown = (await pageA.$('#done-clar:not([hidden])')) !== null;
if (clarShown) {
  const clarLis = await pageA.$$('#done-clar li');
  const clarText = await pageA.textContent('#done-clar');
  step('澄清提示渲染成列表项', clarLis.length > 0, '项数 ' + clarLis.length);
  step('页面上不出现裸露的 <li>', !/<\/?li>/.test(clarText), clarText.slice(0, 40));
} else {
  step('澄清提示渲染成列表项', false, '这一单没触发澄清提示, 用例前提不成立');
}

/* ---------- 当场看到候选人 ---------- */
await pageA.waitForSelector('#done-who [data-who]', { timeout: 20000 });
const cards = await pageA.$$('#done-who [data-who]');
step('发完单当场出现候选人卡', cards.length === 2, '卡片数 ' + cards.length);
await pageA.screenshot({ path: OUT + '/who-1-publish.png', fullPage: true });

const whoText = await pageA.textContent('#done-who');
step('卡上有强对口那位', whoText.includes(HEAD_B));
step('卡上有弱对口那位', whoText.includes(HEAD_C));
const posB = whoText.indexOf(HEAD_B);
const posC = whoText.indexOf(HEAD_C);
step('对口多的排在前面', posB >= 0 && posC > posB, posB + ' < ' + posC);

step('理由说得出口', /技能对得上/.test(whoText), (whoText.match(/技能对得上[^看]*/) || [''])[0].slice(0, 40));
step('写明这一次还不能直接指派', /还不能直接指派/.test(whoText));
step('卡上不出现综合匹配度这种数', !/匹配度|综合匹配|%/.test(whoText));

// 只有技能标签的话, 需求方看到的全是自述。作品是他自己排在最前面的那条,
// 也是这张卡上唯一能点开看真东西的地方。
const workLink = await pageA.$('#done-who .wwork a');
step('卡上有能点开的作品', workLink !== null);
if (workLink) {
  const href = await workLink.getAttribute('href');
  step('作品链接是完整地址', /^https?:\/\//.test(href || ''), href || '');
  step('新窗口打开不乱跑掉当前页', (await workLink.getAttribute('target')) === '_blank');
}
const noWork = await pageA.evaluate(() => {
  const cards = Array.from(document.querySelectorAll('#done-who [data-who]'));
  return cards.filter((c) => /写点 Python 小工具/.test(c.textContent || '') && !c.querySelector('.wwork')).length;
});
step('没填作品的那位不硬凑一条', noWork === 1);

/* ---------- 理由点得开、看得见凭据 ---------- */
const why = await pageA.$('#done-who [data-why]');
step('理由带「看凭据」', (await why.textContent()).includes('看凭据'));
step('凭据默认收起', (await pageA.$('#done-who [data-whybox]:not([hidden])')) === null);

await why.click();
await pageA.waitForSelector('#done-who [data-whybox]:not([hidden])', { timeout: 5000 });
const boxText = await pageA.textContent('#done-who [data-whybox]:not([hidden])');
step('点开看到技能凭据', /档案技能标签：/.test(boxText), boxText.slice(0, 40));
step('并且说清是自己填的', /本人自填/.test(boxText));
step('按钮状态跟着变', (await why.getAttribute('aria-expanded')) === 'true');
await pageA.screenshot({ path: OUT + '/who-2-evidence.png', fullPage: true });

await why.click();
step('再点一下收起', (await pageA.$('#done-who [data-whybox]:not([hidden])')) === null);

/* ---------- 回到大厅「我发的单」里还能点开 ---------- */
await pageA.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await pageA.waitForSelector('[data-open-task]', { state: 'attached', timeout: 15000 });
const ask = await pageA.$('[data-open-task] [data-ask-who]');
step('大厅「我发的单」上有「谁能接」入口', ask !== null);

await ask.click();
await pageA.waitForSelector('[data-box^="who-"]:not([hidden]) [data-who]', { timeout: 20000 });
const hallWho = await pageA.textContent('[data-box^="who-"]:not([hidden])');
step('大厅里也能看到同一批人', hallWho.includes(HEAD_B) && hallWho.includes(HEAD_C));
step('大厅里也说明了不能直接指派', /还不能直接指派/.test(hallWho));
await pageA.screenshot({ path: OUT + '/who-3-hall.png', fullPage: true });

/* ---------- 接单方看不到别人的推荐位 ---------- */
const asB = await call(pageB, 'GET', '/api/v2/tasks?limit=1&sort=newest&q=' + encodeURIComponent('动销'));
const tid = (((asB.json || {}).data || {}).items || [])[0];
if (tid) {
  const peek = await call(pageB, 'GET', '/api/v2/tasks/' + tid.id + '/matches');
  step('接单方读别人的推荐名单被拒', peek.status === 403, peek.status);
} else {
  step('接单方读别人的推荐名单被拒', false, '没找到刚发的单');
}

step('全程无 JS 报错', errs.length === 0, errs.slice(0, 2).join(' | '));

await browser.close();
console.log('');
console.log('谁能接三卡: ' + pass + ' 通过, ' + fail + ' 失败');
process.exit(fail ? 1 : 0);

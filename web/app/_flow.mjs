/**
 * 端到端走一遍：接单方登录 → 填档案 → 保存 → 大厅列表。
 * 这是「保存后自动跳大厅」这条需求的验收脚本，肉眼看得见的那种。
 *
 * 跑法：node web/app/_flow.mjs   （服务需已在 8082 起来）
 */
import path from 'path';
import fs from 'fs';

const { chromium } = await import('file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs');

const BASE = process.env.BASE || 'http://127.0.0.1:8082';
const OUT = 'E:/workspace/project/aijiedan/zanbuddy/web/app/_shots';
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  executablePath: 'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});

const phone = '139' + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');
const log = [];
const step = (name, ok, extra = '') => {
  log.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  ' + extra : ''}`);
  console.log(log[log.length - 1]);
};

const ctx = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(String(e)));
page.on('console', (m) => {
  if (m.type() === 'error') errors.push(m.text());
});

/* 1. 登录 */
await page.goto(BASE + '/static/app/login.html', { waitUntil: 'networkidle' });
await page.fill('#phone', phone);
await page.click('#to-role');
await page.waitForSelector('[data-pane="2"]:not([hidden])');
await page.click('.pick[data-role="supply"]');
await page.click('#to-code');
await page.waitForSelector('[data-pane="3"]:not([hidden])');
await page.fill('#code', '000000');
await Promise.all([page.waitForURL(/profile\.html/, { timeout: 10000 }), page.click('#submit')]);
step('登录后落到档案页', /profile\.html/.test(page.url()), page.url());

/* 2. 档案加载完成 */
await page.waitForFunction(() => !document.getElementById('save').disabled, null, { timeout: 8000 });
await page.screenshot({ path: path.join(OUT, 'profile-empty.png'), fullPage: true });

/* 3. 填档案 */
await page.fill('#name', '算法老王');
await page.fill('#headline', '把重复流程做成能跑的 Agent');
await page.fill('#bio', '做过 7 个客服工单类 Agent，最长的一个跑了两年。交付前先给一份能跑的最小版本。');
await page.click('.chip[data-skill="python"]');
await page.click('.chip[data-skill="rag"]');
await page.click('.chip[data-skill="automation"]');
const pctBefore = await page.textContent('#pct');
step('填完显示完整度 80%', pctBefore.trim() === '80%', pctBefore.trim());
await page.screenshot({ path: path.join(OUT, 'profile-filled.png'), fullPage: true });

/* 4. 保存 → 必须自己进大厅 */
const t0 = Date.now();
await page.click('#save');
// 先看见「已保存」再看见跳转：证明不是"提交失败但页面被别人带走了"
let savedToast = '';
try {
  await page.waitForFunction(
    () => /已保存|已进入/.test((document.getElementById('toast') || {}).textContent || ''),
    null,
    { timeout: 8000, polling: 100 }
  );
  savedToast = (await page.textContent('#toast')) || '';
} catch (e) {
  savedToast = '';
}
await page.waitForURL(/hall\.html/, { timeout: 10000 });
const cost = Date.now() - t0;
step('保存后自动跳转大厅', true, `${cost}ms  ${page.url()}`);
step('跳转前弹了保存成功', /已保存/.test(savedToast), savedToast.trim());

/* 5. 大厅必须有单 */
await page.waitForSelector('.task', { timeout: 8000 }).catch(() => {});
const cards = await page.locator('.task').count();
const countText = (await page.textContent('#count')) || '';
step('大厅渲染出需求卡片', cards >= 1, `${cards} 张 · ${countText.trim()}`);
const firstTitle = cards ? (await page.locator('.task .tlink').first().textContent()) : '';
step('卡片有标题', !!firstTitle.trim(), String(firstTitle).trim());
const saveFlagToast = await page.textContent('#toast').catch(() => '');
step('落地说清了已保存', /档案已保存/.test(saveFlagToast || ''), String(saveFlagToast).trim());

/* 6. 档案数据真的带过来了 */
const who = (await page.textContent('#who')) || '';
step('大厅认识这个人', /档案完整度 80%/.test(who), who.trim());
step('大厅显示的是刚填的昵称', /算法老王/.test(who), who.trim());

await page.screenshot({ path: path.join(OUT, 'hall-desk.png'), fullPage: true });

/* 7. 展开一条需求 */
await page.locator('.task .tlink').first().click();
const detailOpen = await page.locator('.task .detail:not([hidden])').count();
step('点标题能展开要求', detailOpen === 1, `${detailOpen} 个展开`);
await page.screenshot({ path: path.join(OUT, 'hall-detail.png'), fullPage: true });

/* 8. 搜索。关键词取自当前第一张卡，不写死「rag」：
   演示数据被认领掉几单之后，写死的关键词会让这一步假失败。 */
const seed = (await page.locator('.task .tlink').first().textContent()) || '';
const kw = seed.trim().slice(0, 3).trim();
await page.fill('#q', kw);
await page.waitForTimeout(700);
const filtered = await page.locator('.task').count();
step('搜「' + kw + '」有结果', filtered >= 1, `${filtered} 条`);

/* 8.5 认领：先确认再动手 */
await page.fill('#q', '');
await page.waitForTimeout(700);
const beforeClaim = await page.locator('.task').count();
const claimTitle = (await page.locator('.task .tlink').first().textContent()) || '';

await page.locator('.task [data-claim]').first().click();
const boxOpen = await page.locator('.claim-box:not([hidden])').count();
step('点认领先亮后果，不直接扣单', boxOpen === 1, `${boxOpen} 个确认框`);
await page.screenshot({ path: path.join(OUT, 'hall-claim-confirm.png'), fullPage: true });

await page.locator('.claim-box:not([hidden]) [data-do]').click();
await page.waitForSelector('#mine:not([hidden])', { timeout: 10000 });
const mineTitle = (await page.locator('#mine-list .oitem strong').first().textContent()) || '';
step('我的单里就是刚认领的这一单', mineTitle.trim() === claimTitle.trim(), `${mineTitle.trim()} vs ${claimTitle.trim()}`);

// 卡片淡出用 180ms，等它真的从 DOM 走掉再数，否则数到的是过渡中的旧节点
await page.waitForTimeout(320);
const afterClaim = await page.locator('.task').count();
step('认领后卡片从大厅消失', afterClaim === beforeClaim - 1, `${beforeClaim} → ${afterClaim}`);

const claimToast = (await page.textContent('#toast').catch(() => '')) || '';
step('认领给了回执', /接了/.test(claimToast), claimToast.trim());
step('导航出现我接的单计数', (await page.textContent('#nav-mine-n')) === '1', await page.textContent('#nav-mine-n'));
await page.screenshot({ path: path.join(OUT, 'hall-claimed.png'), fullPage: true });

/* 8.6 刷新：认领必须还在（证明落库，不是前端自己记着） */
await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('#mine:not([hidden])', { timeout: 10000 });
const mineAfterReload = (await page.locator('#mine-list .oitem strong').first().textContent()) || '';
step('刷新后认领还在', mineAfterReload.trim() === claimTitle.trim(), mineAfterReload.trim());
step('刷新后大厅仍没有这一单', !(await page.locator('.task .tlink').allTextContents()).includes(claimTitle.trim()), '');

/* 9. 手机尺寸（带上登录态，否则大厅会把人送回登录页） */
const saved = await ctx.storageState();
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, storageState: saved });
const mp = await m.newPage();
await mp.goto(BASE + '/static/app/hall.html', { waitUntil: 'networkidle' });
await mp.waitForSelector('.task', { timeout: 8000 }).catch(() => {});
step('手机尺寸也渲染出卡片', (await mp.locator('.task').count()) >= 1);
await mp.screenshot({ path: path.join(OUT, 'hall-mobile.png'), fullPage: true });
step('手机尺寸无横向溢出', await mp.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1));

step('无 JS 报错', errors.length === 0, errors.slice(0, 3).join(' | '));

await browser.close();

const bad = log.filter((l) => l.startsWith('FAIL')).length;
console.log(`\n${log.length - bad} passed, ${bad} failed`);
process.exit(bad ? 1 : 0);

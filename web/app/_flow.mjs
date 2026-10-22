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

/* 8.5 认领：先确认再动手。挑「需求方甲」发的单——
   后面要用他的手机号登录去点头，随便挑一张会对不上人。 */
await page.fill('#q', '');
await page.waitForTimeout(700);
const beforeClaim = await page.locator('.task').count();
const r001 = page.locator('.task', { hasText: '需求方甲' }).first();
const claimTitle = ((await r001.locator('.tlink').textContent()) || '').trim();

await r001.locator('[data-claim]').click();
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

/* 8.7 需求方这一侧：登录 → 看见有人认领 → 点头
   用种子需求方 R001 的手机号，他名下有两单，正好一单点头一单退回。 */
const dctx = await browser.newContext({ viewport: { width: 1440, height: 980 } });
const dp = await dctx.newPage();
dp.on('pageerror', (e) => errors.push('demander: ' + String(e)));
dp.on('console', (m) => {
  if (m.type() === 'error') errors.push('demander: ' + m.text());
});

await dp.goto(BASE + '/static/app/login.html', { waitUntil: 'networkidle' });
await dp.fill('#phone', '13800000001');
await dp.click('#to-role');
await dp.waitForSelector('[data-pane="2"]:not([hidden])');
await dp.click('.pick[data-role="demand"]');
await dp.click('#to-code');
await dp.waitForSelector('[data-pane="3"]:not([hidden])');
await dp.fill('#code', '000000');
await Promise.all([dp.waitForURL(/hall\.html/, { timeout: 10000 }), dp.click('#submit')]);
step('需求方登录后落在大厅', /hall\.html/.test(dp.url()), dp.url());

await dp.waitForSelector('#mine:not([hidden])', { timeout: 10000 });
const dTitle = ((await dp.textContent('#mine-title')) || '').trim();
step('面板对需求方换成「等你确认」', dTitle === '等你确认', dTitle);
const dBanner = ((await dp.textContent('#banner')) || '').trim();
step('横幅说清有几单等他点头', /1 单等你点头/.test(dBanner), dBanner);
const dItem = ((await dp.locator('#mine-list .oitem').first().textContent()) || '').trim();
step('看到接单人的一句话定位', /把重复流程做成能跑的 Agent/.test(dItem), dItem.slice(0, 40));
step('看到自己发的单标题', dItem.includes(claimTitle), claimTitle);
await dp.screenshot({ path: path.join(OUT, 'demand-inbox.png'), fullPage: true });

await dp.locator('[data-ask-confirm]').first().click();
step('点头前先亮后果', (await dp.locator('[data-box^="confirm-"]:not([hidden])').count()) === 1);
await dp.screenshot({ path: path.join(OUT, 'demand-confirm.png'), fullPage: true });
await dp.locator('[data-box^="confirm-"]:not([hidden]) [data-do-confirm]').click();
await dp.waitForTimeout(700);
const dState = ((await dp.locator('#mine-list .oitem .state').first().textContent()) || '').trim();
step('点头之后变进行中', /进行中/.test(dState), dState);
await dp.screenshot({ path: path.join(OUT, 'demand-confirmed.png'), fullPage: true });

/* 8.8 接单方刷新，能看到这一单已经开工 */
await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('#mine:not([hidden])', { timeout: 10000 });
const pState = ((await page.locator('#mine-list .oitem .state').first().textContent()) || '').trim();
step('接单方也看到进行中', /进行中/.test(pState), pState);
await page.screenshot({ path: path.join(OUT, 'provider-confirmed.png'), fullPage: true });

/* 8.85 交付：进行中之后球在接单方这边，不交这一单永远走不动。
   需求清单要先逐项交代，交完单子进待验收，需求方才有东西可看。 */
await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('#mine-list [data-ask-deliver]', { timeout: 10000 });
const pBanner = ((await page.textContent('#banner')) || '').trim();
step('顶部提醒该交付了', /该交付/.test(pBanner), pBanner);

await page.locator('#mine-list [data-ask-deliver]').first().click();
const dbox = page.locator('[data-box^="deliver-"]:not([hidden])');
step('交付前先亮出表单', (await dbox.count()) === 1);
const rows = await dbox.locator('.drow').count();
step('交付按需求清单逐项列出', rows >= 3, `${rows} 项`);
await page.screenshot({ path: path.join(OUT, 'deliver-form.png'), fullPage: true });

await dbox.locator('textarea').fill('RAG 问答系统已跑通，R3 的 Windows 启动脚本还差一版。');
await dbox.locator('input[type="url"]').fill('https://example.com/rag.zip');

// 第三项没做完又不写原因：这一步应该在客户端就被拦住
await dbox.locator('.drow').nth(2).locator('input[type="checkbox"]').uncheck();
await dbox.locator('[data-do-deliver]').click();
await page.waitForTimeout(400);
const guardToast = ((await page.textContent('#toast').catch(() => '')) || '').trim();
step('未完成项不写原因会被拦下', /没做完/.test(guardToast), guardToast);
step('被拦下时单子没被推走', (await page.locator('#mine-list [data-ask-deliver]').count()) === 1);

await dbox.locator('.drow').nth(2).locator('input[type="text"]').fill('Windows 启动脚本明天补上');
await dbox.locator('[data-do-deliver]').click();
await page.waitForTimeout(900);

const pDelState = ((await page.locator('#mine-list .oitem .state').first().textContent()) || '').trim();
step('交付后变成待验收', /待验收/.test(pDelState), pDelState);
const pDelToast = ((await page.textContent('#toast').catch(() => '')) || '').trim();
step('交付给了回执', /已交付/.test(pDelToast), pDelToast);
const shown = ((await page.locator('#mine-list .oitem .dshow').first().textContent()) || '').trim();
step('交付记录里有说明和逐项结果', /RAG 问答系统已跑通/.test(shown) && /未完成/.test(shown), shown.slice(0, 60));
step('交完这一单不再需要他动', (await page.locator('#mine-list [data-ask-deliver]').count()) === 0);
await page.screenshot({ path: path.join(OUT, 'provider-delivered.png'), fullPage: true });

/* 8.86 需求方看到同一份交付 */
await dp.reload({ waitUntil: 'networkidle' });
await dp.waitForTimeout(600);
const dRevState = ((await dp.locator('#mine-list .oitem .state').first().textContent()) || '').trim();
step('需求方看到待验收', /待验收/.test(dRevState), dRevState);
const dShown = ((await dp.locator('#mine-list .oitem .dshow').first().textContent()) || '').trim();
step('需求方读到同一份交付', /RAG 问答系统已跑通/.test(dShown), dShown.slice(0, 60));
await dp.screenshot({ path: path.join(OUT, 'demand-review.png'), fullPage: true });

/* 8.8 验收：先打回一次，接单方按说明重交，需求方再验收通过 */
await dp.locator('#mine-list [data-ask-revise]').first().click();
const rvBox = dp.locator('[data-box^="revise-"]:not([hidden])').first();
await rvBox.locator('textarea').fill('R3 的启动脚本只在 Linux 上验证过，Windows 用户起不来，补一份说明。');
await dp.screenshot({ path: path.join(OUT, 'demand-revise.png'), fullPage: true });
await rvBox.locator('[data-do-revise]').click();
await dp.waitForTimeout(900);
step('打回后这一单回到进行中', (await dp.locator('#mine-list .oitem', { hasText: '进行中' }).count()) >= 1);

await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('#mine-list .oitem', { timeout: 10000 });
const backNote = ((await page.locator('#mine-list .dnote-back').first().textContent().catch(() => '')) || '').trim();
step('接单方看到打回说明', /Windows/.test(backNote), backNote.slice(0, 40));

await page.locator('#mine-list [data-ask-deliver]').first().click();
const dbox2 = page.locator('[data-box^="deliver-"]:not([hidden])').first();
await dbox2.locator('textarea').fill('第二版：补了 Windows 启动脚本与说明。');
await dbox2.locator('[data-do-deliver]').click();
await page.waitForTimeout(900);
step('重交后回到待验收', (await page.locator('#mine-list .oitem', { hasText: '待验收' }).count()) >= 1);
await page.screenshot({ path: path.join(OUT, 'provider-redelivered.png'), fullPage: true });

await dp.reload({ waitUntil: 'networkidle' });
await dp.waitForSelector('#mine-list [data-ask-accept]', { timeout: 10000 });
await dp.locator('#mine-list [data-ask-accept]').first().click();
const acBox = dp.locator('[data-box^="accept-"]:not([hidden])').first();
await dp.screenshot({ path: path.join(OUT, 'demand-accept.png'), fullPage: true });
await acBox.locator('[data-do-accept]').click();
await dp.waitForTimeout(900);
step('验收后这一单已完成', (await dp.locator('#mine-list .oitem', { hasText: '已完成' }).count()) >= 1);
step('完成后需求方这一格不再要动作', (await dp.locator('#mine-list [data-ask-accept]').count()) === 0);
await dp.screenshot({ path: path.join(OUT, 'demand-done.png'), fullPage: true });

/* 8.9 再接一单，被需求方退回大厅 */
const r001next = page.locator('.task', { hasText: '需求方甲' }).first();
const nextTitle = ((await r001next.locator('.tlink').textContent()) || '').trim();
await r001next.locator('[data-claim]').click();
await page.locator('.claim-box:not([hidden]) [data-do]').click();
await page.waitForTimeout(800);
step('接单方第二单进了「我接的单」', (await page.locator('#mine-list .oitem').count()) === 2, nextTitle);

await dp.reload({ waitUntil: 'networkidle' });
await dp.waitForSelector('#mine-list [data-ask-decline]', { timeout: 10000 });
const pend = dp.locator('li.oitem', { has: dp.locator('[data-ask-decline]') }).first();
await pend.locator('[data-ask-decline]').click();
await pend.locator('textarea').fill('工期比我能等的更紧');
await dp.screenshot({ path: path.join(OUT, 'demand-decline.png'), fullPage: true });
await pend.locator('[data-do-decline]').click();
await dp.waitForTimeout(900);

const dToast = ((await dp.textContent('#toast').catch(() => '')) || '').trim();
step('退回有回执', /退回/.test(dToast), dToast);
step('退回的那一单变成已取消', (await dp.locator('#mine-list .oitem', { hasText: '已取消' }).count()) >= 1);
const hallTitles = (await dp.locator('#list .tlink').allTextContents()).map((s) => s.trim());
step('退回后单子重新出现在大厅', hallTitles.includes(nextTitle), nextTitle);

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

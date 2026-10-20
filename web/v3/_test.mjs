import path from 'path';
const { chromium } = await import('file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs');
const dir = 'E:/workspace/project/aijiedan/zanbuddy/web/v3';
const url = (p, q = '') => 'file:///' + path.posix.join(dir, p + '.html') + q;
const browser = await chromium.launch({
  executablePath: 'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe'
});
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
let pass = 0, fail = 0;
const ck = (n, ok, extra = '') => { ok ? pass++ : fail++; console.log((ok ? 'PASS ' : 'FAIL ') + n + (extra ? ' :: ' + extra : '')); };

/* 收集页面报错 */
const errors = [];
function watch(pg, tag) {
  pg.on('pageerror', e => errors.push(tag + ' › ' + e.message));
  pg.on('console', m => { if (m.type() === 'error') errors.push(tag + ' › console: ' + m.text()); });
}

/* 1. 候选人页：3 卡 + 抽屉 */
let page = await ctx.newPage(); watch(page, 'match');
await page.goto(url('match'));
await page.waitForTimeout(400);
await ck('候选人固定 3 张', await page.locator('article.card').count() === 3, String(await page.locator('article.card').count()));
await page.locator('button:has-text("作品与背书全文")').first().click();
await page.waitForTimeout(250);
await ck('抽屉打开', await page.locator('#mask.show').count() === 1);
ck('抽屉含姓名', (await page.locator('#sheetTitle').innerText()).includes('陈默'));
ck('抽屉含订单号溯源', (await page.locator('#sheetBody').innerText()).includes('ZB-2381'));
await page.keyboard.press('Escape');
await page.waitForTimeout(200);
ck('Esc 关闭抽屉', await page.locator('#mask.show').count() === 0);

/* 2. 订单页：背书门槛 */
page = await ctx.newPage(); watch(page, 'flow');
await page.goto(url('flow'));
await page.waitForTimeout(300);
ck('初始按钮禁用', await page.locator('#submitBtn').isDisabled());
await page.locator('.q[data-q="1"]').click();
await page.locator('.q[data-q="2"]').click();
await ck('选 2 问后计数=2', (await page.locator('#qCount').innerText()) === '2');
ck('只选问题仍禁用（字数不足）', await page.locator('#submitBtn').isDisabled());
await page.locator('#body').fill('他按时交付了');
await page.waitForTimeout(150);
ck('10 字仍禁用', await page.locator('#submitBtn').isDisabled(), (await page.locator('#charCount').innerText()));
ck('提示还差 6 字（6 字 < 12 字门槛）', (await page.locator('#why').innerText()).includes('还差 6 字'), await page.locator('#why').innerText());
await page.locator('#body').fill('他把退款审批做成了一个能自己跑的 Agent，回测一致率 88%。');
await page.waitForTimeout(150);
ck('字数达标 + 2 问 → 可提交', await page.locator('#submitBtn').isEnabled());
await page.locator('.q[data-q="2"]').click();
await page.waitForTimeout(150);
ck('只选 1 问 → 又禁用', await page.locator('#submitBtn').isDisabled());
ck('提示还差 1 个问题', (await page.locator('#why').innerText()).includes('还差 1 个问题'), await page.locator('#why').innerText());

/* 3. 登录页：校验与角色 */
page = await ctx.newPage(); watch(page, 'auth');
await page.goto(url('auth'));
await page.waitForTimeout(200);
ck('登录按钮初始禁用', await page.locator('#go').isDisabled());
await page.locator('#phone').fill('123');
await page.waitForTimeout(100);
ck('号码不合法时报错', await page.locator('#phone-err').isVisible());
await page.locator('#phone').fill('13800138000');
await page.locator('#code').fill('123456');
await page.waitForTimeout(100);
ck('号码+验证码齐全 → 可登录', await page.locator('#go').isEnabled());
ck('未发码时不问身份', await page.locator('#role-block').isHidden());
await page.locator('#send').click();
await page.waitForTimeout(200);
ck('发码后出现身份选择', await page.locator('#role-block').isVisible());
ck('发码按钮进入倒计时', /秒/.test(await page.locator('#send').innerText()), await page.locator('#send').innerText());
await page.locator('.pick[data-role="supply"]').click();
await page.waitForTimeout(100);
ck('选中供给方', await page.locator('.pick[data-role="supply"]').getAttribute('aria-checked') === 'true');
ck('角色为单选（需求方自动取消）', await page.locator('.pick[data-role="demand"]').getAttribute('aria-checked') === 'false');

/* 4. 发单页：三步 + 必填拦截 */
page = await ctx.newPage(); watch(page, 'post');
await page.goto(url('post'));
await page.waitForTimeout(200);
await page.locator('#next').click();
await page.waitForTimeout(100);
ck('空输入 → 拦截', await page.locator('#e1').isVisible());
ck('仍在第一步', await page.locator('#s1').isVisible());
await page.locator('#brief').fill('做一个退款审批 Agent');
await page.waitForTimeout(100);
ck('字数够 → 错误消失', await page.locator('#e1').isHidden());
await page.locator('#next').click();
await page.waitForTimeout(200);
ck('出现加载态', await page.locator('#s2-loading').isVisible());
await page.waitForTimeout(900);
ck('加载后出结果', await page.locator('#s2').isVisible());
ck('步骤条走到第 2 步', await page.locator('.steps .s[data-step="2"]').getAttribute('class').then(c => c.includes('on')));
// 原话没写验收标准 → 标红且清空
ck('缺验收标准时标红', await page.locator('#warn-accept').isVisible());
ck('缺验收标准时清空该字段', (await page.locator('#r2').inputValue()) === '');
await page.locator('#publish').click();
await page.waitForTimeout(200);
ck('缺必填 → 不发布', await page.locator('#s2').isVisible());
await page.locator('#r2').fill('输入订单号能输出退或不退，并给理由');
await page.locator('#publish').click();
await page.waitForTimeout(300);
ck('补齐 → 发布成功', await page.locator('#s3').isVisible());
ck('发布页有去候选人的出口', await page.locator('#s3 a[href="match.html"]').count() === 1);

/* 5. 发单页：从首页带过来的那句话 */
page = await ctx.newPage(); watch(page, 'post-brief');
await page.goto(url('post', '?brief=' + encodeURIComponent('把客服的退款审批做成能自动跑的 Agent，输入订单号就能判断该退不该退')));
await page.waitForTimeout(200);
ck('首页那句话被带过来', (await page.locator('#brief').inputValue()).includes('退款审批'));

/* 6. 档案页：实时预览 */
page = await ctx.newPage(); watch(page, 'profile');
await page.goto(url('profile'));
await page.waitForTimeout(200);
ck('预览先显示当前定位', (await page.locator('#pv-bio').innerText()).includes('Agent'));
await page.locator('#bio').fill('把客服流程做成能自己跑的东西');
await page.waitForTimeout(100);
ck('改定位 → 预览同步', (await page.locator('#pv-bio').innerText()).includes('客服流程'));
ck('字数计数同步', (await page.locator('#c-bio').innerText()).includes(String('把客服流程做成能自己跑的东西'.length)));
const before = await page.locator('#pv-skills .tag').count();
await page.locator('#skills .chip:has-text("RAG")').click();
await page.waitForTimeout(100);
ck('加技能 → 预览多一个标签', await page.locator('#pv-skills .tag').count() === before + 1);
await page.locator('#avail button:has-text("暂停接单")').click();
await page.waitForTimeout(100);
ck('接单状态可切换', (await page.locator('.state').innerText()).includes('暂停接单'));

/* 7. 工作台：双身份 */
page = await ctx.newPage(); watch(page, 'app');
await page.goto(url('app'));
await page.waitForTimeout(200);
ck('默认需求方视角', await page.locator('#view-demand').isVisible() && await page.locator('#view-supply').isHidden());
await page.locator('#role-seg button:has-text("供给方")').click();
await page.waitForTimeout(150);
ck('切到供给方视角', await page.locator('#view-supply').isVisible() && await page.locator('#view-demand').isHidden());
ck('供给方标题变了', (await page.locator('#h1').innerText()).includes('交付'));
ck('供给方隐藏匿名标记', await page.locator('#antag').isHidden());

/* 8. 全站：横向溢出 + 无报错 */
const pages = ['index', 'auth', 'post', 'match', 'flow', 'credit', 'app', 'profile', 'system'];
for (const w of [1440, 768, 390]) {
  const c = await browser.newContext({ viewport: { width: w, height: 900 } });
  for (const p of pages) {
    const pg = await c.newPage();
    await pg.goto(url(p));
    await pg.waitForTimeout(180);
    const over = await pg.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    ck(`${p} @${w} 无横向溢出`, over <= 1, 'overflow=' + over);
    await pg.close();
  }
  await c.close();
}

ck('全站无 JS 报错', errors.length === 0, errors.slice(0, 5).join(' | '));

console.log(`\n=== ${pass} passed, ${fail} failed ===`);
await browser.close();
process.exit(fail ? 1 : 0);

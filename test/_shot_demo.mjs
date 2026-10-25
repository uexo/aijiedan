/**
 * 给演示现场截一张图：需求方登录 → 大厅 → 我的单 → 打开背书框（不提交）。
 *
 * 单独一支而不是塞进 _verify_endorse.mjs：验收用例必须把背书写完才算验完，
 * 截图要的恰恰是「还没写」那一刻的界面。两个目的相反，硬合到一起会互相将就。
 *
 * 跑法: node test/_shot_demo.mjs   （服务需在 8082，库为已铺好的演示现场）
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
const ctx = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
const page = await ctx.newPage();

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

await page.waitForSelector('#mine-list li', { timeout: 10000 });
await page.locator('[data-ask-endorse]').first().waitFor({ state: 'attached', timeout: 8000 });
await page.locator('[data-act]').first().evaluate((n) => (n.hidden = false));
await page.locator('[data-ask-endorse]').first().click({ force: true });
await page.locator('[data-box^="endorse-"]').first().waitFor({ state: 'visible' });
await page.waitForTimeout(400);

// 滚到背书框，别把整页几千像素都截进来
await page.locator('[data-box^="endorse-"]').first().scrollIntoViewIfNeeded();
await page.waitForTimeout(300);
await page.screenshot({ path: `${OUT}/demo-endorse-blank.png`, fullPage: false });
console.log('saved demo-endorse-blank.png');

// 填一半再截一张，展示字数提示与三问
const ta = page.locator('[data-ebody]').first();
await ta.fill('这个脚本能读抖音导出的表，我拿上周的数据跑了一遍');
await page.locator('[data-eans="delivered"]').fill('对得上，三条都做了');
await page.waitForTimeout(300);
await page.screenshot({ path: `${OUT}/demo-endorse-filled.png`, fullPage: false });
console.log('saved demo-endorse-filled.png');

await browser.close();

const { chromium } = await import('file:///C:/Users/admin/.workbuddy/binaries/node/workspace/node_modules/playwright/index.mjs');
const BASE = process.env.BASE || 'http://127.0.0.1:8082';
const browser = await chromium.launch({
  executablePath: 'C:/Users/admin/AppData/Local/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-win64/chrome-headless-shell.exe',
});
const page = await browser.newPage();

let pass = 0, fail = 0;
const check = (name, ok) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}`); ok ? pass++ : fail++; };

await page.goto(`${BASE}/static/landing.html`, { waitUntil: 'networkidle' });
const hrefs = await page.$$eval('a[href*="login.html"]', as => as.map(a => a.getAttribute('href')));
check('落地页有指向 v2 登录页的入口', hrefs.length > 0);
check('落地页不再指向 v1 auth.html', (await page.$$('a[href="auth.html"]')).length === 0);

await page.goto(`${BASE}/static/app/login.html?role=demand`, { waitUntil: 'networkidle' });
await page.waitForSelector('[data-role="demand"]', { state: 'attached' });
const d1 = await page.getAttribute('[data-role="demand"]', 'aria-checked');
const s1 = await page.getAttribute('[data-role="supply"]', 'aria-checked');
check('?role=demand 预选了需求方', d1 === 'true');
check('?role=demand 未选接单方', s1 !== 'true');

await page.goto(`${BASE}/static/app/login.html?role=supply`, { waitUntil: 'networkidle' });
await page.waitForSelector('[data-role="supply"]', { state: 'attached' });
const s2 = await page.getAttribute('[data-role="supply"]', 'aria-checked');
check('?role=supply 预选了接单方', s2 === 'true');

await page.goto(`${BASE}/static/app/login.html`, { waitUntil: 'networkidle' });
const d3 = await page.getAttribute('[data-role="demand"]', 'aria-checked');
check('不带参数时不预选', d3 !== 'true');

/* 一个真人第一次登录。演示环境没有短信通道, 后端把码放在 dev_code 里返回,
   页面必须显示它——不显示的话, 用户看到"已发送至 138****1234"，手机永远收不到,
   卡在第三步。这一步断了, 后面六步一步都走不到, 所以它比任何界面细节都重要。 */
const phone = '135' + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');
await page.goto(`${BASE}/static/app/login.html?role=demand`, { waitUntil: 'networkidle' });
await page.fill('#phone', phone);
await page.click('#to-role');
await page.waitForSelector('[data-pane="2"]:not([hidden])');
await page.click('#to-code');
await page.waitForSelector('[data-pane="3"]:not([hidden])');
await page.waitForSelector('#dev-code:not([hidden])', { timeout: 5000 }).catch(() => {});
const devText = ((await page.textContent('#dev-code')) || '').replace(/\s+/g, ' ');
check('第三步把演示验证码显示出来', /验证码是\s*0{6}/.test(devText), devText.trim());

await page.click('#fill-code');
check('一键填入填进了输入框', (await page.inputValue('#code')) === '000000');

await Promise.all([
  page.waitForURL(/(hall|profile)\.html/, { timeout: 15000 }),
  page.click('#submit'),
]);
check('用页面给的验证码真的登进去了', /(hall|profile)\.html/.test(page.url()), page.url());

await browser.close();
console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);

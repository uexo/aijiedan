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

await browser.close();
console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);

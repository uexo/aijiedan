import { api, html, toast } from './runtime.js';

/* 发单页。校验口径与后端 internal/demand 一致, 但文案不在这里重复写:
   前端只做"空着就点不动"和字数计数, 到底合不合规由服务端说了算。
   两处各写一套提示, 早晚会漂成两种说法。 */

const MIN_TITLE = 4;
const MAX_TITLE = 60;
const MIN_SPEC = 20;
const MAX_ITEMS = 12;

const form = document.getElementById('f');
const errBox = document.getElementById('err');
const whoEl = document.getElementById('who');
const catEl = document.getElementById('category');
const titleEl = document.getElementById('title');
const specEl = document.getElementById('spec');
const budgetEl = document.getElementById('budget');
const deliverablesEl = document.getElementById('deliverables');
const formatEl = document.getElementById('format');
const styleEl = document.getElementById('style');
const deadlineEl = document.getElementById('deadline');
const itemsEl = document.getElementById('items');
const addItemBtn = document.getElementById('add-item');
const submitBtn = document.getElementById('submit');
const doneSec = document.getElementById('done');

let submitting = false;

/* ---------- 身份 ---------- */

async function boot() {
  try {
    const me = await api('GET', '/me');
    const name = (me.user && me.user.name) || '（还没起昵称）';
    whoEl.textContent = name + ' · 以需求方身份发布';
  } catch (e) {
    // 401 已被 runtime 带去登录页, 走到这里说明是别的错
    whoEl.textContent = '身份没确认上，仍可尝试发布';
  }
  loadCategories();
  addItemRow();
  addItemRow();
  addItemRow();
}

async function loadCategories() {
  try {
    const d = await api('GET', '/meta/categories');
    const items = (d && d.items) || [];
    catEl.innerHTML =
      '<option value="">选一个类目</option>' +
      items.map((c) => html`<option value="${c.id}">${c.label}</option>`).join('');
  } catch (e) {
    catEl.innerHTML = '<option value="">类目加载失败，请刷新</option>';
  }
}

/* ---------- 字数计数 ---------- */

const counters = [
  [titleEl, 'c-title', MAX_TITLE],
  [specEl, 'c-spec', 2000],
  [deliverablesEl, 'c-deliverables', 80],
  [formatEl, 'c-format', 40],
  [styleEl, 'c-style', 80],
];

for (const [input, id, max] of counters) {
  const el = document.getElementById(id);
  const update = () => {
    el.textContent = `${[...input.value].length} / ${max}`;
  };
  input.addEventListener('input', update);
  update();
}

/* ---------- 验收清单 ---------- */

function addItemRow() {
  const rows = itemsEl.querySelectorAll('.item-row').length;
  if (rows >= MAX_ITEMS) {
    toast('最多 12 项，太多了验收也没法逐项对', 'warn');
    return;
  }
  const row = document.createElement('div');
  row.className = 'item-row flex ac g8';
  row.innerHTML = html`
    <span class="mono tiny muted item-no"></span>
    <input class="input" maxlength="100" placeholder="比如：能读 30 天订单导出的 xlsx">
    <button class="btn btn-quiet btn-sm" type="button" data-del aria-label="删除这一项">删</button>`;
  row.querySelector('[data-del]').addEventListener('click', () => {
    row.remove();
    renumber();
  });
  itemsEl.appendChild(row);
  renumber();
}

function renumber() {
  const rows = [...itemsEl.querySelectorAll('.item-row')];
  rows.forEach((r, i) => {
    r.querySelector('.item-no').textContent = 'R' + (i + 1);
  });
  document.getElementById('c-items').textContent = `${rows.length} / ${MAX_ITEMS}`;
  addItemBtn.disabled = rows.length >= MAX_ITEMS;
}

function collectItems() {
  return [...itemsEl.querySelectorAll('.item-row input')]
    .map((i) => i.value.trim())
    .filter((v) => v !== '');
}

addItemBtn.addEventListener('click', addItemRow);

/* ---------- 提交 ---------- */

function showErr(msg, hint) {
  errBox.innerHTML = html`<strong>${msg}</strong>${hint ? html`<span class="mt4">${hint}</span>` : ''}`;
  errBox.hidden = false;
  errBox.scrollIntoView({ block: 'center', behavior: 'smooth' });
}

function clearErr() {
  errBox.hidden = true;
}

/* 元 → 分。浮点乘 100 会出现 0.1*100=10.000000000000002,
   四舍五入到整数分, 别把 8.7 元变成 869 分。 */
function yuanToCents(raw) {
  const v = String(raw).trim().replace(/[，,\s]/g, '');
  if (v === '') return null;
  const n = Number(v);
  if (!isFinite(n) || n <= 0) return null;
  return Math.round(n * 100);
}

/* 日期 → 当天 23:59:59 的时间戳。"10 月 30 日交"指的是那天过完,
   不是那天开始——按 00:00 算等于提前一天截止。 */
function deadlineToMs(raw) {
  if (!raw) return 0;
  const d = new Date(raw + 'T23:59:59');
  const t = d.getTime();
  return isFinite(t) ? t : 0;
}

function clientCheck() {
  const title = titleEl.value.trim();
  const spec = specEl.value.trim();
  if ([...title].length < MIN_TITLE) return '标题至少 4 个字，说清要做什么';
  if (!catEl.value) return '选一个类目，人家才知道从哪儿找起';
  if (yuanToCents(budgetEl.value) === null) return '预算填个数字，最少 1 元';
  if ([...spec].length < MIN_SPEC) return '需求说明至少 20 个字，不然没人敢接';
  return '';
}

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  if (submitting) return;
  clearErr();

  const bad = clientCheck();
  if (bad) {
    showErr(bad);
    return;
  }

  const body = {
    title: titleEl.value.trim(),
    category: catEl.value,
    spec: specEl.value.trim(),
    budget_cents: yuanToCents(budgetEl.value),
    deadline_at: deadlineToMs(deadlineEl.value),
    deliverables: deliverablesEl.value.trim(),
    required_format: formatEl.value.trim(),
    style_ref: styleEl.value.trim(),
    samples: '',
    items: collectItems(),
  };

  submitting = true;
  submitBtn.disabled = true;
  submitBtn.textContent = '发布中…';

  try {
    const v = await api('POST', '/tasks', body);
    onPublished(v);
  } catch (err) {
    // 服务端说哪儿不对就说哪儿, 不翻译成自己的话——两边文案不一致时,
    // 用户照着前端提示改会被服务端再拒一次。
    showErr(err.message || '没发出去，请再试一次', err.hint || '');
  } finally {
    submitting = false;
    submitBtn.disabled = false;
    submitBtn.textContent = '发布需求';
  }
});

function onPublished(v) {
  form.hidden = true;
  doneSec.hidden = false;
  document.getElementById('done-id').textContent = v.id;
  document.getElementById('done-go').href = 'hall.html?new=' + encodeURIComponent(v.id);

  // 澄清提示是"还能补什么", 不是错误: 单子已经发出去了, 别让人以为要重发。
  const clar = v.clarifications || [];
  const box = document.getElementById('done-clar');
  if (clar.length) {
    box.innerHTML = html`
      <strong>还能补这几项，补了更容易被接：</strong>
      <ul class="tb-list mt8">${clar.map((c) => html`<li>${c}</li>`).join('')}</ul>`;
    box.hidden = false;
  } else {
    box.hidden = true;
  }
  doneSec.scrollIntoView({ block: 'start', behavior: 'smooth' });
}

document.getElementById('done-again').addEventListener('click', () => {
  form.reset();
  itemsEl.innerHTML = '';
  addItemRow();
  addItemRow();
  addItemRow();
  form.hidden = false;
  doneSec.hidden = true;
  window.scrollTo({ top: 0, behavior: 'smooth' });
});

document.getElementById('logout').addEventListener('click', async () => {
  try {
    await api('POST', '/auth/logout');
  } catch (e) {
    /* 退出失败也让用户走, 服务端会话过期与否都不该卡住 */
  }
  location.replace('login.html');
});

boot();

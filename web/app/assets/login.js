import { api, ApiError, html, validate, toast } from './runtime.js';

const form = document.getElementById('f');
const panes = {
  1: document.querySelector('[data-pane="1"]'),
  2: document.querySelector('[data-pane="2"]'),
  3: document.querySelector('[data-pane="3"]'),
};
const phoneEl = document.getElementById('phone');
const codeEl = document.getElementById('code');
const errBox = document.getElementById('form-err');
const rolePick = document.getElementById('roles');
const nextBtn = document.getElementById('to-code');
const resendBtn = document.getElementById('resend');

const PHONE_RULE = {
  required: true,
  pattern: /^1[3-9]\d{9}$/,
  message: '手机号是 11 位，1 开头',
};
const CODE_RULE = {
  required: true,
  pattern: /^\d{6}$/,
  message: '验证码是 6 位数字',
};

let step = 1;
let role = '';
let sentTo = '';
let left = 0;
let timer = 0;

function renderStep(n) {
  step = n;
  Object.keys(panes).forEach((k) => {
    panes[k].hidden = Number(k) !== n;
  });
  document.querySelectorAll('#steps .s').forEach((el) => {
    const i = Number(el.dataset.step);
    el.classList.toggle('on', i === n);
    el.classList.toggle('done', i < n);
  });
  showErr(null);
  if (n === 1) phoneEl.focus();
  if (n === 3) codeEl.focus();
}

function markField(name, msg) {
  const el = form.elements[name];
  if (!el) return;
  const field = el.closest('.field');
  if (!field) return;
  let box = field.querySelector(':scope > .err');
  if (!box) {
    box = document.createElement('div');
    box.className = 'err';
    field.appendChild(box);
  }
  box.textContent = msg;
  box.hidden = false;
  el.setAttribute('aria-invalid', 'true');
}

function showErr(err) {
  if (!err) {
    errBox.hidden = true;
    errBox.textContent = '';
    return;
  }
  const hint = err instanceof ApiError ? err.hint : '';
  const code = err instanceof ApiError ? err.code : 'ERROR';
  const trace = err instanceof ApiError ? err.traceId : '';
  errBox.innerHTML = html`
    <h4>${err.message || '请求失败'}</h4>
    ${hint ? html`<p>${hint}</p>` : ''}
    <p class="tiny mono mt8">${code}${trace ? ' · trace ' + trace : ''}</p>`;
  errBox.hidden = false;

  // 信封里的 field 直接标到对应输入框，用户不用自己猜是哪个字段
  if (err instanceof ApiError && err.field) markField(err.field, err.message);
}

function busy(btn, on, label) {
  if (!btn) return;
  if (on) {
    btn.dataset.label = btn.textContent;
    btn.textContent = label || '处理中…';
    btn.disabled = true;
  } else {
    if (btn.dataset.label) btn.textContent = btn.dataset.label;
    btn.disabled = false;
  }
}

function tick() {
  if (left <= 0) {
    clearInterval(timer);
    resendBtn.disabled = false;
    resendBtn.textContent = '重新获取';
    return;
  }
  resendBtn.disabled = true;
  resendBtn.textContent = left + ' 秒';
  left -= 1;
}

function startCountdown() {
  left = 60;
  clearInterval(timer);
  tick();
  timer = setInterval(tick, 1000);
}

async function sendCode(btn) {
  const result = validate(form, { phone: PHONE_RULE });
  if (!result.valid) return false;
  if (btn) busy(btn, true, '发送中…');
  try {
    await api('POST', '/auth/code', { phone: phoneEl.value.trim() });
    sentTo = phoneEl.value.trim();
    document.getElementById('sent-to').textContent = sentTo.replace(/^(\d{3})\d{4}(\d{4})$/, '$1****$2');
    startCountdown();
    toast('验证码已发送', 'ok');
    return true;
  } catch (e) {
    showErr(e);
    return false;
  } finally {
    if (btn) busy(btn, false);
  }
}

phoneEl.addEventListener('input', () => {
  phoneEl.value = phoneEl.value.replace(/\D/g, '').slice(0, 11);
  if (errBox && !errBox.hidden) showErr(null);
});

codeEl.addEventListener('input', () => {
  codeEl.value = codeEl.value.replace(/\D/g, '').slice(0, 6);
});

document.getElementById('to-role').addEventListener('click', async () => {
  if (await sendCode(document.getElementById('to-role'))) renderStep(2);
});

document.getElementById('back-1').addEventListener('click', () => renderStep(1));
document.getElementById('back-2').addEventListener('click', () => renderStep(1));

rolePick.addEventListener('click', (e) => {
  const el = e.target.closest('.pick');
  if (!el) return;
  role = el.dataset.role;
  rolePick.querySelectorAll('.pick').forEach((o) => {
    o.setAttribute('aria-checked', o === el ? 'true' : 'false');
  });
  nextBtn.disabled = false;
});

rolePick.addEventListener('keydown', (e) => {
  if (e.key !== ' ' && e.key !== 'Enter') return;
  const el = e.target.closest('.pick');
  if (!el) return;
  e.preventDefault();
  el.click();
});

nextBtn.addEventListener('click', () => {
  if (!role) {
    toast('先选一个身份', 'warn');
    return;
  }
  renderStep(3);
});

resendBtn.addEventListener('click', async () => {
  if (left > 0) return;
  if (!(await sendCode(null))) renderStep(1);
});

function isSupply(r) {
  return /supply|provider|接单|供给/i.test(String(r || ''));
}

function nextTarget() {
  const raw = new URLSearchParams(location.search).get('next') || '';
  // 只接受站内绝对路径：挡掉 //evil.com 和 /\evil.com（浏览器会把反斜杠当斜杠）
  return /^\/(?!\/)[^\\\s]*$/.test(raw) ? raw : '';
}

// 服务端给的落点（如 /static/app/hall.html）优先：它知道这人建过档没有。
// 只认同目录的页面文件名，别的形状一律忽略。
function serverNext(data) {
  const raw = String((data && data.next) || '');
  const m = /^\/static\/app\/([a-z0-9-]+\.html)$/i.exec(raw);
  return m ? m[1] : '';
}

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  if (step !== 3) return;
  const result = validate(form, { code: CODE_RULE });
  if (!result.valid) return;

  const submit = document.getElementById('submit');
  busy(submit, true, '正在进入…');
  try {
    const data = await api('POST', '/auth/login', {
      phone: sentTo || phoneEl.value.trim(),
      code: codeEl.value.trim(),
      role: role,
    });
    const returned = (data && (data.role || data.role_pref)) || (data && data.user && data.user.role) || '';
    // 接单方建过档就直接进大厅找单；没建过档先去档案页，填完会自己落到大厅
    const supply = isSupply(returned || role);
    const fallback = supply && !(data && data.onboarded) ? 'profile.html' : 'hall.html';
    location.replace(nextTarget() || serverNext(data) || fallback);
  } catch (err) {
    showErr(err);
    busy(submit, false);
  }
});

renderStep(1);

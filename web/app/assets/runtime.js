/**
 * zanbuddy 前端运行时 —— 零依赖 ES module
 * 只做四件事：拿数据（api）、拼 DOM（html）、查表单（validate）、给反馈（toast）。
 * 契约见《数据模型与接口契约-v2》第四节：/ 开头的路径自动补 /api/v2。
 */

const PREFIX = '/api/v2';
const CSRF_COOKIE = 'csrf_token';
let redirected = false;

export class ApiError extends Error {
  constructor(code, message, info) {
    const i = info || {};
    super(message || code);
    this.name = 'ApiError';
    this.code = code;
    this.message = message || code;
    this.hint = i.hint || '';
    this.field = i.field || '';
    this.status = i.status || 0;
    this.traceId = i.traceId || '';
  }
  toString() {
    return this.code + ': ' + this.message;
  }
}

function readCookie(name) {
  const m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
  return m ? decodeURIComponent(m[1]) : '';
}

function uuid() {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID();
  const b = new Uint8Array(16);
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(b);
  else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((n) => n.toString(16).padStart(2, '0')).join('');
  return h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' + h.slice(16, 20) + '-' + h.slice(20);
}

function toLogin() {
  if (redirected) return;
  redirected = true;
  if (/\/login\.html$/.test(location.pathname)) return;
  const here = location.pathname + location.search;
  location.replace('login.html?next=' + encodeURIComponent(here));
}

/**
 * api(method, path, body, opts)
 * - path 以 / 开头 → 拼 /api/v2；否则原样使用（便于直连 v1 或绝对地址）
 * - 成功返回信封里的 data；失败一律抛 ApiError，调用方只需 try/catch
 * - opts: {idempotencyKey: true|string, timeout: ms, headers: {}, signal}
 */
export async function api(method, path, body, opts) {
  const o = opts || {};
  const url = path.charAt(0) === '/' ? PREFIX + path : path;
  const headers = Object.assign({ Accept: 'application/json' }, o.headers || {});
  const init = {
    method: String(method || 'GET').toUpperCase(),
    credentials: 'same-origin',
    headers: headers,
  };

  if (body !== undefined && body !== null) {
    if (typeof FormData !== 'undefined' && body instanceof FormData) {
      init.body = body;
    } else {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
  }

  const csrf = readCookie(CSRF_COOKIE);
  if (csrf) headers['X-CSRF-Token'] = csrf;

  if (o.idempotencyKey) {
    headers['Idempotency-Key'] = o.idempotencyKey === true ? uuid() : String(o.idempotencyKey);
  }

  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), o.timeout || 20000);
  init.signal = o.signal || ctl.signal;

  let res;
  try {
    res = await fetch(url, init);
  } catch (e) {
    throw new ApiError('NETWORK', e && e.name === 'AbortError' ? '请求超时，请重试' : '网络连接失败，检查一下网络');
  } finally {
    clearTimeout(timer);
  }

  let text = '';
  try {
    text = await res.text();
  } catch (e) {
    text = '';
  }

  let env = null;
  try {
    env = text ? JSON.parse(text) : null;
  } catch (e) {
    env = null;
  }

  // 401 优先处理：即便响应体不是信封（网关/旧接口），也要回登录页
  if (res.status === 401) toLogin();

  if (!env || typeof env.ok !== 'boolean') {
    throw new ApiError(
      res.ok ? 'BAD_RESPONSE' : 'HTTP_' + res.status,
      res.ok ? '服务端返回的不是约定的信封格式' : '请求失败（HTTP ' + res.status + '）',
      { status: res.status }
    );
  }

  if (!env.ok) {
    const e = env.error || {};
    throw new ApiError(e.code || 'INTERNAL', e.message || '请求失败', {
      hint: e.hint,
      field: e.field,
      status: res.status,
      traceId: env.trace_id,
    });
  }

  return env.data;
}

/* ---------- 模板 ---------- */

export function esc(str) {
  return String(str === null || str === undefined ? '' : str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

const RAW = Symbol('raw');

/** raw() 跳过转义，只用于已经可信的片段（自己拼的 html`` 或常量）。 */
export function raw(value) {
  return { [RAW]: true, value: value };
}

function piece(v) {
  if (v === null || v === undefined || v === false) return '';
  // raw() 是普通对象，html`` 返回的是带标记的 String，两者都要原样输出
  if (v && v[RAW]) return v instanceof String ? String(v) : String(v.value === null || v.value === undefined ? '' : v.value);
  if (Array.isArray(v)) return v.map(piece).join('');
  if (typeof v === 'object') return esc(JSON.stringify(v));
  return esc(v);
}

/**
 * html`<p>${userInput}</p>`
 * 返回带安全标记的字符串对象：插值默认转义，嵌套 html`` 不重复转义。
 */
export function html(strings) {
  let out = strings[0];
  for (let i = 1; i < arguments.length; i++) {
    out += piece(arguments[i]) + strings[i];
  }
  const boxed = new String(out);
  boxed[RAW] = true;
  return boxed;
}

/* ---------- 表单校验 ---------- */

function fieldOf(el) {
  return el ? el.closest('.field') || el.parentElement : null;
}

function setFieldErr(field, msg) {
  if (!field) return;
  let box = field.querySelector(':scope > .err');
  if (!box) {
    box = document.createElement('div');
    box.className = 'err';
    field.appendChild(box);
  }
  box.textContent = msg;
  box.hidden = !msg;
}

/**
 * validate(formEl, rules)
 * rules: {field: {required, min, max, pattern, message, label}}
 * 返回 {valid, errors}，同时把红字写进对应 .field 里的 .err。
 */
export function validate(formEl, rules) {
  const errors = {};
  let first = null;

  Object.keys(rules).forEach((name) => {
    const rule = rules[name];
    const el = formEl.elements[name] || formEl.querySelector('[name="' + name + '"]');
    const field = fieldOf(el);
    const label =
      rule.label ||
      (field && field.querySelector('.label') ? field.querySelector('.label').textContent.replace(/[*＊\s]/g, '') : name);
    const value = el ? String(el.value === null || el.value === undefined ? '' : el.value).trim() : '';
    let msg = '';

    if (!el) {
      msg = '找不到字段 ' + name;
    } else if (rule.required && !value) {
      msg = rule.message || label + '不能为空';
    } else if (value && rule.min !== undefined && value.length < rule.min) {
      msg = rule.message || label + '至少 ' + rule.min + ' 个字符';
    } else if (value && rule.max !== undefined && value.length > rule.max) {
      msg = rule.message || label + '最多 ' + rule.max + ' 个字符';
    } else if (value && rule.pattern && !rule.pattern.test(value)) {
      msg = rule.message || label + '格式不对';
    }

    if (el) el.setAttribute('aria-invalid', msg ? 'true' : 'false');
    setFieldErr(field, msg);
    if (msg) {
      errors[name] = msg;
      if (!first && el) first = el;
    }
  });

  // 校验同时清掉不属于本字段的历史红字由各字段自身负责；这里只保证首错可聚焦
  if (first) first.focus();
  return { valid: Object.keys(errors).length === 0, errors: errors };
}

/* ---------- 提示 ---------- */

let toastEl = null;
let toastTimer = 0;

/** toast(msg, 'ok'|'err'|'warn')：右下角，3 秒，同一时刻只留一条。 */
export function toast(msg, type) {
  if (!toastEl) {
    toastEl = document.getElementById('toast');
    if (!toastEl) {
      toastEl = document.createElement('div');
      toastEl.id = 'toast';
      document.body.appendChild(toastEl);
    }
  }
  const kind = type === 'err' ? 'err' : type === 'warn' ? 'warn' : 'ok';
  toastEl.className = 'toast toast-' + kind;
  toastEl.setAttribute('role', 'status');
  toastEl.setAttribute('aria-live', 'polite');
  toastEl.textContent = String(msg);
  // 先移除再加，保证连续两条也能重放同一段位移动画
  toastEl.classList.remove('show');
  void toastEl.offsetWidth;
  toastEl.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toastEl.classList.remove('show'), 3000);
}

/* ---------- 剪贴板 ---------- */

/* 复制一段文字。返回 'ok' | 'pick' | 'empty'。
   优先 navigator.clipboard（https / localhost 才有），局域网 http 打开时它不存在，
   退回到选中文本——让用户长按复制，而不是弹一句"复制失败"了事。
   pickEl 是"复制不成时该被选中的那个元素"。这段逻辑必须只有一份：
   发单成功页和我的单要复制的是同一句话，两份实现早晚一份记得加 fallback、
   另一份忘，而忘了的那一份在所有手机上都是死按钮、在桌面上却一切正常。 */
export async function copyText(text, pickEl) {
  if (!text) return 'empty';
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.top = '-1000px';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      if (!ok) throw new Error('copy failed');
    }
    return 'ok';
  } catch (e) {
    if (pickEl) {
      pickEl.classList.add('is-pick');
      const r = document.createRange();
      r.selectNodeContents(pickEl);
      const sel = window.getSelection();
      if (sel) {
        sel.removeAllRanges();
        sel.addRange(r);
      }
    }
    return 'pick';
  }
}

/* ---------- 数字 ---------- */

function group(intPart) {
  return intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** num(1234567) → "1,234,567" */
export function num(n) {
  const v = Number(n);
  if (!Number.isFinite(v)) return '0';
  const parts = Math.abs(v).toString().split('.');
  return (v < 0 ? '-' : '') + group(parts[0]) + (parts[1] ? '.' + parts[1] : '');
}

/** money(300000) → "¥3,000.00" */
export function money(cents) {
  const v = Number(cents);
  const fixed = (Math.abs(Number.isFinite(v) ? v : 0) / 100).toFixed(2);
  const parts = fixed.split('.');
  return (v < 0 ? '-¥' : '¥') + group(parts[0]) + '.' + parts[1];
}

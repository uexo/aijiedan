import { api, html, validate, toast } from './runtime.js';

/* 技能目录：value 是入库的 slug，label 只用于显示 */
const CATALOG = [
  ['python', 'Python'],
  ['llm', 'LLM 应用'],
  ['rag', 'RAG'],
  ['agent', 'Agent 编排'],
  ['automation', '工作流自动化'],
  ['api', 'API 对接'],
  ['frontend', '前端'],
  ['data', '数据清洗'],
  ['ops', '部署运维'],
  ['design', '界面设计'],
  ['writing', '技术写作'],
  ['ecom', '电商系统'],
];

const form = document.getElementById('f');
const headlineEl = document.getElementById('headline');
const bioEl = document.getElementById('bio');
const skillsEl = document.getElementById('skills');
const pfEl = document.getElementById('portfolio');
const pfEmpty = document.getElementById('pf-empty');
const addPf = document.getElementById('add-pf');
const availEl = document.getElementById('availability');
const saveBtn = document.getElementById('save');
const loadErr = document.getElementById('load-err');

const barEl = document.getElementById('bar');
const pctEl = document.getElementById('pct');
const tipEl = document.getElementById('tip');
const pvBar = document.getElementById('pv-bar');
const pvPct = document.getElementById('pv-pct');

let catalog = CATALOG.slice();
let loadFailed = false;
const state = {
  name: '',
  headline: '',
  bio: '',
  skills: [],
  portfolio: [],
  availability: 1,
};

/* ---------- 完成度：与后端 provider_profile 的算法逐项一致 ---------- */
function filledPortfolio() {
  return state.portfolio.filter((p) => String(p.url || '').trim());
}

function scoreNow() {
  const parts = {
    headline: state.headline.trim() ? 20 : 0,
    skills: state.skills.length >= 3 ? 30 : 0,
    portfolio: filledPortfolio().length >= 1 ? 20 : 0,
    bio: state.bio.trim() ? 15 : 0,
    availability: state.availability ? 15 : 0,
  };
  const total = Object.keys(parts).reduce((n, k) => n + parts[k], 0);
  return { parts: parts, total: Math.min(100, total) };
}

function setMeter(el, pct) {
  // 进度条宽度只能动态给；用 CSS 变量而不是直接写 width，样式仍然归 app.css 管
  el.style.setProperty('--pct', pct + '%');
}

function sync() {
  const scored = scoreNow();
  const pct = scored.total;

  pctEl.textContent = pct + '%';
  pvPct.textContent = pct + '%';
  setMeter(barEl, pct);
  setMeter(pvBar, pct);

  document.querySelectorAll('#score-list .score-row').forEach((row) => {
    const done = scored.parts[row.dataset.score] > 0;
    row.classList.toggle('done', done);
  });

  tipEl.textContent =
    pct >= 100
      ? '该填的都填了。剩下的只能靠真实订单攒。'
      : pct >= 70
        ? '还缺一块。补齐之后，别人判断你的速度会快一点。'
        : '定位和 3 个技能是底线，缺了很难被算进推荐。';

  document.getElementById('c-headline').textContent = state.headline.length + ' / 60';
  document.getElementById('c-bio').textContent = state.bio.length + ' / 200';
  document.getElementById('c-headline').classList.toggle('ok', state.headline.trim().length > 0);

  const skillCount = document.getElementById('c-skills');
  skillCount.textContent = '已选 ' + state.skills.length + ' / 5 · 至少 3 个';
  skillCount.classList.toggle('ok', state.skills.length >= 3);
  skillCount.classList.toggle('short', state.skills.length > 0 && state.skills.length < 3);

  const on = state.availability ? 1 : 0;
  document.getElementById('avail-text').textContent = on ? '可接单' : '暂停接单';
  [document.getElementById('avail-state'), document.getElementById('pv-state')].forEach((el) => {
    el.textContent = on ? '接单中' : '暂停接单';
    el.classList.toggle('state-done', !!on);
    el.classList.toggle('state-todo', !on);
  });

  // 预览
  document.getElementById('pv-av').textContent = (state.name || '我').slice(0, 1);
  document.getElementById('pv-name').textContent = state.name || '我';
  document.getElementById('pv-headline').textContent = state.headline.trim() || '还没写定位';
  const pvBio = document.getElementById('pv-bio');
  pvBio.textContent = state.bio.trim();
  pvBio.hidden = !state.bio.trim();

  const pvSkills = document.getElementById('pv-skills');
  pvSkills.innerHTML = state.skills.length
    ? state.skills
        .map((v) => {
          const pair = catalog.find((c) => c[0] === v);
          return html`<span class="tag">${pair ? pair[1] : v}</span>`;
        })
        .join('')
    : html`<span class="tag">没选技能</span>`;

  const links = filledPortfolio();
  document.getElementById('pv-links').innerHTML = links
    .map((p) => {
      const href = /^https?:\/\//i.test(p.url) ? p.url : 'https://' + p.url;
      return html`<li><a href="${href}" target="_blank" rel="noopener noreferrer">${p.label || p.url}</a></li>`;
    })
    .join('');
}

/* ---------- 技能 ---------- */
function renderSkills() {
  skillsEl.innerHTML = catalog
    .map(
      (c) =>
        html`<button class="chip" type="button" data-skill="${c[0]}" aria-pressed="${state.skills.indexOf(c[0]) >= 0}">${c[1]}</button>`
    )
    .join('');
  updateChips();
}

function updateChips() {
  const full = state.skills.length >= 5;
  skillsEl.querySelectorAll('.chip').forEach((c) => {
    const on = state.skills.indexOf(c.dataset.skill) >= 0;
    c.setAttribute('aria-pressed', on ? 'true' : 'false');
    c.setAttribute('aria-disabled', !on && full ? 'true' : 'false');
  });
}

skillsEl.addEventListener('click', (e) => {
  const chip = e.target.closest('.chip');
  if (!chip) return;
  const v = chip.dataset.skill;
  const i = state.skills.indexOf(v);
  if (i >= 0) {
    state.skills.splice(i, 1);
  } else {
    if (state.skills.length >= 5) {
      toast('最多选 5 个，多了推荐理由会说不清', 'warn');
      return;
    }
    state.skills.push(v);
  }
  updateChips();
  sync();
});

/* ---------- 作品 ---------- */
function renderPortfolio(focusIndex) {
  pfEl.innerHTML = state.portfolio
    .map(
      (p, i) => html`
    <div class="pf-row" data-i="${i}">
      <div class="field">
        <input class="input" name="pf-label-${i}" maxlength="40" placeholder="名称，如 退款审批 Agent"
          value="${p.label}" aria-label="作品名称">
      </div>
      <div class="field">
        <input class="input mono" name="pf-url-${i}" placeholder="https://" value="${p.url}"
          aria-label="作品链接" inputmode="url">
      </div>
      <button class="btn btn-quiet btn-sm del" type="button" data-del="${i}">删除</button>
    </div>`
    )
    .join('');

  pfEmpty.hidden = state.portfolio.length > 0;
  addPf.disabled = state.portfolio.length >= 3;
  addPf.textContent = state.portfolio.length >= 3 ? '已到 3 条上限' : '+ 再加一条';

  if (focusIndex !== undefined) {
    const input = pfEl.querySelector('[name="pf-url-' + focusIndex + '"]');
    if (input) input.focus();
  }
  sync();
}

addPf.addEventListener('click', () => {
  if (state.portfolio.length >= 3) return;
  state.portfolio.push({ label: '', url: '' });
  renderPortfolio(state.portfolio.length - 1);
});

pfEl.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-del]');
  if (!btn) return;
  state.portfolio.splice(Number(btn.dataset.del), 1);
  renderPortfolio();
});

pfEl.addEventListener('input', (e) => {
  const m = /^pf-(label|url)-(\d+)$/.exec(e.target.name || '');
  if (!m) return;
  const i = Number(m[2]);
  if (!state.portfolio[i]) return;
  state.portfolio[i][m[1]] = e.target.value;
  if (m[1] === 'url') {
    e.target.setAttribute('aria-invalid', 'false');
    const field = e.target.closest('.field');
    const err = field && field.querySelector('.err');
    if (err) err.hidden = true;
  }
  sync();
});

// 失焦时补协议：手敲 github.com/x 也能存成合法链接
pfEl.addEventListener(
  'blur',
  (e) => {
    const m = /^pf-url-(\d+)$/.exec(e.target.name || '');
    if (!m) return;
    const i = Number(m[1]);
    let v = e.target.value.trim();
    if (v && !/^https?:\/\//i.test(v)) v = 'https://' + v;
    e.target.value = v;
    if (state.portfolio[i]) state.portfolio[i].url = v;
    sync();
  },
  true
);

function markErr(input, msg) {
  if (!input) return;
  const field = input.closest('.field');
  if (!field) return;
  let box = field.querySelector('.err');
  if (!box) {
    box = document.createElement('div');
    box.className = 'err';
    field.appendChild(box);
  }
  box.textContent = msg;
  box.hidden = false;
  input.setAttribute('aria-invalid', 'true');
}

/* ---------- 表单输入 ---------- */
headlineEl.addEventListener('input', () => {
  state.headline = headlineEl.value;
  sync();
});

bioEl.addEventListener('input', () => {
  state.bio = bioEl.value;
  sync();
});

availEl.addEventListener('change', () => {
  state.availability = availEl.checked ? 1 : 0;
  sync();
});

/* ---------- 载入 ---------- */
function showLoadErr(err) {
  loadErr.innerHTML = html`
    <h4>档案没载入</h4>
    <p>${err.message || '请求失败'}</p>
    <p class="tiny mono mt8">${err.code || ''}${err.traceId ? ' · trace ' + err.traceId : ''}</p>`;
  loadErr.hidden = false;
}

async function load() {
  const pvSkel = document.getElementById('pv-skel');
  const pvBody = document.getElementById('pv-body');
  try {
    const data = await api('GET', '/me');
    const user = (data && data.user) || data || {};
    const p = (data && (data.profile || data.provider_profile)) || {};

    state.name = user.name || user.nickname || '';
    state.headline = p.headline || '';
    state.bio = p.bio || '';
    state.availability = Number(p.availability) === 0 ? 0 : 1;

    const skills = Array.isArray(p.skills) ? p.skills : [];
    state.skills = skills
      .map((s) => (typeof s === 'string' ? s : s && (s.slug || s.name)))
      .filter(Boolean)
      .slice(0, 5);

    // 服务端返回了目录外的技能也要能显示、能取消
    state.skills.forEach((v) => {
      if (!catalog.some((c) => c[0] === v)) catalog.push([v, v]);
    });

    const pf = Array.isArray(p.portfolio) ? p.portfolio : [];
    state.portfolio = pf
      .slice(0, 3)
      .map((x) => (typeof x === 'string' ? { label: '', url: x } : { label: x.label || '', url: x.url || '' }));

    headlineEl.value = state.headline;
    bioEl.value = state.bio;
    availEl.checked = !!state.availability;
  } catch (err) {
    // 载入失败就别让人接着保存：表单是空的，一存就把服务端已有档案覆盖了
    loadFailed = true;
    showLoadErr(err);
    saveBtn.disabled = true;
    document.getElementById('save-hint').textContent = '档案没载入，暂时不能保存，刷新重试。';
  } finally {
    pvSkel.hidden = true;
    pvBody.hidden = false;
    renderSkills();
    renderPortfolio();
  }
}

/* ---------- 保存 ---------- */
function busy(btn, on, label) {
  if (on) {
    btn.dataset.label = btn.textContent;
    btn.textContent = label;
    btn.disabled = true;
  } else {
    if (btn.dataset.label) btn.textContent = btn.dataset.label;
    btn.disabled = false;
  }
}

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  if (loadFailed) {
    toast('档案没载入，刷新后再保存', 'err');
    return;
  }

  const result = validate(form, {
    headline: { required: true, max: 60, message: '写一句能让人记住的定位（60 字以内）' },
  });
  if (!result.valid) {
    toast('定位还没写', 'warn');
    return;
  }
  // bio 的 200 字由 maxlength 兜住，这里再挡一次粘贴超长
  if (state.bio.length > 200) {
    toast('补充说明超过 200 字', 'err');
    return;
  }

  let badUrl = false;
  state.portfolio.forEach((p, i) => {
    const url = String(p.url || '').trim();
    if (!url) return;
    const input = pfEl.querySelector('[name="pf-url-' + i + '"]');
    if (!/^https?:\/\/[^\s]+\.[^\s]+$/i.test(url)) {
      markErr(input, '链接格式不对，检查一下');
      badUrl = true;
    }
  });
  if (badUrl) {
    toast('有作品链接格式不对', 'err');
    return;
  }

  const payload = {
    headline: state.headline.trim(),
    bio: state.bio.trim(),
    skills: state.skills.slice(0, 5),
    portfolio: filledPortfolio()
      .slice(0, 3)
      .map((p) => ({ label: String(p.label || '').trim(), url: String(p.url).trim() })),
    availability: state.availability ? 1 : 0,
  };

  busy(saveBtn, true, '保存中…');
  try {
    await api('PUT', '/me/profile', payload);
    toast('档案已保存', 'ok');
    document.getElementById('save-hint').textContent = '已保存，下一次匹配就会用上。';
  } catch (err) {
    toast(err.message || '保存失败', 'err');
    if (err.field === 'headline') {
      markErr(headlineEl, err.message || '定位不符合要求');
    }
  } finally {
    busy(saveBtn, false);
  }
});

document.getElementById('logout').addEventListener('click', async () => {
  try {
    await api('POST', '/auth/logout');
  } catch (e) {
    // 退出失败也回登录页：Cookie 清不掉时登录页会自行处理
  }
  location.replace('login.html');
});

renderSkills();
renderPortfolio();
load();

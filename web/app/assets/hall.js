import { api, html, money, toast, ApiError } from './runtime.js';

/* 需求大厅：拉列表、筛选、展开要求。
   一屏只做一件事——让接单方在 10 秒内判断"这单跟我有没有关系"。 */

const PAGE = 20;
const el = {
  who: document.getElementById('who'),
  lead: document.getElementById('lead'),
  banner: document.getElementById('banner'),
  q: document.getElementById('q'),
  sort: document.getElementById('sort'),
  cats: document.getElementById('cats'),
  reset: document.getElementById('reset'),
  count: document.getElementById('count'),
  tip: document.getElementById('tip'),
  err: document.getElementById('err'),
  list: document.getElementById('list'),
  skeleton: document.getElementById('skeleton'),
  empty: document.getElementById('empty'),
  emptyT: document.getElementById('empty-t'),
  emptyP: document.getElementById('empty-p'),
  emptyAct: document.getElementById('empty-act'),
  more: document.getElementById('more'),
  mine: document.getElementById('mine'),
  mineN: document.getElementById('mine-n'),
  mineNote: document.getElementById('mine-note'),
  mineList: document.getElementById('mine-list'),
  mineToggle: document.getElementById('mine-toggle'),
  navMine: document.getElementById('nav-mine'),
  navMineN: document.getElementById('nav-mine-n'),
};

const state = {
  q: '',
  category: '',
  sort: 'newest',
  items: [],
  total: 0,
  offset: 0,
  hasMore: false,
  loading: false,
  loaded: false,
  justClaimed: false,
};

let me = null;
let catMap = {};
let canClaim = false; // 只有已建档的接单方看得到认领按钮
let mineItems = [];
let mineOpen = true;

/* ---------- 小工具 ---------- */

function ago(ms) {
  if (!ms) return '刚发布';
  const diff = Date.now() - ms;
  if (diff < 60 * 1000) return '刚刚';
  if (diff < 3600 * 1000) return Math.floor(diff / 60000) + ' 分钟前';
  if (diff < 24 * 3600 * 1000) return Math.floor(diff / 3600000) + ' 小时前';
  const days = Math.floor(diff / 86400000);
  return days < 30 ? days + ' 天前' : Math.floor(days / 30) + ' 个月前';
}

function deadlineOf(ms) {
  if (!ms) return { text: '', urgent: false };
  const days = Math.ceil((ms - Date.now()) / 86400000);
  if (days < 0) return { text: '截止已过', urgent: true };
  if (days === 0) return { text: '今天截止', urgent: true };
  if (days === 1) return { text: '明天截止', urgent: true };
  return { text: '还剩 ' + days + ' 天', urgent: days <= 3 };
}

function catLabel(id) {
  return catMap[id] || id || '未分类';
}

function hasFilter() {
  return !!(state.q || state.category);
}

/* ---------- 渲染 ---------- */

function card(t) {
  const dl = deadlineOf(t.deadline_at);
  const badge = dl.urgent
    ? html`<span class="state state-todo">${dl.text}</span>`
    : html`<span class="state state-done">开放中</span>`;

  return html`
  <article class="card card-pad task" data-card="${t.id}">
    <div class="flex ac jb wrapw g8 mb8">
      <div class="flex ac g6 wrapw">
        <span class="tag tag-mono">${catLabel(t.category)}</span>
        ${badge}
      </div>
      <div class="price"><small>预算</small>${money(t.budget_cents)}</div>
    </div>

    <h3>
      <button class="tlink" type="button" data-open="${t.id}" aria-expanded="false">${t.title || '未命名需求'}</button>
    </h3>
    <p class="snip">${t.spec || '需求方还没写细节，展开看交付要求。'}</p>

    <div class="meta mt12">
      <span>${t.requester || '需求方'}</span>
      <span class="dot"></span>
      <span>${Number(t.requester_credit) > 0 ? '信用 ' + Number(t.requester_credit) : '信用未建立'}</span>
      <span class="dot"></span>
      <span>${ago(t.created_at)}</span>
      ${dl.text && !dl.urgent ? html`<span class="dot"></span><span>${dl.text}</span>` : ''}
    </div>

    <div class="detail" hidden data-detail="${t.id}">
      <dl>
        <dt>交付物</dt><dd>${t.deliverables || '未填写'}</dd>
        <dt>交付格式</dt><dd>${t.required_format || '未填写'}</dd>
        <dt>参考风格</dt><dd>${t.style_ref || '未填写'}</dd>
        <dt>完整说明</dt><dd>${t.spec || '未填写'}</dd>
        <dt>需求编号</dt><dd class="mono">${t.id}</dd>
      </dl>
      <p class="tiny muted mt12">这一单由 ${t.requester || '需求方'} 发布，验收标准以「交付物」和「交付格式」为准。</p>
    </div>

    ${canClaim ? claimUI(t) : ''}
  </article>`;
}

/* 认领是两步：先亮出后果，再动手。
   一步到位的按钮看着痛快，但"点错就被别人接了单"这种事没有撤销。 */
function claimUI(t) {
  return html`
    <div class="task-foot" data-foot="${t.id}">
      <button class="btn btn-ink btn-sm" type="button" data-claim="${t.id}">认领这一单</button>
      <span class="tiny muted">认领后从大厅下架，等需求方确认</span>
    </div>
    <div class="claim-box" hidden data-confirm="${t.id}">
      <p><strong>接这一单？</strong>认领之后这单从大厅下架，${t.requester || '需求方'} 会看到你的档案来确认。确认前不算开工。</p>
      <div class="flex ac g8 wrapw">
        <button class="btn btn-ink btn-sm" type="button" data-do="${t.id}">确认接单</button>
        <button class="btn btn-quiet btn-sm" type="button" data-cancel="${t.id}">再看看</button>
      </div>
    </div>`;
}

function render() {
  el.list.innerHTML = state.items.map(card).join('');

  const n = state.total;
  el.count.textContent = hasFilter() ? '筛出 ' + n + ' 单' : n + ' 单开放';
  el.tip.textContent = n > 0 ? '点标题展开交付要求' : '';
  el.more.hidden = !state.hasMore;
  el.reset.hidden = !hasFilter();

  const empty = state.loaded && state.items.length === 0;
  el.empty.hidden = !empty;
  if (empty) {
    if (hasFilter()) {
      el.emptyT.textContent = '这个筛选下没有需求';
      el.emptyP.textContent = '换个关键词，或者把类目放开看看。';
      el.emptyAct.hidden = false;
    } else if (state.justClaimed) {
      // 刚把最后一单接走。这时候说"大厅是空的"会让人以为出错了
      el.emptyT.textContent = '暂时没有别的开放需求';
      el.emptyP.textContent = '刚接的单在「我接的单」里，需求方确认后就会往下走。';
      el.emptyAct.hidden = true;
    } else {
      el.emptyT.textContent = '大厅现在是空的';
      el.emptyP.textContent = '第一个需求发布之后，这里就会出现。过一会儿刷新看看。';
      el.emptyAct.hidden = true;
    }
  }
}

/* 我接的单。放顶部而不是单独开一页: 认领之后视线不用离开大厅,
   下一单还能接着找。单子多了再拆页, 现在拆是空页里放一条。 */
function renderMine() {
  const n = mineItems.length;
  el.mineN.textContent = String(n);
  el.navMineN.textContent = String(n);
  el.mine.hidden = n === 0;
  el.navMine.hidden = n === 0;
  if (n === 0) return;

  el.mineList.innerHTML = mineItems
    .map(
      (o) => html`
    <li class="oitem">
      <div class="flex ac jb wrapw g8">
        <span class="${stateClass(o.state)}">${o.state_label || '待确认'}</span>
        <span class="tiny muted">${ago(o.created_at)}认领</span>
      </div>
      <strong>${o.task_title || '需求 ' + o.task_id}</strong>
      <div class="oline">
        <span>${o.counterparty || '需求方'}</span>
        <span class="dot"></span>
        <span>${money(o.amount_cents)}</span>
        <span class="dot"></span>
        <span class="mono">${o.id}</span>
      </div>
    </li>`
    )
    .join('');

  el.mineList.hidden = !mineOpen;
  el.mineNote.hidden = !mineOpen;
  el.mineToggle.textContent = mineOpen ? '收起' : '展开';
  el.mineToggle.setAttribute('aria-expanded', mineOpen ? 'true' : 'false');
}

function stateClass(s) {
  if (s === 'done') return 'state state-done';
  if (s === 'in_progress') return 'state state-doing';
  return 'state state-todo';
}

async function loadMine() {
  try {
    const data = await api('GET', '/me/orders');
    const items = Array.isArray(data.items) ? data.items : [];
    // 这一页是接单方视角。需求方的单子在"我发的需求"里看, 那边还没做。
    mineItems = items.filter((o) => o.role === 'provider');
    renderMine();
  } catch (e) {
    // 拉不到不挡大厅: 认领本身还能用, 成功后会本地补一条
  }
}

function paintWho(p) {
  const name = (me && (me.name || me.nickname)) || '你';
  const pct = Number(p && p.completeness) || 0;

  if (me && me.role !== 'provider') {
    el.who.innerHTML = html`${name} · 需求方`;
    el.banner.innerHTML = html`<strong>你现在是需求方身份。</strong>大厅里是别人发出来的需求，你可以先看行情；发单入口还没开放。`;
    el.banner.hidden = false;
    document.getElementById('nav-profile').hidden = true;
    return;
  }

  el.who.innerHTML = pct >= 100
    ? html`${name} · 档案完整度 ${pct}%`
    : html`<a href="profile.html">${name} · 档案完整度 ${pct}%，去补全</a>`;

  if (pct < 100) {
    el.banner.innerHTML = html`<strong>档案还差一块。</strong>补齐之后，系统算到你的时候才有东西可说。 <a href="profile.html">去补全 →</a>`;
    el.banner.hidden = false;
  }
}

function showErr(err) {
  const box = el.err;
  const hint = err instanceof ApiError ? err.hint : '';
  const code = err instanceof ApiError ? err.code : 'ERROR';
  const trace = err instanceof ApiError ? err.traceId : '';
  box.innerHTML = html`
    <h4>需求没拉到</h4>
    <p>${(err && err.message) || '请求失败'}</p>
    ${hint ? html`<p class="tiny mt8">${hint}</p>` : ''}
    <div class="flex ac g8 mt12">
      <button class="btn btn-line btn-sm" type="button" id="retry">重试</button>
      <span class="tiny mono muted">${code}${trace ? ' · trace ' + trace : ''}</span>
    </div>`;
  box.hidden = false;
  const retry = document.getElementById('retry');
  if (retry) retry.addEventListener('click', () => load(true));
}

/* ---------- 数据 ---------- */

async function loadCats() {
  try {
    const data = await api('GET', '/meta/categories');
    const items = Array.isArray(data.items) ? data.items : [];
    catMap = {};
    items.forEach((c) => (catMap[c.id] = c.label));
    el.cats.innerHTML =
      html`<button class="chip" type="button" data-cat="" aria-pressed="${!state.category}">全部</button>` +
      items.map(
        (c) =>
          html`<button class="chip" type="button" data-cat="${c.id}" aria-pressed="${state.category === c.id}">${c.label}</button>`
      ).join('');
  } catch (e) {
    // 类目拉不到不该挡住列表: 卡片里会退回显示类目 ID
    el.cats.innerHTML = html`<button class="chip" type="button" data-cat="" aria-pressed="true">全部</button>`;
  }
}

async function load(reset) {
  if (state.loading) return;
  state.loading = true;
  el.err.hidden = true;

  if (reset) {
    state.items = [];
    state.total = 0;
    state.offset = 0;
    state.justClaimed = false;
    el.list.innerHTML = '';
    el.skeleton.hidden = false;
    el.empty.hidden = true;
  }
  el.more.disabled = true;

  const qs = new URLSearchParams({
    limit: String(PAGE),
    offset: String(reset ? 0 : state.offset),
    sort: state.sort,
  });
  if (state.q) qs.set('q', state.q);
  if (state.category) qs.set('category', state.category);

  try {
    const data = await api('GET', '/tasks?' + qs.toString());
    const items = Array.isArray(data.items) ? data.items : [];
    state.items = reset ? items : state.items.concat(items);
    state.total = Number(data.total) || state.items.length;
    state.offset = state.items.length;
    state.hasMore = !!data.has_more;
    state.loaded = true;
    render();
  } catch (err) {
    // 首屏失败要留白给人看错误; 追加失败保留已有列表, 只弹一条
    if (reset) {
      el.list.innerHTML = '';
      el.count.textContent = '没拉到';
      state.loaded = true;
      render();
    }
    showErr(err);
  } finally {
    el.skeleton.hidden = true;
    el.more.disabled = false;
    state.loading = false;
  }
}

/* ---------- 交互 ---------- */

let timer = 0;
el.q.addEventListener('input', () => {
  clearTimeout(timer);
  timer = setTimeout(() => {
    state.q = el.q.value.trim();
    load(true);
  }, 260);
});

el.sort.addEventListener('change', () => {
  state.sort = el.sort.value;
  load(true);
});

el.cats.addEventListener('click', (e) => {
  const chip = e.target.closest('[data-cat]');
  if (!chip) return;
  state.category = chip.dataset.cat || '';
  el.cats.querySelectorAll('[data-cat]').forEach((c) => c.setAttribute('aria-pressed', c === chip ? 'true' : 'false'));
  load(true);
});

function clearFilters() {
  state.q = '';
  state.category = '';
  el.q.value = '';
  el.cats.querySelectorAll('[data-cat]').forEach((c) => c.setAttribute('aria-pressed', c.dataset.cat === '' ? 'true' : 'false'));
  load(true);
}

el.reset.addEventListener('click', clearFilters);
el.emptyAct.addEventListener('click', clearFilters);

el.list.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-open]');
  if (!btn) return;
  const id = btn.dataset.open;
  const detail = el.list.querySelector('[data-detail="' + id + '"]');
  if (!detail) return;
  const open = detail.hidden;
  detail.hidden = !open;
  btn.setAttribute('aria-expanded', open ? 'true' : 'false');
});

el.more.addEventListener('click', () => load(false));

/* ---------- 认领 ---------- */

function dropCard(id) {
  state.items = state.items.filter((t) => t.id !== id);
  state.total = Math.max(0, state.total - 1);
  const node = el.list.querySelector('[data-card="' + id + '"]');
  if (node) {
    node.classList.add('card-gone');
    setTimeout(() => node.remove(), 180);
  }
  el.count.textContent = hasFilter() ? '筛出 ' + state.total + ' 单' : state.total + ' 单开放';
  if (state.total === 0) state.justClaimed = true;
  if (state.items.length === 0) {
    state.loaded = true;
    render();
  }
}

async function doClaim(id, btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '正在接单…';
  try {
    const order = await api('POST', '/tasks/' + encodeURIComponent(id) + '/take', {});
    // 服务端回的就是已经落库的订单, 直接放进"我接的单", 不再多问一次
    mineItems.unshift(order);
    mineOpen = true;
    renderMine();
    dropCard(id);
    toast('这一单你接了，等' + (order.counterparty || '需求方') + '确认', 'ok');
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'TASK_TAKEN') {
      dropCard(id); // 别人先接了, 这张卡本来就不该留在开放列表里
      toast('这一单刚被接走了', 'warn');
      return;
    }
    if (code === 'PROFILE_INCOMPLETE' || code === 'ROLE_NOT_PROVIDER') {
      toast(err.message || '先补一下档案', 'warn');
      setTimeout(() => location.assign('profile.html'), 1000);
      return;
    }
    toast((err && err.message) || '没接上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

el.list.addEventListener('click', (e) => {
  const claim = e.target.closest('[data-claim]');
  if (claim) {
    const id = claim.dataset.claim;
    claim.closest('[data-foot="' + id + '"]').hidden = true;
    const box = el.list.querySelector('[data-confirm="' + id + '"]');
    if (box) {
      box.hidden = false;
      const first = box.querySelector('[data-do]');
      if (first) first.focus();
    }
    return;
  }

  const cancel = e.target.closest('[data-cancel]');
  if (cancel) {
    const id = cancel.dataset.cancel;
    const box = el.list.querySelector('[data-confirm="' + id + '"]');
    if (box) box.hidden = true;
    const foot = el.list.querySelector('[data-foot="' + id + '"]');
    if (foot) foot.hidden = false;
    return;
  }

  const go = e.target.closest('[data-do]');
  if (go) doClaim(go.dataset.do, go);
});

el.mineToggle.addEventListener('click', () => {
  mineOpen = !mineOpen;
  renderMine();
});

el.navMine.addEventListener('click', () => {
  mineOpen = true;
  renderMine();
  el.mine.scrollIntoView({ behavior: 'smooth', block: 'start' });
});

document.getElementById('logout').addEventListener('click', async () => {
  try {
    await api('POST', '/auth/logout');
  } catch (e) {
    // 退出失败也回登录页: cookie 清不掉时登录页会自己处理
  }
  location.replace('login.html');
});

/* ---------- 启动 ---------- */

async function boot() {
  loadCats();

  try {
    const data = await api('GET', '/me');
    me = (data && data.user) || {};
    const p = (data && data.profile) || {};
    // 接单方没建档: 大厅对他没意义, 先送去填档案。填完档案会自己回到这儿。
    if (me.role === 'provider' && !me.onboarded) {
      location.replace('profile.html?next=hall');
      return;
    }
    // 认领按钮只给已建档的接单方: 需求方点不了, 没档案的人点了也会被服务端拒
    canClaim = me.role === 'provider' && !!me.onboarded;
    paintWho(p);
  } catch (e) {
    return; // 未登录时 runtime 已经带去登录页
  }

  if (canClaim) loadMine();

  if (new URLSearchParams(location.search).get('saved') === '1') {
    toast('档案已保存，这里是现在能接的需求', 'ok');
  }

  load(true);
}

boot();

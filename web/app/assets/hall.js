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
  mineTitle: document.getElementById('mine-title'),
  mineN: document.getElementById('mine-n'),
  mineNote: document.getElementById('mine-note'),
  mineList: document.getElementById('mine-list'),
  mineToggle: document.getElementById('mine-toggle'),
  navMine: document.getElementById('nav-mine'),
  navMineN: document.getElementById('nav-mine-n'),
  navMineLabel: document.getElementById('nav-mine-label'),
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
let mineItems = []; // 接单方: 我接的单
let inboxItems = []; // 需求方: 别人接了我的单
let mineOpen = true;
let myProfile = {}; // 档案完整度, 横幅要用; 确认之后横幅得跟着变

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
  // 需求方看大厅时会看到自己发的单。不标一下，他会以为自己也能接
  const mineTask = !!(me && me.slug && t.requester_slug === me.slug);

  return html`
  <article class="card card-pad task" data-card="${t.id}">
    <div class="flex ac jb wrapw g8 mb8">
      <div class="flex ac g6 wrapw">
        <span class="tag tag-mono">${catLabel(t.category)}</span>
        ${mineTask ? html`<span class="tag">你发的</span>` : ''}
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

/* 这一块两种身份各看各的: 接单方看我接的单, 需求方看等我点头的单。
   放顶部而不是单独开一页: 认领之后视线不用离开大厅, 下一单还能接着找。
   单子多了再拆页, 现在拆是空页里放一条。 */
function renderMine() {
  const isProvider = !me || me.role === 'provider';
  const items = isProvider ? mineItems : inboxItems;
  const pending = items.filter((o) => o.needs_me).length;
  const n = items.length;

  // 标题跟着"有没有事等你"变: 全在进行了还挂「等你确认」, 是在催一件已经做完的事
  if (isProvider) {
    el.mineTitle.textContent = '我接的单';
    el.navMineLabel.textContent = '我接的单';
    el.mineNote.textContent = '认领之后等需求方确认，确认后进入进行中。';
  } else if (pending > 0) {
    el.mineTitle.textContent = '等你确认';
    el.navMineLabel.textContent = '等你确认';
    el.mineNote.textContent = '有人认领了你的需求。看一眼他的一句话定位，确认之后这一单才开始算。';
  } else {
    el.mineTitle.textContent = '我发的单';
    el.navMineLabel.textContent = '我的单';
    el.mineNote.textContent = '暂时没有等你确认的单。有人认领你的需求时，会出现在这里。';
  }

  el.mineN.textContent = String(n);
  // 需求方的导航上显示"几单等你动", 不是"一共几单"——前者才是需要他处理的
  el.navMineN.textContent = String(isProvider ? n : pending || n);
  el.mine.hidden = n === 0;
  el.navMine.hidden = n === 0;
  if (n === 0) return;

  // 该我动的排前面: 需求方登进来第一眼要看见的是"有事等你"
  el.mineList.innerHTML = items
    .slice()
    .sort((a, b) => Number(b.needs_me) - Number(a.needs_me))
    .map(orderItem)
    .join('');

  el.mineList.hidden = !mineOpen;
  el.mineNote.hidden = !mineOpen;
  el.mineToggle.textContent = mineOpen ? '收起' : '展开';
  el.mineToggle.setAttribute('aria-expanded', mineOpen ? 'true' : 'false');

  // 横幅上的「有 N 单等你点头」和面板是同一件事, 不能一个变了另一个还挂着
  if (myProfile) paintWho(myProfile);
}

function orderItem(o) {
  const isProvider = o.role === 'provider';
  const credit = Number(o.counterparty_credit) || 0;
  // 时间轴只有"认领"这一刻: 后两个状态没有各自的变更时间, 硬配动词是在编。
  const verb = o.state === 'to_confirm' ? (isProvider ? '认领' : '有人认领') : '';
  return html`
  <li class="oitem" data-order="${o.id}">
    <div class="flex ac jb wrapw g8">
      <span class="${stateClass(o.state)}">${o.state_label || '待确认'}</span>
      <span class="tiny muted">${ago(o.created_at)}${verb}</span>
    </div>
    <strong>${o.task_title || '需求 ' + o.task_id}</strong>
    <div class="oline">
      <span>${o.counterparty || (isProvider ? '需求方' : '接单方')}</span>
      ${credit > 0 ? html`<span class="dot"></span><span>信用 ${credit}</span>` : ''}
      <span class="dot"></span>
      <span>${money(o.amount_cents)}</span>
      <span class="dot"></span>
      <span class="mono">${o.id}</span>
    </div>
    ${o.counterparty_headline ? html`<p class="otag">${o.counterparty_headline}</p>` : ''}
    ${o.cancel_reason ? html`<p class="tiny muted mt6">退回原因：${o.cancel_reason}</p>` : ''}
    ${o.needs_me ? actionUI(o) : ''}
  </li>`;
}

/* 确认开工、退回，都要点两下。认领点错还能去别处找单，
   这两下点错就把一单推下去了或者推回去了，没有撤回。
   退回还要留一句原因: 接单方有权知道为什么没成。 */
function actionUI(o) {
  const who = o.counterparty || '对方';
  return html`
  <div class="oact" data-act="${o.id}">
    <button class="btn btn-ink btn-sm" type="button" data-ask-confirm="${o.id}">确认开工</button>
    <button class="btn btn-quiet btn-sm" type="button" data-ask-decline="${o.id}">不合适，退回</button>
  </div>

  <div class="obox" hidden data-box="confirm-${o.id}">
    <p><strong>确认让 ${who} 做这一单？</strong>确认后进入进行中，往后交付、验收都对着这一单走。</p>
    <div class="flex ac g8 wrapw">
      <button class="btn btn-ink btn-sm" type="button" data-do-confirm="${o.id}">确认开工</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">再想想</button>
    </div>
  </div>

  <div class="obox" hidden data-box="decline-${o.id}">
    <p><strong>把这一单退回大厅？</strong>${who} 会看到这条原因，单子重新对别人开放。</p>
    <label class="label" for="why-${o.id}">原因（可选，60 字内）</label>
    <textarea class="input" id="why-${o.id}" rows="2" maxlength="60" placeholder="比如：这单的工期比我能等的更紧"></textarea>
    <div class="flex ac g8 wrapw mt8">
      <button class="btn btn-ink btn-sm" type="button" data-do-decline="${o.id}">确认退回</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">取消</button>
    </div>
  </div>`;
}

function stateClass(s) {
  if (s === 'done') return 'state state-done';
  if (s === 'in_progress') return 'state state-doing';
  return 'state state-todo';
}

async function loadOrders() {
  try {
    const data = await api('GET', '/me/orders');
    const items = Array.isArray(data.items) ? data.items : [];
    // 一个接口两种身份: 服务端按 role 分好了, 这里各归各位
    mineItems = items.filter((o) => o.role === 'provider');
    inboxItems = items.filter((o) => o.role === 'requester');
    renderMine();
  } catch (e) {
    // 拉不到不挡大厅: 认领本身还能用, 成功后会本地补一条
  }
}

// 单条替换, 不重拉整表: 确认/退回之后立刻能看到新状态, 又不会闪一下
function replaceItem(o) {
  const swap = (list) => list.map((x) => (x.id === o.id ? o : x));
  mineItems = swap(mineItems);
  inboxItems = swap(inboxItems);
}

function paintWho(p) {
  const name = (me && (me.name || me.nickname)) || '你';
  const pct = Number(p && p.completeness) || 0;

  if (me && me.role !== 'provider') {
    const pending = inboxItems.filter((o) => o.needs_me).length;
    el.who.innerHTML = html`${name} · 发布方`;
    // 发布方点不了认领, 这句引导要跟着改, 不然是让人去做一件他做不了的事
    el.lead.textContent = '这些是平台上开放的需求。你发的单也在里面，标着「你发的」。';
    if (pending > 0) {
      el.banner.innerHTML = html`<strong>有 ${pending} 单等你点头。</strong>看一眼接单人的一句话定位和信用分，确认之后这一单才开始算。 <button class="btn btn-line btn-sm" type="button" id="jump-mine">去确认</button>`;
    } else {
      el.banner.innerHTML = html`<strong>你现在是发布方。</strong>有人认领你的需求时会出现在上面这块。发单入口还没开放。`;
    }
    el.banner.hidden = false;
    const jump = document.getElementById('jump-mine');
    if (jump) {
      jump.addEventListener('click', () => {
        mineOpen = true;
        renderMine();
        el.mine.scrollIntoView({ behavior: 'smooth', block: 'start' });
      });
    }
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

/* ---------- 需求方: 确认开工 / 退回大厅 ---------- */

function openBox(id, which) {
  // 一次只开一个盒子: 两个盒子都开着, 人容易点错
  el.mineList.querySelectorAll('[data-box]').forEach((b) => (b.hidden = true));
  el.mineList.querySelectorAll('[data-act]').forEach((a) => (a.hidden = true));
  const box = el.mineList.querySelector('[data-box="' + which + '-' + id + '"]');
  if (!box) return;
  box.hidden = false;
  const first = box.querySelector('button');
  if (first) first.focus();
}

function closeBox(id) {
  el.mineList.querySelectorAll('[data-box]').forEach((b) => (b.hidden = true));
  const act = el.mineList.querySelector('[data-act="' + id + '"]');
  if (act) act.hidden = false;
}

async function doConfirm(id, btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '确认中…';
  try {
    const o = await api('POST', '/orders/' + encodeURIComponent(id) + '/confirm');
    replaceItem(o);
    renderMine();
    toast('已确认，这一单开始算', 'ok');
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'ORDER_NOT_PENDING' || code === 'ORDER_NOT_FOUND') {
      await loadOrders(); // 状态已经被别处改过, 让界面跟上真实情况
      toast(err.message || '这一单已经不在等确认', 'warn');
      return;
    }
    toast((err && err.message) || '没确认上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

async function doDecline(id, btn) {
  const label = btn.textContent;
  const why = document.getElementById('why-' + id);
  btn.disabled = true;
  btn.textContent = '退回中…';
  try {
    const o = await api('POST', '/orders/' + encodeURIComponent(id) + '/decline', {
      reason: why ? why.value.trim() : '',
    });
    replaceItem(o);
    renderMine();
    toast('已退回大厅，别人可以接了', 'ok');
    load(true); // 这一单重新开放, 大厅列表里应该又能看到
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'ORDER_NOT_PENDING' || code === 'ORDER_NOT_FOUND') {
      await loadOrders();
      toast(err.message || '这一单已经不在等确认', 'warn');
      return;
    }
    toast((err && err.message) || '没退回，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

el.mineList.addEventListener('click', (e) => {
  const askC = e.target.closest('[data-ask-confirm]');
  if (askC) {
    openBox(askC.dataset.askConfirm, 'confirm');
    return;
  }
  const askD = e.target.closest('[data-ask-decline]');
  if (askD) {
    openBox(askD.dataset.askDecline, 'decline');
    return;
  }
  const close = e.target.closest('[data-close-box]');
  if (close) {
    closeBox(close.dataset.closeBox);
    return;
  }
  const goC = e.target.closest('[data-do-confirm]');
  if (goC) {
    doConfirm(goC.dataset.doConfirm, goC);
    return;
  }
  const goD = e.target.closest('[data-do-decline]');
  if (goD) doDecline(goD.dataset.doDecline, goD);
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

  let prof = {};
  try {
    const data = await api('GET', '/me');
    me = (data && data.user) || {};
    prof = (data && data.profile) || {};
    myProfile = prof;
    // 接单方没建档: 大厅对他没意义, 先送去填档案。填完档案会自己回到这儿。
    if (me.role === 'provider' && !me.onboarded) {
      location.replace('profile.html?next=hall');
      return;
    }
    // 认领按钮只给已建档的接单方: 需求方点不了, 没档案的人点了也会被服务端拒
    canClaim = me.role === 'provider' && !!me.onboarded;
  } catch (e) {
    return; // 未登录时 runtime 已经带去登录页
  }

  // 两种身份都要拉: 需求方靠这份单子知道有没有人认领，
  // 横幅上的「有 N 单等你点头」用的就是它, 所以得排在 paintWho 前面。
  await loadOrders();
  paintWho(prof);

  if (new URLSearchParams(location.search).get('saved') === '1') {
    toast('档案已保存，这里是现在能接的需求', 'ok');
  }

  load(true);
}

boot();

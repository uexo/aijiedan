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
let myOpenTasks = []; // 需求方: 我发的、还没人接的需求(不在订单里, 要单独查)
let mineOpen = true;
let myProfile = {}; // 档案完整度, 横幅要用; 确认之后横幅得跟着变
// 互评打分: 每个订单选了几星。放外面是因为重渲染会重建按钮,
// 存在 DOM 上就会在列表刷新时丢掉用户已经选好的分数。
let starPick = {};

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
  // 还没人接的需求不在任何订单里, 要单独列——否则自己刚发出去的单,
  // 在大厅里滚两页就再也找不着了(它旁边还挂着几十条别人的单)。
  const open = isProvider ? [] : myOpenTasks;
  const pending = items.filter((o) => o.needs_me).length;
  const n = items.length + open.length;

  // 标题跟着"有没有事等你"变: 全在进行了还挂「等你确认」, 是在催一件已经做完的事
  if (isProvider) {
    el.mineTitle.textContent = '我接的单';
    el.navMineLabel.textContent = '我接的单';
    el.mineNote.textContent = '认领之后等需求方确认；确认开工的单在「进行中」，交完等验收。';
  } else if (pending > 0) {
    // "等你确认"和"等你验收"是两种事: 一个是还没开工, 一个是已经交了东西。
    // 混成一个标题, 需求方会以为又来了个认领。
    const toConfirm = items.filter((o) => o.needs_me && o.state === 'to_confirm').length;
    const toReview = items.filter((o) => o.needs_me && o.state === 'to_review').length;
    el.mineTitle.textContent = toReview === 0 ? '等你确认' : toConfirm === 0 ? '等你验收' : '等你处理';
    el.navMineLabel.textContent = '等你处理';
    const notes = [];
    if (toReview > 0) notes.push('待验收的单里逐项对一遍交付内容，再决定通过还是打回。');
    if (toConfirm > 0) notes.push('待确认的单看一眼接单人的一句话定位，确认之后这一单才开始算。');
    el.mineNote.textContent = notes.join(' ');
  } else {
    el.mineTitle.textContent = '我发的单';
    el.navMineLabel.textContent = '我的单';
    el.mineNote.textContent = open.length
      ? `挂着 ${open.length} 条还没人接。有人认领或交付时，会出现在这里。`
      : '暂时没有等你处理的单。有人认领或交付你的需求时，会出现在这里。';
  }

  el.mineN.textContent = String(n);
  // 需求方的导航上显示"几单等你动", 不是"一共几单"——前者才是需要他处理的
  el.navMineN.textContent = String(isProvider ? n : pending || n);
  el.mine.hidden = n === 0;
  el.navMine.hidden = n === 0;
  if (n === 0) return;

  // 订单在前、待接的需求在后。
  // 不是因为订单更重要, 而是因为订单里有"该你动手"的(needs_me),
  // 待接的需求只是挂着等别人来——第一眼要看见的是能推动的事。
  el.mineList.innerHTML =
    items
      .slice()
      .sort((a, b) => Number(b.needs_me) - Number(a.needs_me))
      .map(orderItem)
      .join('') + open.map(taskItem).join('');

  el.mineList.hidden = !mineOpen;
  el.mineNote.hidden = !mineOpen;
  el.mineToggle.textContent = mineOpen ? '收起' : '展开';
  el.mineToggle.setAttribute('aria-expanded', mineOpen ? 'true' : 'false');

  // 横幅上的「有 N 单等你点头」和面板是同一件事, 不能一个变了另一个还挂着
  if (myProfile) paintWho(myProfile);
}

/* 还没人接的需求。它和订单卡长得像但含义不同:
   订单说的是"这件事在推进", 这条说的是"这件事还悬着, 没人应"。
   撤回只挂在这一格上: 有人接了之后就得走订单侧的退回, 两条链路各管各的状态。 */
function taskItem(t) {
  const cat = t.category_name || catMap[t.category] || t.category || '未分类';
  return html`
  <li class="oitem" data-open-task="${t.id}">
    <div class="flex ac jb wrapw g8">
      <span class="state state-todo">${t.state_label || '开放中'}</span>
      <span class="tiny muted">${ago(t.created_at)}挂出 · 还没人接</span>
    </div>
    <strong>${t.title}</strong>
    <div class="oline">
      <span>${cat}</span>
      <span class="dot"></span>
      <span>${money(t.budget_cents)}</span>
      ${t.item_count > 0 ? html`<span class="dot"></span><span>清单 ${t.item_count} 项</span>` : ''}
      <span class="dot"></span>
      <span class="mono">${t.id}</span>
    </div>

    <div class="oact" data-act="${t.id}">
      <button class="btn btn-quiet btn-sm" type="button" data-ask-withdraw="${t.id}">撤回这条</button>
    </div>

    <div class="obox" hidden data-box="withdraw-${t.id}">
      <p><strong>把这条从大厅撤掉？</strong>撤掉之后别人就搜不到了，也不能再被认领。已经有人接的单撤不了，那种要走退回。</p>
      <div class="flex ac g8 wrapw">
        <button class="btn btn-ink btn-sm" type="button" data-do-withdraw="${t.id}">确认撤回</button>
        <button class="btn btn-quiet btn-sm" type="button" data-close-box="${t.id}">还在发</button>
      </div>
    </div>
  </li>`;
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
    ${deliveryBox(o)}
    ${o.needs_me ? actionUI(o) : ''}
    ${o.state === 'done' ? reviewDoneUI(o) : ''}
  </li>`;
}

/* 最近一轮交付。待验收时这是需求方唯一的判断依据:
   不看内容只看到一个"待验收"标签，人会直接去私聊问"你交了啥"。 */
function deliveryBox(o) {
  const d = o.last_delivery;
  if (!d) return '';
  const undone = Number(d.undone_count) || 0;
  const linkOk = d.link && /^https?:\/\//i.test(d.link);
  return html`
  <div class="dshow">
    <div class="flex ac jb wrapw g8">
      <span class="tiny muted">第 ${d.round} 轮交付 · ${ago(d.created_at)}</span>
      ${undone > 0 ? html`<span class="state state-todo">${undone} 项未完成</span>` : ''}
    </div>
    <p class="dtext">${d.content}</p>
    ${d.review_note ? html`<p class="dnote-back"><strong>需求方打回：</strong>${d.review_note}</p>` : ''}
    ${linkOk ? html`<p class="tiny mt6"><a href="${d.link}" target="_blank" rel="noopener noreferrer">打开交付物 →</a></p>` : ''}
    ${(d.items || []).length
      ? html`<ul class="ditems">
          ${(d.items || []).map(
            (it) => html`<li class="${it.done ? 'don' : 'dun'}">
              <span class="mono">${it.item_id}</span>
              <span>${it.done ? '已完成' : '未完成'}</span>
              ${it.note ? html`<span class="dnote">${it.note}</span>` : ''}
            </li>`
          )}
        </ul>`
      : ''}
  </div>`;
}

/* 这一单该谁动。按身份和四态分派，而不是堆一串 if:
   接单方在进行中只有一个动作(交付)，需求方在待确认只有一个动作(点头)，
   需求方在待验收有两个动作(通过/打回)——两个都摆出来，但要说清后果。 */
function actionUI(o) {
  if (o.role === 'provider' && o.state === 'in_progress') return deliverUI(o);
  if (o.role === 'requester' && o.state === 'to_review') return reviewUI(o);
  if (o.role === 'requester' && o.state === 'to_confirm') return confirmUI(o);
  // 兜底给空: 万一 needs_me 算漏了, 宁可这一格没按钮, 也别把"确认开工"
  // 挂到已经完成或者不属于他的单上。
  return '';
}

/* 验收。通过是把这一单结束掉，打回是让它回到进行中:
   两个动作都不该一点就走，所以都放在二次确认里。
   打回必须写原因——接单方下一轮只凭这句话改。 */
function reviewUI(o) {
  const who = o.counterparty || '接单方';
  const round = Number(o.delivery_round) || 0;
  const max = Number(o.max_revisions) || 0;
  const left = max > 0 ? max - round : 0;
  return html`
  <div class="oact" data-act="${o.id}">
    <button class="btn btn-ink btn-sm" type="button" data-ask-accept="${o.id}">验收通过</button>
    <button class="btn btn-quiet btn-sm" type="button" data-ask-revise="${o.id}">打回修改</button>
    <span class="tiny muted">${max > 0 ? '第 ' + round + '/' + max + ' 轮' + (left > 0 ? '，还能打回 ' + left + ' 次' : '，轮次已用完') : ''}</span>
  </div>

  <div class="obox" hidden data-box="accept-${o.id}">
    <p><strong>验收通过，这一单就算成交？</strong>通过后订单结束，需求单关闭，不能再打回。</p>
    <div class="flex ac g8 wrapw">
      <button class="btn btn-ink btn-sm" type="button" data-do-accept="${o.id}">确认通过</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">再看看</button>
    </div>
  </div>

  <div class="obox" hidden data-box="revise-${o.id}">
    <p><strong>打回给 ${who} 重做？</strong>这一单回到进行中，第 ${round > 0 ? round + 1 : 1} 轮交付要把这里说的问题改掉。</p>
    <label class="label" for="rv-${o.id}">要改什么（必填，500 字内）</label>
    <textarea class="input" id="rv-${o.id}" rows="3" maxlength="500" placeholder="比如：第 2 项只说做了，没给可复现的地址；第 3 项口径和数据源对不上"></textarea>
    <div class="flex ac g8 wrapw mt8">
      <button class="btn btn-ink btn-sm" type="button" data-do-revise="${o.id}">确认打回</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">取消</button>
    </div>
  </div>`;
}

/* 交付这一格要按需求清单逐项交代。没有清单的需求就只填说明和链接，
   有清单的不逐项填，需求方验收时没法判断"哪一条做到了"。 */
function deliverUI(o) {
  const reqs = Array.isArray(o.requirements) ? o.requirements : [];
  const round = Number(o.delivery_round) || 0;
  const max = Number(o.max_revisions) || 0;
  return html`
  <div class="oact" data-act="${o.id}">
    <button class="btn btn-ink btn-sm" type="button" data-ask-deliver="${o.id}">提交交付</button>
    <span class="tiny muted">交付后进入待验收${max > 0 ? '，已交 ' + round + '/' + max + ' 轮' : ''}</span>
  </div>

  <div class="obox" hidden data-box="deliver-${o.id}">
    <p><strong>交付这一单？</strong>${reqs.length ? '按需求清单逐项交代完成情况，' : ''}提交后进入待验收，自己撤不回来。</p>
    <label class="label" for="dc-${o.id}">交付说明（必填，1000 字内）</label>
    <textarea class="input" id="dc-${o.id}" rows="3" maxlength="1000" placeholder="做了什么、怎么用、有哪些已知限制"></textarea>
    <label class="label mt8" for="dl-${o.id}">交付物链接（可选）</label>
    <input class="input" id="dl-${o.id}" type="url" maxlength="500" placeholder="https:// 网盘 / 仓库 / 文档地址">
    ${reqs.length
      ? html`<div class="dlist mt12">
          ${reqs.map(
            (it) => html`<div class="drow">
              <label class="dcheck">
                <input type="checkbox" data-done="${it.id}" checked>
                <span><span class="mono">${it.id}</span> ${it.desc}</span>
              </label>
              <input class="input input-sm" type="text" maxlength="200" data-note="${it.id}" placeholder="完成说明；没做完就写清原因">
            </div>`
          )}
        </div>`
      : ''}
    <div class="flex ac g8 wrapw mt8">
      <button class="btn btn-ink btn-sm" type="button" data-do-deliver="${o.id}">确认交付</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">再等等</button>
    </div>
  </div>`;
}

/* 互评。一单走完之后才有这一格，而且必须说清"现在看不看得见对方打的星"。
   规则是同时揭盲: 双方都提交才互相可见。不说清楚的话，用户会以为对方
   不打算评，于是自己也拖着——那就成了互相等待的死锁。 */
function reviewDoneUI(o) {
  const who = o.counterparty || '对方';
  const mine = !!o.my_review_done;
  const theirs = !!o.counterpart_review_done;

  if (mine) {
    return html`
    <div class="oact" data-act="${o.id}">
      <span class="tiny muted">你已评价${who}</span>
      ${theirs
        ? html`<button class="btn btn-quiet btn-sm" type="button" data-show-reviews="${o.id}">看双方评价</button>`
        : html`<span class="tiny muted">· 等对方评价，双方都提交后才互相可见</span>`}
    </div>

    <div class="obox" hidden data-box="show-${o.id}">
      <div data-reviews="${o.id}"><p class="tiny muted">加载中…</p></div>
      <div class="flex ac g8 wrapw mt8">
        <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">收起</button>
      </div>
    </div>`;
  }

  return html`
  <div class="oact" data-act="${o.id}">
    <button class="btn btn-ink btn-sm" type="button" data-ask-review="${o.id}">评价${who}</button>
    ${theirs ? html`<span class="tiny muted">他先评了。你提交后才互相可见，不会被他照着改</span>` : ''}
  </div>

  <div class="obox" hidden data-box="review-${o.id}">
    <p><strong>给 ${who} 打个分？</strong>提交后不能改。双方都提交之后才互相看见——这样先写的人不会被报复。</p>
    <div class="flex ac g8 wrapw" data-stars="${o.id}">
      ${[1, 2, 3, 4, 5].map(
        (n) => html`<button class="btn btn-quiet btn-sm" type="button" data-star="${n}" aria-pressed="false">${n} 星</button>`
      )}
    </div>
    <label class="label mt8" for="rc-${o.id}">说一句（可选，500 字内）</label>
    <textarea class="input" id="rc-${o.id}" rows="3" maxlength="500" placeholder="比如：交付干净，启动说明写得比我自己写的还清楚"></textarea>
    <div class="flex ac g8 wrapw mt8">
      <button class="btn btn-ink btn-sm" type="button" data-do-review="${o.id}">确认提交</button>
      <button class="btn btn-quiet btn-sm" type="button" data-close-box="${o.id}">先不评</button>
    </div>
  </div>`;
}

/* 双方评价的展示。每条都要带"谁写的"，否则两段话摆在一起分不清方向。
   时间只到"几天前": 精确到分钟对判断"这人靠不靠得住"没有帮助。 */
function reviewsBox(items) {
  if (!items.length) return html`<p class="tiny muted">还没有可见的评价</p>`;
  return html`<ul class="ditems">
    ${items.map(
      (r) => html`<li class="${r.mine ? 'don' : ''}">
        <span>${r.mine ? '我' : r.reviewer_name || '对方'} → ${r.mine ? '对方' : '我'}</span>
        <span>${'★'.repeat(Number(r.rating) || 0)}${'☆'.repeat(5 - (Number(r.rating) || 0))}</span>
        ${r.comment ? html`<span class="dnote">${r.comment}</span>` : html`<span class="tiny muted">（没写正文）</span>`}
      </li>`
    )}
  </ul>`;
}

/* 确认开工、退回，都要点两下。认领点错还能去别处找单，
   这两下点错就把一单推下去了或者推回去了，没有撤回。
   退回还要留一句原因: 接单方有权知道为什么没成。 */
function confirmUI(o) {
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
  } catch (e) {
    // 拉不到不挡大厅: 认领本身还能用, 成功后会本地补一条
  }

  // 没人接的需求不在订单里, 得单独拉一次。两个请求都失败也不影响看大厅,
  // 所以这里不把它塞进上面的 try——一个失败不该把另一个已经拿到的数据丢掉。
  try {
    const data = await api('GET', '/me/tasks');
    const items = Array.isArray(data.items) ? data.items : [];
    myOpenTasks = items.filter((t) => t.status === 'open');
  } catch (e) {
    myOpenTasks = [];
  }

  renderMine();
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

  // 发单入口对两种角色都开: 接单方也会有自己的需求要发(工具脚本、素材、外包),
  // 只给"发布方"看会把一个双边市场切成两半, 而且角色是登录时选的, 用户会选错。
  const pubBtn = document.getElementById('nav-publish');
  if (pubBtn) pubBtn.hidden = false;

  if (me && me.role !== 'provider') {
    // 待确认与待验收是两件事: 一个还没开工, 一个已经交了东西。
    // 混成一句"等你点头"，需求方会以为收到的是新认领。
    const toConfirm = inboxItems.filter((o) => o.needs_me && o.state === 'to_confirm').length;
    const toReview = inboxItems.filter((o) => o.needs_me && o.state === 'to_review').length;
    el.who.innerHTML = html`${name} · 发布方`;
    // 发布方点不了认领, 这句引导要跟着改, 不然是让人去做一件他做不了的事
    el.lead.textContent = '这些是平台上开放的需求。你发的单也在里面，标着「你发的」。';
    if (toReview > 0 || toConfirm > 0) {
      const parts = [];
      if (toReview > 0) parts.push(`${toReview} 单等你验收`);
      if (toConfirm > 0) parts.push(`${toConfirm} 单等你点头`);
      el.banner.innerHTML = html`<strong>有 ${parts.join('、')}。</strong>验收看交付内容对不对得上需求；点头看接单人的定位和信用分。 <button class="btn btn-line btn-sm" type="button" id="jump-mine">去处理</button>`;
    } else {
      el.banner.innerHTML = html`<strong>你现在是发布方。</strong>有人认领或交付你的需求时会出现在上面这块。 <a href="publish.html">发一条新需求 →</a>`;
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

  // 进行中的单在等接单方交付。这条要排在档案提醒前面:
  // 补档案是"以后更划算"，交付是"这一单现在卡着"。
  const toDeliver = mineItems.filter((o) => o.needs_me).length;
  if (toDeliver > 0) {
    el.banner.innerHTML = html`<strong>有 ${toDeliver} 单该交付了。</strong>交了之后单子进入待验收，需求方才有东西可以看。 <button class="btn btn-line btn-sm" type="button" id="jump-mine">去交付</button>`;
    el.banner.hidden = false;
    const jump = document.getElementById('jump-mine');
    if (jump) {
      jump.addEventListener('click', () => {
        mineOpen = true;
        renderMine();
        el.mine.scrollIntoView({ behavior: 'smooth', block: 'start' });
      });
    }
  } else if (pct < 100) {
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

/* ---------- 接单方: 交付 ---------- */

/* 交付是轻交易里唯一"由接单方推着走"的动作: 不点这一下, 单子永远停在进行中。
   提交前不再多一次确认框——盒子本身就是确认，再问一遍只是多一次点击。 */
async function doDeliver(id, btn) {
  const box = el.mineList.querySelector('[data-box="deliver-' + id + '"]');
  const contentEl = document.getElementById('dc-' + id);
  const linkEl = document.getElementById('dl-' + id);
  const content = contentEl ? contentEl.value.trim() : '';

  if (!content) {
    toast('写一句交付说明，需求方才好验收', 'warn');
    if (contentEl) contentEl.focus();
    return;
  }

  // 有清单就按项上报。没做完的项不写原因，验收时看到的就是一个没解释的空洞，
  // 需求方只能来回问，所以这一条在提交前拦掉。
  const items = Array.from(box ? box.querySelectorAll('[data-done]') : []).map((cb) => {
    const iid = cb.dataset.done;
    const noteEl = box.querySelector('[data-note="' + iid + '"]');
    return { item_id: iid, done: !!cb.checked, note: noteEl ? noteEl.value.trim() : '' };
  });
  const missing = items.find((it) => !it.done && !it.note);
  if (missing) {
    toast('第 ' + missing.item_id + ' 项没做完，写一句原因', 'warn');
    const ne = box ? box.querySelector('[data-note="' + missing.item_id + '"]') : null;
    if (ne) ne.focus();
    return;
  }

  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '提交中…';
  try {
    const o = await api('POST', '/orders/' + encodeURIComponent(id) + '/deliveries', {
      content: content,
      link: linkEl ? linkEl.value.trim() : '',
      items: items,
    });
    // 服务端回的是落库后的订单视图（含轮次与最近一轮内容），直接换掉这一行
    replaceItem(o);
    renderMine();
    toast('已交付，等' + (o.counterparty || '需求方') + '验收', 'ok');
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'ORDER_NOT_IN_PROGRESS' || code === 'ORDER_NOT_FOUND') {
      await loadOrders(); // 状态被别处改过（比如刚被打回又交过），让界面跟上
      toast(err.message || '这一单现在不能交付', 'warn');
      return;
    }
    toast((err && err.message) || '没交上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

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

async function doAccept(id, btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '验收中…';
  try {
    const o = await api('POST', '/orders/' + encodeURIComponent(id) + '/accept');
    replaceItem(o);
    renderMine();
    toast('已验收，这一单完成', 'ok');
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'ORDER_NOT_TO_REVIEW' || code === 'ORDER_NOT_FOUND') {
      await loadOrders(); // 状态被别处改过, 让界面跟上
      toast(err.message || '这一单现在不在待验收', 'warn');
      return;
    }
    toast((err && err.message) || '没提交上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

/* 提交互评。分数必须显式选——默认给 5 星等于替用户表了态。 */
async function doReview(id, btn) {
  const stars = Number(starPick[id]) || 0;
  const input = document.getElementById('rc-' + id);
  const comment = input ? input.value.trim() : '';

  if (!stars) {
    toast('先选 1 到 5 星', 'warn');
    return;
  }

  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '提交中…';
  try {
    const data = await api('POST', '/orders/' + encodeURIComponent(id) + '/reviews', {
      rating: stars,
      comment: comment,
    });
    delete starPick[id];
    await loadOrders();
    renderMine();
    if (data && data.counterpart_submitted) {
      toast('评价已提交，双方都评完了，现在可以互相看见', 'ok');
    } else {
      toast('评价已提交。等对方也评完，两份评价才互相可见', 'ok');
    }
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    // 已评过 / 单没走完: 都不是"再试一次"能解决的, 先把界面拉回真实状态。
    if (code === 'ALREADY_REVIEWED' || code === 'ORDER_NOT_COMPLETED' || code === 'ORDER_NOT_FOUND') {
      await loadOrders();
      renderMine();
      toast(err.message || '这一单现在不能评价', 'warn');
      return;
    }
    toast((err && err.message) || '没提交上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

/* 拉双方评价。theirs 为空时要说出"为什么空"——
   接口空着不解释, 用户会读成"对方没评我", 而实际上可能只是还没揭盲。 */
async function doShowReviews(id) {
  const holder = el.mineList.querySelector('[data-reviews="' + id + '"]');
  if (!holder) return;
  try {
    const data = await api('GET', '/orders/' + encodeURIComponent(id) + '/reviews');
    const mine = (data && data.mine) || [];
    const theirs = (data && data.theirs) || [];
    const box = el.mineList.querySelector('[data-box="show-' + id + '"]');
    if (box) box.hidden = false;
    holder.innerHTML = reviewsBox(mine.concat(theirs));
    if (!theirs.length) {
      toast(data && data.counterpart_submitted
        ? '对方还没到揭盲条件，还要再等等'
        : '对方还没评，双方都评完才互相可见', 'warn');
    }
  } catch (err) {
    holder.innerHTML = '<p class="tiny muted">评价没读上来，刷新再试</p>';
  }
}

async function doRevise(id, btn) {
  const label = btn.textContent;
  const box = el.mineList.querySelector('[data-box="revise-' + id + '"]');
  const noteEl = document.getElementById('rv-' + id);
  const note = noteEl ? noteEl.value.trim() : '';

  // 空原因在提交前拦掉: 说了等于没说的打回, 接单方下一轮只能瞎猜
  if (!note) {
    toast('写一句要改什么，接单方才知道怎么改', 'warn');
    if (noteEl) noteEl.focus();
    return;
  }

  btn.disabled = true;
  btn.textContent = '打回中…';
  try {
    const o = await api('POST', '/orders/' + encodeURIComponent(id) + '/revisions', { reason: note });
    replaceItem(o);
    renderMine();
    toast('已打回，等' + (o.counterparty || '接单方') + '再交一版', 'ok');
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    // 轮次用完是这一格特有的死路: 只能验收或走纠纷, 得让界面显示出来
    if (code === 'REVISION_LIMIT' || code === 'ORDER_NOT_TO_REVIEW' || code === 'ORDER_NOT_FOUND') {
      await loadOrders();
      toast(err.message || '这一单现在不能打回', 'warn');
      return;
    }
    toast((err && err.message) || '没打回上，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

/* 撤回。成功之后这张卡直接从"我发的单"里消失, 而不是原地改成"已撤回":
   这一格存在的意义是"还没人接、还等着人", 撤回的单不再等任何人,
   留在列表里只会让"挂着 N 条"这个数字对不上。 */
async function doWithdraw(id, btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = '撤回中…';
  try {
    await api('POST', '/tasks/' + encodeURIComponent(id) + '/withdraw');
    myOpenTasks = myOpenTasks.filter((t) => t.id !== id);
    renderMine();
    toast('已撤回，这条不会再出现在大厅', 'ok');
    load(true); // 大厅列表里也得跟着没有
  } catch (err) {
    const code = err instanceof ApiError ? err.code : '';
    if (code === 'TASK_ALREADY_TAKEN' || code === 'TASK_NOT_OPEN' || code === 'TASK_CHANGED') {
      // 状态在别处变了(有人接了), 界面不能还显示"还没人接"
      await loadOrders();
      load(true);
      toast(err.message || '这一单状态变了，刷新看看', 'warn');
      return;
    }
    toast((err && err.message) || '没撤回，再试一次', 'err');
    btn.disabled = false;
    btn.textContent = label;
  }
}

el.mineList.addEventListener('click', (e) => {
  const askW = e.target.closest('[data-ask-withdraw]');
  if (askW) {
    openBox(askW.dataset.askWithdraw, 'withdraw');
    return;
  }
  const goW = e.target.closest('[data-do-withdraw]');
  if (goW) {
    doWithdraw(goW.dataset.doWithdraw, goW);
    return;
  }
  const askC = e.target.closest('[data-ask-confirm]');
  if (askC) {
    openBox(askC.dataset.askConfirm, 'confirm');
    return;
  }
  const askA = e.target.closest('[data-ask-accept]');
  if (askA) {
    openBox(askA.dataset.askAccept, 'accept');
    return;
  }
  const askR = e.target.closest('[data-ask-revise]');
  if (askR) {
    openBox(askR.dataset.askRevise, 'revise');
    return;
  }
  const askD = e.target.closest('[data-ask-decline]');
  if (askD) {
    openBox(askD.dataset.askDecline, 'decline');
    return;
  }
  const askDel = e.target.closest('[data-ask-deliver]');
  if (askDel) {
    openBox(askDel.dataset.askDeliver, 'deliver');
    return;
  }
  const askRv = e.target.closest('[data-ask-review]');
  if (askRv) {
    openBox(askRv.dataset.askReview, 'review');
    return;
  }
  // 星级: 点第 n 个按钮就把 1..n 全点亮。
  // 用 aria-pressed 而不是自己编 class, 屏幕阅读器和测试脚本都能读到同一个状态。
  const star = e.target.closest('[data-star]');
  if (star) {
    const group = star.closest('[data-stars]');
    const oid = group.dataset.stars;
    starPick[oid] = Number(star.dataset.star);
    group.querySelectorAll('[data-star]').forEach((b) => {
      const on = Number(b.dataset.star) <= starPick[oid];
      b.setAttribute('aria-pressed', on ? 'true' : 'false');
      b.classList.toggle('btn-ink', on);
      b.classList.toggle('btn-quiet', !on);
    });
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
  if (goD) {
    doDecline(goD.dataset.doDecline, goD);
    return;
  }
  const goDel = e.target.closest('[data-do-deliver]');
  if (goDel) {
    doDeliver(goDel.dataset.doDeliver, goDel);
    return;
  }
  const goA = e.target.closest('[data-do-accept]');
  if (goA) {
    doAccept(goA.dataset.doAccept, goA);
    return;
  }
  const goR = e.target.closest('[data-do-revise]');
  if (goR) {
    doRevise(goR.dataset.doRevise, goR);
    return;
  }
  const goRv = e.target.closest('[data-do-review]');
  if (goRv) {
    doReview(goRv.dataset.doReview, goRv);
    return;
  }
  const showRv = e.target.closest('[data-show-reviews]');
  if (showRv) {
    doShowReviews(showRv.dataset.showReviews);
    return;
  }
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

  const params = new URLSearchParams(location.search);
  if (params.get('saved') === '1') {
    toast('档案已保存，这里是现在能接的需求', 'ok');
  }

  const newId = params.get('new');
  await load(true);
  if (newId) markNew(newId);
}

/* 发完单回到大厅，把自己刚发的那条指出来。
   大厅按最新排序, 它本来就在第一张; 但"排在第一"和"这是我刚发的"是两件事——
   不点出来, 用户会疑心到底发成功了没有, 然后去翻「我的需求」再确认一遍。 */
function markNew(id) {
  const node = el.list.querySelector('[data-card="' + id + '"]');
  // 参数用完就摘掉, 否则刷新页面会再提示一次"新需求已经发出"
  const url = new URL(location.href);
  url.searchParams.delete('new');
  history.replaceState(null, '', url.pathname + (url.search || '') + url.hash);

  if (!node) {
    toast('新需求已经发出，大厅里能搜到', 'ok');
    return;
  }
  node.classList.add('is-new');
  node.scrollIntoView({ block: 'center', behavior: 'smooth' });
  toast('新需求已经发出，就是下面这一条', 'ok');
  setTimeout(() => node.classList.remove('is-new'), 4000);
}

boot();

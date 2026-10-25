/**
 * take.js — 有人把这一单发给了你。
 *
 * 这一页的全部任务是: 让一个刚点开陌生链接的人在 10 秒内判断
 * 「这是不是我能干的活、给多少钱、我要不要接」。
 * 所以顺序是 金额 → 标题 → 要做什么 → 怎么验收, 而不是先解释平台是什么。
 *
 * 一个原则: 不能接的时候，先说清为什么，再给出口。
 * 「点一下试试」在这里代价特别高——这个人对平台的全部了解就是这一屏，
 * 第一屏就被拒的人不会看第二屏。
 */
import { api, html, esc, toast } from './runtime.js';

const taskId = new URLSearchParams(location.search).get('t') || '';
const $ = (id) => document.getElementById(id);

function amount(cents) {
  const v = Number(cents) || 0;
  const yuan = v / 100;
  // 整数元不显示 .00：一屏里只有一个数字的时候，两位小数是噪音。
  return Number.isInteger(yuan) ? '¥' + yuan.toLocaleString('zh-CN') : '¥' + yuan.toFixed(2);
}

function deadlineText(ms) {
  if (!ms) return '没有硬性截止';
  const d = new Date(Number(ms));
  if (Number.isNaN(d.getTime())) return '没有硬性截止';
  const days = Math.ceil((d.getTime() - Date.now()) / 86400000);
  const ymd = d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  if (days < 0) return '截止已过（' + ymd + '）';
  if (days === 0) return '今天截止 · ' + ymd;
  return '还有 ' + days + ' 天 · ' + ymd;
}

function show(id, on) {
  const n = $(id);
  if (n) n.hidden = !on;
}

/* ---------- 三种"接不了"，各给一个真正能走的路 ---------- */

function blocked(title, text, acts) {
  show('loading', false);
  show('main', false);
  show('blocked', true);
  $('bk-h').textContent = title;
  $('bk-p').textContent = text;
  $('bk-acts').innerHTML = acts
    .map((a) => html`<a class="btn ${a.ink ? 'btn-ink' : 'btn-line'} btn-sm" href="${a.href}">${a.label}</a>`)
    .join('');
}

function actionsFor(v) {
  switch (v.why_code) {
    case 'PROFILE_INCOMPLETE':
      return [{ label: '先把档案填了', href: 'profile.html', ink: true }];
    case 'ROLE_NOT_PROVIDER':
      // 身份在注册时定，之后不能改——所以这里不能给一个"切换身份"的按钮，
      // 给了一个点不动的东西比不给更糟。
      return [{ label: '去大厅看看', href: 'hall.html' }];
    case 'TASK_TAKEN':
      return [{ label: '大厅里还有别的', href: 'hall.html', ink: true }];
    case 'SELF_CLAIM':
      return [{ label: '看这一单的进展', href: 'hall.html', ink: true }];
    default:
      return [{ label: '去大厅看看', href: 'hall.html', ink: true }];
  }
}

async function boot() {
  if (!taskId) {
    blocked('这个链接不完整', '地址里少了单号。请让对方重新发一次链接给你。', [
      { label: '去大厅看看', href: 'hall.html', ink: true },
    ]);
    return;
  }

  let data;
  try {
    data = await api('GET', '/tasks/' + encodeURIComponent(taskId));
  } catch (err) {
    if (err.code === 'TASK_NOT_FOUND') {
      blocked('这一单不在了', '可能已经做完、撤回，或者链接发错了。', [
        { label: '去大厅看看', href: 'hall.html', ink: true },
      ]);
      return;
    }
    blocked('没打开这一单', err.message || '网络不好，刷新一下再试。', [
      { label: '刷新', href: location.href, ink: true },
    ]);
    return;
  }

  const t = data.task || {};
  const v = data.viewer || {};
  show('loading', false);

  // 不是开放状态：这一单已经有人接了 / 做完了。不把需求书全文摊给外人看。
  if (t.status !== 'open') {
    blocked(
      '这一单「' + (t.state_label || '已经不在开放状态') + '」',
      t.status === 'matched'
        ? '已经有人接了。对方先一步，这一单就不在大厅里了。'
        : '这一单已经' + (t.state_label || '收工') + '，不再接新的认领。',
      [{ label: '大厅里还有别的', href: 'hall.html', ink: true }]
    );
    return;
  }

  $('crumb').hidden = false;
  show('main', true);
  $('cat').textContent = t.category_name || '需求';
  $('state').textContent = t.state_label || '开放中';
  $('title').textContent = t.title || '—';
  $('budget').textContent = amount(t.budget_cents);
  $('deadline').textContent = deadlineText(t.deadline_at);
  $('spec').textContent = t.spec || '发单的人没写详细说明。';
  $('who').textContent = t.requester || '需求方';
  $('credit').textContent = t.requester_credit ? '信用 ' + t.requester_credit : '';

  if (t.deliverables) {
    $('deli').textContent = t.deliverables;
    show('deli-wrap', true);
  }
  if (t.required_format) {
    $('fmt').textContent = t.required_format;
    show('fmt-wrap', true);
  }
  document.title = t.title + ' · zanbuddy 联赞';

  const btn = $('claim');
  const extra = $('act-extra');

  // 没登录：按钮先去登录，回来还是这一页。
  // next 用短链 /t/xxx 而不是本页长地址——回来的时候地址栏还是那条能念的链接。
  if (!v.logged_in) {
    btn.textContent = '登录后接这一单';
    btn.addEventListener('click', () => {
      location.href = 'login.html?next=' + encodeURIComponent('/t/' + taskId);
    });
    $('act-note').textContent = '登录只需要一个手机号。这一阶段钱不过平台，接不了也没有损失。';
    return;
  }

  // 登录了但接不了：把原因摆在按钮旁边，别让他点了才知道。
  if (!v.can_claim) {
    $('act-h').textContent = '这一单你现在接不了';
    $('act-p').textContent = v.why_not || '条件还不满足。';
    btn.hidden = true;
    $('act-note').hidden = true;
    if (v.why_hint) {
      extra.innerHTML = html`<p class="tiny muted mb8">${v.why_hint}</p>`;
    }
    extra.innerHTML += actionsFor(v)
      .map((a) => html`<a class="btn btn-line btn-sm btn-block" href="${a.href}">${a.label}</a>`)
      .join('');
    return;
  }

  btn.addEventListener('click', async () => {
    btn.disabled = true;
    const old = btn.textContent;
    btn.textContent = '正在接…';
    try {
      const o = await api('POST', '/tasks/' + encodeURIComponent(taskId) + '/take', {});
      show('main', false);
      show('blocked', true);
      $('bk-h').textContent = '接下了';
      $('bk-p').textContent =
        '等' + (t.requester || '发单的人') + '点确认开工，这一单就开始。' +
        '在那之前他随时可以退回大厅，你不会被扣任何东西。';
      $('bk-acts').innerHTML = html`
        <a class="btn btn-ink btn-sm" href="hall.html">看我的单</a>
        <span class="tiny muted mono">${o.id || ''}</span>`;
    } catch (err) {
      btn.disabled = false;
      btn.textContent = old;
      // 被抢了：这一单已经不在 open，刷新一次让页面自己变成"已有人接"。
      if (err.code === 'TASK_TAKEN') {
        toast('这一单刚被接走了', 'warn');
        setTimeout(() => location.reload(), 900);
        return;
      }
      toast(err.message || '没接上，请再试一次', 'err');
      if (err.hint) extra.innerHTML = html`<p class="tiny muted mt8">${esc(err.hint)}</p>`;
    }
  });
}

boot();

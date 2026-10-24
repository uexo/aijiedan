import { api, html } from './runtime.js';

/* 「谁能接」三卡。
   放在一个模块里，是因为它要出现在两个地方：发单成功当场、大厅「我发的单」点开。
   两处各写一遍，早晚会漂成两种说法——一张卡写「完成 3 单」，另一张写「3 单成交」。

   一条硬规矩：卡上不出现"匹配度 87.3"这种数。分数是给人排序用的，
   摆到需求方面前的必须是一句能指认的话：对得上哪几项技能、完成过哪一单。
   理由是后端 match.BuildReasons 产出的，没有锚点的理由根本不会到这里来。 */

const MAX_REASONS = 3;
const MAX_SKILLS = 4;

export async function fetchCandidates(taskId) {
  const data = await api('GET', '/tasks/' + encodeURIComponent(taskId) + '/matches');
  const items = Array.isArray(data && data.candidates) ? data.candidates : [];
  return { items: items, count: Number((data && data.count) || items.length) };
}

/* 一个候选人的数字。算不出来的不编：
   没有完成单就直说没有，而不是隐藏这一行让人以为"平台没统计"。 */
function statLine(w) {
  const s = (w && w.stats) || {};
  const orders = Number(s.orders) || 0;
  const parts = [orders > 0 ? '完成 ' + orders + ' 单' : '还没在平台上完成过单'];
  if (Number(s.avg_days) > 0) parts.push('平均 ' + Number(s.avg_days) + ' 天交付');
  if (Number(s.repeat) > 0) parts.push(Number(s.repeat) + ' 个回头客');
  return parts.join(' · ');
}

/* 点开一条理由看到的东西。锚点是什么就说什么，不美化：
   技能是他自己填的（平台没验过），订单是平台上真实完成的（查得到单号）。
   两件事的分量不一样，写成一个样等于骗人。 */
function anchorLine(a) {
  const type = (a && a.type) || '';
  const ref = (a && a.ref) || '';
  const label = (a && a.label) || '';
  if (type === 'skill') {
    return '档案技能标签：' + ref.replace(/^skill:/, '') + ' · 本人自填，平台未验';
  }
  if (type === 'order') {
    return label ? '订单 ' + ref + '（' + label + '）' : '订单 ' + ref;
  }
  if (type === 'endorsement') {
    return label ? '背书 ' + ref + '（' + label + '）' : '背书 ' + ref;
  }
  return label || ref || '没有可指的凭据';
}

function card(w, i) {
  const key = w.match_id || 'w' + i;
  const skills = (w.skills || []).slice(0, MAX_SKILLS);
  const reasons = (w.reasons || []).slice(0, MAX_REASONS);
  // 只认能点开的地址: 后端已经滤过一遍, 这里再滤是因为脏数据不该变成
  // 卡片上一个点了没反应的「作品」——那比不显示更伤信任。
  const work = (w.portfolio || []).find((p) => p && /^https?:\/\//i.test(p.url || ''));

  return html`
  <li class="wcard" data-who="${key}">
    <div class="wtop">
      <span class="wrank mono">${i + 1}</span>
      <div class="wgrow">
        <strong>${w.name || '接单方'}</strong>
        ${w.headline ? html`<span class="tiny muted">${w.headline}</span>` : ''}
      </div>
    </div>
    <div class="tiny muted wstat">${statLine(w)}</div>
    ${skills.length
      ? html`<div class="wskills">${skills.map((s) => html`<span class="tag tag-mono">${s}</span>`)}</div>`
      : ''}
    ${work
      ? html`<p class="tiny wwork"><a href="${work.url}" target="_blank" rel="noopener noreferrer">作品：${work.label || work.url} ↗</a></p>`
      : ''}
    ${reasons.length
      ? html`<ul class="wreasons">
          ${reasons.map(
            (r, k) => html`<li>
              <button class="wwhy" type="button" data-why="${key}-${k}" aria-expanded="false">
                <span>${r.text}</span><span class="wmore">看凭据 ↗</span>
              </button>
              <div class="wbox" hidden data-whybox="${key}-${k}">
                <span class="tiny mono">${anchorLine(r.anchor)}</span>
              </div>
            </li>`
          )}
        </ul>`
      : html`<p class="tiny muted mt8">这一位没有可以指认的凭据，所以这里不写理由。</p>`}
  </li>`;
}

/* 整块渲染。空的时候要说清"为什么一个人都算不出来"——
   一句"暂无推荐"会让人以为平台在敷衍，而真实原因通常是还没有人建档。 */
export function whoCards(data) {
  const items = (data && data.items) || [];
  if (!items.length) {
    return html`<p class="tiny muted whow-empty">
      现在还没有建档的接单方，算不出谁能接这一单。单子已经挂在大厅，
      有人补全档案后，就会被算到这一单上来。
    </p>`;
  }
  return html`
    <ul class="wlist">${items.map(card)}</ul>
    <p class="tiny muted whow-foot">
      按对得上的程度排，就这 ${items.length} 位。现在还不能直接指派：单子挂在大厅，
      谁先认领谁接，认领之后由你确认开工。
    </p>`;
}

/* 点理由展开凭据。挂在容器上而不是每张卡上：
   卡片是整块 innerHTML 换掉的，逐个绑事件每次重渲染都要重来一遍。 */
export function bindWhy(root) {
  root.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-why]');
    if (!btn || !root.contains(btn)) return;
    const key = btn.dataset.why;
    const sel = '[data-whybox="' + (window.CSS && CSS.escape ? CSS.escape(key) : key) + '"]';
    const box = root.querySelector(sel);
    if (!box) return;
    const open = box.hidden;
    box.hidden = !open;
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
}

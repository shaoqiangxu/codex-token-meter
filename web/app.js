const $ = selector => document.querySelector(selector);
let state = {sessions: [], hosts: [], totals: {}, project_totals: [], exchange_rate: {}};

function trimNumber(value, digits) {
  if (!Number.isFinite(Number(value))) return '0';
  return String(Number(Number(value).toFixed(digits)));
}

function token(value) {
  const number = Number(value || 0);
  const absolute = Math.abs(number);
  if (absolute >= 1000000) {
    const digits = absolute < 10000000 ? 2 : absolute < 100000000 ? 1 : 0;
    return `${trimNumber(number / 1000000, digits)}M`;
  }
  return String(Math.round(number));
}

function money(value) {
  const number = Number(value || 0);
  const digits = Math.abs(number) > 0 && Math.abs(number) < 0.01 ? 6 : 4;
  return `$${trimNumber(number, digits)}`;
}

function cny(value) {
  const number = Number(value || 0);
  const digits = Math.abs(number) > 0 && Math.abs(number) < 1 ? 4 : 2;
  return `¥${trimNumber(number, digits)}`;
}

function credits(value) {
  return `${trimNumber(Number(value || 0), 2)}credits`;
}

function esc(value) {
  return String(value || '').replace(/[&<>"']/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[char]));
}

function shortID(value) {
  return String(value || '').replace(/^ctco_|^fco_/, '').slice(0, 8) || '-';
}

function ago(value) {
  if (!value) return '-';
  const seconds = Math.max(0, (Date.now() - new Date(value)) / 1000);
  if (seconds < 60) return `${Math.floor(seconds)}秒前`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}时前`;
  return `${Math.floor(seconds / 86400)}天前`;
}

function duration(value) {
  if (!value) return '-';
  const seconds = Math.max(0, (Date.now() - new Date(value)) / 1000);
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}时`;
  return `${Math.floor(seconds / 86400)}天`;
}

function stamp(value) {
  if (!value) return '未验证';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? esc(value) : date.toLocaleString('zh-CN', {hour12: false});
}

function card(label, value, note = '') {
  return `<div class="card"><div class="label">${label}</div><div class="value">${value}</div><div class="note">${note}</div></div>`;
}

function render(snapshot) {
  state = snapshot;
  const totals = snapshot.totals || {};
  const fx = snapshot.exchange_rate || {};
  const pending = (snapshot.sessions || []).some(item => item.status === 'LOWER_BOUND');
  const fxState = fx.stale ? '汇率已过期' : '每6小时同步';
  $('#cards').innerHTML =
    card('今日精确 Token', token(totals.total_tokens), '已结算 EXACT') +
    card('当前生成中估算', pending && !totals.live_estimate ? '等待精确值' : token(totals.live_estimate), pending && !totals.live_estimate ? '生成完成后更新' : '仅可见文本估算') +
    card('总输入 / 输出', `${token(totals.input_tokens)}/${token(totals.output_tokens)}`, `推理输出${token(totals.reasoning_output_tokens)}`) +
    card('缓存读取 / 写入', `${token(totals.cached_input_tokens)}/${token(totals.cache_write_input_tokens)}`, `命中率${trimNumber(totals.cache_hit_rate || 0, 1)}%`) +
    card('活跃会话 / 在线设备', `${totals.active_sessions || 0}/${totals.online_hosts || 0}`, '5分钟内有事件 · 按设备和父会话去重') +
    card('OpenAI API 等价', money(totals.api?.value), `${cny(totals.api_cny)} · 官方费率每日同步 · ${stamp(totals.api?.verified_at)}`) +
    card('Vercel 等价', money(totals.vercel?.value), `${cny(totals.vercel_cny)} · 模型目录每日同步 · ${stamp(totals.vercel?.verified_at)}`) +
    card('Codex Credits 等价', credits(totals.credits?.value), totals.credits_purchase_usd ? `购买均价${money(totals.credits_purchase_usd)}/${cny(totals.credits_purchase_cny)}` : '未录入购买批次') +
    card('实时 USD/CNY', fx.rate ? `1USD=${cny(fx.rate)}` : '等待汇率', `ECB ${fx.rate_date || '-'} · ${fxState}`) +
    card('实际新增现金支出', money(totals.actual_incremental_cash), '套餐内会话为0');
  renderGroups();
  $('#updated').textContent = `最后更新 ${new Date(snapshot.generated_at).toLocaleString('zh-CN', {hour12: false})}`;
}

const sumFields = ['live_estimate', 'total_tokens', 'input_tokens', 'cached_input_tokens', 'cache_write_input_tokens', 'output_tokens', 'reasoning_output_tokens', 'api_cost', 'vercel_cost', 'credits'];

function displayProjectName(value, conversationName) {
  const raw = String(value || '').trim();
  const generic = !raw || ['repo', 'root', 'workspace', 'worktree'].includes(raw.toLowerCase()) || /^\d+$/.test(raw);
  if (!generic) return raw;
  const title = String(conversationName || '').trim();
  const quotedProject = title.match(/[“"]([^“”"]{2,80})[”"]项目/i);
  if (quotedProject) return quotedProject[1];
  if (title && !/^会话[0-9a-f-]+$/i.test(title)) return title;
  return '未归属项目';
}

function groupedSessions(rows) {
  const groups = new Map();
  for (const row of rows) {
    const rootID = row.parent_conversation_id || row.conversation_id;
    const key = `${row.host_id}\u0000${rootID}`;
    if (!groups.has(key)) {
      const group = {host_id: row.host_id, host: row.host, platform: row.platform, root_id: rootID, records: []};
      for (const field of sumFields) group[field] = 0;
      groups.set(key, group);
    }
    const group = groups.get(key);
    group.records.push(row);
    for (const field of sumFields) group[field] += Number(row[field] || 0);
    if (row.conversation_id === rootID) group.root = row;
    if (!group.started_at || new Date(row.started_at) < new Date(group.started_at)) group.started_at = row.started_at;
    if (!group.last_event_at || new Date(row.last_event_at) > new Date(group.last_event_at)) {
      group.last_event_at = row.last_event_at;
      group.latest = row;
    }
  }
  const result = [...groups.values()];
  for (const group of result) {
    const representative = group.root || group.latest || group.records[0];
    const rawProject = representative.project || group.records.find(item => item.project)?.project || '';
    group.name = representative.name && representative.name !== rawProject ? representative.name : `会话${shortID(group.root_id)}`;
    group.project = displayProjectName(rawProject, group.name);
    group.model = representative.model || '-';
    group.reasoning_effort = representative.reasoning_effort || '-';
    group.status = (group.latest || representative).status || 'EXACT';
    group.data_quality = representative.data_quality || '-';
    group.cache_hit_rate = group.input_tokens > 0 ? group.cached_input_tokens / group.input_tokens * 100 : 0;
    group.records.sort((left, right) => new Date(right.last_event_at) - new Date(left.last_event_at));
  }
  return result.sort((left, right) => new Date(right.last_event_at) - new Date(left.last_event_at));
}

function deviceName(platform) {
  if (platform === 'windows') return ['🖥️', 'Windows电脑'];
  if (platform === 'linux') return ['☁️', 'Linux VPS'];
  return ['◻️', '未知设备'];
}

function statusPill(status) {
  const className = status === 'EXACT' ? 'exact' : status === 'ESTIMATED_LIVE' || status === 'LOWER_BOUND' ? 'estimated' : 'stale';
  return `<span class="pill ${className}">${esc(status)}</span>`;
}

function metric(label, value, note = '') {
  return `<div class="metric"><div class="metric-label">${label}</div><div class="metric-value">${value}</div>${note ? `<div class="pricing-note">${note}</div>` : ''}</div>`;
}

function recordRows(records) {
  return records.map(record => `<tr>
    <td>${record.parent_conversation_id ? '内部调用' : '主会话'}</td>
    <td>${esc(shortID(record.conversation_id))}</td>
    <td>${esc(record.model)}</td>
    <td>${esc(record.reasoning_effort)}</td>
    <td>${token(record.total_tokens)}</td>
    <td>${token(record.input_tokens)}</td>
    <td>${token(record.output_tokens)}</td>
    <td>${ago(record.last_event_at)}</td>
    <td><button type="button" class="detail-button secondary" data-detail="${esc(record.conversation_id)}">详情</button></td>
  </tr>`).join('');
}

function sessionCard(group) {
  const fx = Number(state.exchange_rate?.rate || 0);
  return `<article class="session-card">
    <div class="session-heading">
      <span class="session-title">${esc(group.name)}</span>
      <span class="session-id">${esc(shortID(group.root_id))}</span>
      ${statusPill(group.status)}
      <span>${esc(group.model)} · ${esc(group.reasoning_effort)}</span>
      <span class="session-time">${duration(group.started_at)} · ${ago(group.last_event_at)}</span>
    </div>
    <div class="session-metrics">
      ${metric('总Token', token(group.total_tokens), `${group.records.length}条记录`)}
      ${metric('输入', token(group.input_tokens))}
      ${metric('缓存读取', token(group.cached_input_tokens), `${trimNumber(group.cache_hit_rate, 1)}%`)}
      ${metric('缓存写入', token(group.cache_write_input_tokens))}
      ${metric('输出', token(group.output_tokens), `推理${token(group.reasoning_output_tokens)}`)}
      ${metric('OpenAI', money(group.api_cost), cny(group.api_cost * fx))}
      ${metric('Vercel', money(group.vercel_cost), cny(group.vercel_cost * fx))}
      ${metric('Credits', credits(group.credits))}
    </div>
    <details class="child-records">
      <summary>展开${group.records.length}条原始记录（内部工具调用已聚拢）</summary>
      <div class="table-wrap"><table><thead><tr><th>类型</th><th>ID</th><th>模型</th><th>推理</th><th>总Token</th><th>输入</th><th>输出</th><th>最后事件</th><th>操作</th></tr></thead><tbody>${recordRows(group.records)}</tbody></table></div>
    </details>
  </article>`;
}

function renderGroups() {
  const query = $('#filter').value.trim().toLowerCase();
  const groups = groupedSessions(state.sessions || []).filter(group => !query || JSON.stringify([
    group.host, group.platform, group.project, group.name, group.model, group.root_id,
    ...group.records.map(item => item.conversation_id)
  ]).toLowerCase().includes(query));
  const hosts = new Map();
  for (const host of state.hosts || []) hosts.set(host.host_id, host);
  for (const group of groups) {
    if (!hosts.has(group.host_id)) hosts.set(group.host_id, {host_id: group.host_id, alias: group.host, platform: group.platform, online: false});
  }

  const html = [...hosts.values()].map(host => {
    const hostGroups = groups.filter(group => group.host_id === host.host_id);
    if (query && !hostGroups.length && !JSON.stringify(host).toLowerCase().includes(query)) return '';
    const [icon, kind] = deviceName(host.platform);
    const rawCount = hostGroups.reduce((total, group) => total + group.records.length, 0);
    const projects = new Map();
    for (const group of hostGroups) {
      if (!projects.has(group.project)) projects.set(group.project, []);
      projects.get(group.project).push(group);
    }
    const projectHTML = [...projects.entries()].map(([project, projectGroups]) => {
      const totals = {total_tokens: 0, api_cost: 0, vercel_cost: 0, credits: 0, records: 0};
      for (const group of projectGroups) {
        totals.total_tokens += Number(group.total_tokens || 0);
        totals.api_cost += Number(group.api_cost || 0);
        totals.vercel_cost += Number(group.vercel_cost || 0);
        totals.credits += Number(group.credits || 0);
        totals.records += group.records.length;
      }
      const exactTotals = (state.project_totals || []).find(item => item.host_id === host.host_id && item.project === project);
      if (exactTotals) {
        totals.total_tokens = Number(exactTotals.total_tokens || 0);
        totals.api_cost = Number(exactTotals.api_cost || 0);
        totals.vercel_cost = Number(exactTotals.vercel_cost || 0);
        totals.credits = Number(exactTotals.credits || 0);
        totals.records = Number(exactTotals.records || 0);
      }
      const sessionCount = exactTotals ? Number(exactTotals.sessions || 0) : projectGroups.length;
      const fx = Number(state.exchange_rate?.rate || 0);
      return `<details class="project-group" open>
        <summary>
          <span class="project-title">📁 ${esc(project)}</span>
          <span class="project-totals"><span>Token ${token(totals.total_tokens)}</span><span>OpenAI ${money(totals.api_cost)}/${cny(totals.api_cost * fx)}</span><span>Vercel ${money(totals.vercel_cost)}/${cny(totals.vercel_cost * fx)}</span><span>${credits(totals.credits)}</span></span>
          <span class="project-count">${sessionCount}个会话 · ${totals.records}条记录</span>
        </summary>
        <div class="project-sessions">${projectGroups.map(sessionCard).join('')}</div>
      </details>`;
    }).join('');
    const empty = projectHTML || '<div class="empty-device">此时间范围暂无会话，但设备仍在正常上报心跳。</div>';
    return `<details class="device-group" open>
      <summary><span class="device-icon">${icon}</span><span class="device-name">${esc(host.alias)}</span><span class="device-kind">${kind}</span><span class="pill ${host.online ? 'online' : 'offline'}">${host.online ? '在线' : '离线'}</span><span class="device-count">${hostGroups.length}个聚合会话 · ${rawCount}条原始记录</span></summary>
      <div class="project-list">${empty}</div>
    </details>`;
  }).join('');
  $('#sessionGroups').innerHTML = html || '<div class="empty-device">没有符合条件的会话。</div>';
}

function csrf() {
  return decodeURIComponent((document.cookie.match(/(?:^|; )meter_csrf=([^;]+)/) || [])[1] || '');
}

async function renameSession(id) {
  const name = $('#rename').value;
  const response = await fetch(`/api/sessions/${encodeURIComponent(id)}`, {method: 'PATCH', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf()}, body: JSON.stringify({name})});
  if (response.ok) {
    $('#dialog').close();
    loadPeriod();
  }
}

async function detail(id) {
  const response = await fetch(`/api/sessions/${encodeURIComponent(id)}`);
  if (!response.ok) return;
  const data = await response.json();
  $('#dialogTitle').textContent = `记录 ${shortID(id)}`;
  $('#dialogBody').innerHTML = `<p>这里只展示计量字段，不显示Prompt、推理正文或工具结果。</p><p><input id="rename" maxlength="100" placeholder="会话名称"><button id="renameBtn">保存名称</button></p><div class="table-wrap"><table><thead><tr><th>时间</th><th>输入</th><th>缓存</th><th>写缓存</th><th>输出</th><th>推理</th><th>总计</th><th>质量</th><th>Parser</th></tr></thead><tbody>${(data.events || []).map(event => `<tr><td>${esc(event.timestamp)}</td><td>${token(event.input_tokens)}</td><td>${token(event.cached_input_tokens)}</td><td>${token(event.cache_write_input_tokens)}</td><td>${token(event.output_tokens)}</td><td>${token(event.reasoning_output_tokens)}</td><td>${token(event.total_tokens)}</td><td>${esc(event.data_quality)}</td><td>${esc(event.parser_version)}</td></tr>`).join('')}</tbody></table></div>`;
  $('#renameBtn').onclick = () => renameSession(id);
  $('#dialog').showModal();
}

async function enroll(platform) {
  const response = await fetch('/api/enrollments', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf()}, body: JSON.stringify({platform})});
  const data = await response.json();
  $('#dialogTitle').textContent = `添加${platform === 'windows' ? 'Windows电脑' : 'Linux VPS'}`;
  $('#dialogBody').innerHTML = `<p>命令15分钟内有效，包含一次性Token，不含永久Agent Token。</p><div class="code">${esc(data.command || '生成失败')}</div>`;
  $('#dialog').showModal();
}

async function savePurchase() {
  const value = {purchase_time: $('#pt').value ? new Date($('#pt').value).toISOString() : '', paid_amount: Number($('#pa').value), currency: 'USD', credits_received: Number($('#pc').value), fees: Number($('#pf').value || 0), exchange_rate: Number($('#px').value)};
  const response = await fetch('/api/purchases', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf()}, body: JSON.stringify(value)});
  if (response.ok) {
    $('#dialog').close();
    loadPeriod();
  }
}

function purchaseDialog() {
  const currentRate = state.exchange_rate?.rate || '';
  $('#dialogTitle').textContent = 'Credits购买批次';
  $('#dialogBody').innerHTML = `<label>购买时间<input id="pt" type="datetime-local"></label><label>支付USD<input id="pa" type="number" step="0.01"></label><label>获得Credits<input id="pc" type="number" step="0.01"></label><label>手续费USD<input id="pf" type="number" step="0.01" value="0"></label><label>人民币/USD汇率（已带入ECB最新值）<input id="px" type="number" step="0.0001" value="${esc(currentRate)}"></label><button id="savePurchase">保存</button>`;
  $('#savePurchase').onclick = savePurchase;
  $('#dialog').showModal();
}

async function loadPeriod() {
  let query = `period=${encodeURIComponent($('#period').value)}`;
  const from = $('#from').value;
  const to = $('#to').value;
  if (from) {
    query = `from=${encodeURIComponent(new Date(from).toISOString())}`;
    if (to) query += `&to=${encodeURIComponent(new Date(to).toISOString())}`;
  }
  const response = await fetch(`/api/snapshot?${query}`);
  if (response.ok) render(await response.json());
}

let poll;
function liveRender(event) {
  if ($('#period').value === 'today') render(JSON.parse(event.data));
  else loadPeriod();
}
function connect() {
  const events = new EventSource('/events');
  events.onopen = () => {
    $('#sse').textContent = 'SSE 已连接';
    $('#sse').className = 'pill online';
    clearInterval(poll);
  };
  events.addEventListener('snapshot', liveRender);
  events.addEventListener('update', liveRender);
  events.onerror = () => {
    $('#sse').textContent = 'REST 降级轮询';
    $('#sse').className = 'pill offline';
    events.close();
    poll = setInterval(loadPeriod, 2000);
    setTimeout(connect, 10000);
  };
}

function startDashboard() {
  $('#applyRange').onclick = loadPeriod;
  $('#period').onchange = loadPeriod;
  $('#filter').oninput = renderGroups;
  $('#addWindows').onclick = () => enroll('windows');
  $('#addLinux').onclick = () => enroll('linux');
  $('#addPurchase').onclick = purchaseDialog;
  $('#close').onclick = () => $('#dialog').close();
  $('#sessionGroups').addEventListener('click', event => {
    const button = event.target.closest('[data-detail]');
    if (button) {
      event.stopPropagation();
      detail(button.dataset.detail);
    }
  });
  connect();
}

if (typeof document !== 'undefined') startDashboard();
if (typeof module !== 'undefined' && module.exports) module.exports = {token, groupedSessions, displayProjectName};

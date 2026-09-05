const $ = selector => document.querySelector(selector);
let state = {sessions: [], hosts: [], totals: {}, project_totals: [], exchange_rate: {}};
let renderedStructure = '';
let structurePending = false;
const periodLabels = {today: '今日', '24h': '最近24小时', week: '本周', month: '本月', all: '全部', custom: '所选时段'};

function trimNumber(value, digits) {
  if (!Number.isFinite(Number(value))) return '0';
  return String(Number(Number(value).toFixed(digits)));
}

function token(value) {
  const number = Number(value || 0);
  const absolute = Math.abs(number);
  if (absolute >= 1000000) {
    return `${(number / 1000000).toFixed(4)}M`;
  }
  return String(Math.round(number));
}

function tokenM(value) {
  const number = Number(value || 0);
  return `${(number / 1000000).toFixed(4)}M`;
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
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString('zh-CN', {hour12: false, timeZone: 'Asia/Shanghai'});
}

function card(item) {
  const value = item.parts ? item.parts.map((part, i) => `<span class="value-part"><span class="part-label">${esc(part.label)}</span><span data-card-part="${i}">${esc(part.value)}</span></span>`).join('<span class="value-divider">/</span>') : esc(item.value);
  return `<div class="card" data-card="${esc(item.key)}"><div class="label" data-card-label>${esc(item.label)}</div><div class="value${item.parts ? ' paired-value' : ''}" data-card-value>${value}</div><div class="note" data-card-note>${esc(item.note)}</div></div>`;
}

function selectionTouches(element) {
  if (typeof document === 'undefined' || !element) return false;
  const selection = document.getSelection?.();
  if (!selection || selection.isCollapsed || !selection.rangeCount) return false;
  try {
    return selection.getRangeAt(0).intersectsNode(element);
  } catch (_) {
    return false;
  }
}

function updateText(element, value) {
  if (!element || selectionTouches(element)) return;
  const next = String(value ?? '');
  if (element.textContent !== next) element.textContent = next;
}

function dashboardCards(snapshot) {
  const totals = snapshot.totals || {};
  const fx = snapshot.exchange_rate || {};
  const activeCutoff = Date.now() - 5 * 60 * 1000;
  const pending = (snapshot.sessions || []).some(item => {
    const quality = item.data_quality || item.status;
    const lastEvent = new Date(item.last_event_at || 0).getTime();
    return (quality === 'LOWER_BOUND' || quality === 'ESTIMATED_LIVE') && lastEvent >= activeCutoff;
  });
  const fxState = fx.stale ? '汇率已过期' : '每6小时同步';
  const live = Number(totals.live_estimate || 0);
  const generatingValue = live > 0 ? tokenM(live) : pending ? '日志不可估算' : '0.0000M';
  const generatingNote = live > 0 ? '本地文本增量估算 · 完成后校准' : pending ? '无文本增量；完成后记入精确值' : '当前没有待结算输出';
  const writeVisible = totals.cache_write_visible !== false;
  const writeValue = writeVisible ? tokenM(totals.cache_write_input_tokens) : '不可见';
  const writeNote = writeVisible && Number(totals.cache_write_input_tokens || 0) === 0 ? '日志明确上报写入为0' : writeVisible ? '日志已提供写入字段' : '当前日志未提供写入字段';
  return [
    {key: 'exact', label: `${periodLabels[snapshot.period] || '今日'}精确 Token`, value: tokenM(totals.total_tokens), note: '所选时段内已结算 · EXACT'},
    {key: 'generating', label: '当前生成中临时 Token', value: generatingValue, note: generatingNote},
    {key: 'io', label: '总输入 / 输出', value: `${tokenM(totals.input_tokens)}/${tokenM(totals.output_tokens)}`, parts: [{label: '输入', value: tokenM(totals.input_tokens)}, {label: '输出', value: tokenM(totals.output_tokens)}], note: `推理输出${tokenM(totals.reasoning_output_tokens)}`},
    {key: 'cache', label: '缓存读取 / 写入', value: `${tokenM(totals.cached_input_tokens)}/${writeValue}`, parts: [{label: '读取', value: tokenM(totals.cached_input_tokens)}, {label: '写入', value: writeValue}], note: `命中率${trimNumber(totals.cache_hit_rate || 0, 1)}% · ${writeNote}`},
    {key: 'active', label: '当前活跃会话 / 在线设备', value: `${totals.active_sessions || 0}/${totals.online_hosts || 0}`, note: '近5分钟活跃 / 近15秒在线 · 不随时间筛选'},
    {key: 'openai', label: 'OpenAI API 等价', value: money(totals.api?.value), note: `${cny(totals.api_cny)} · 官方费率每日同步 · ${stamp(totals.api?.verified_at)}`},
    {key: 'vercel', label: 'Vercel 等价', value: money(totals.vercel?.value), note: `${cny(totals.vercel_cny)} · 模型目录每日同步 · ${stamp(totals.vercel?.verified_at)}`},
    {key: 'credits', label: 'Codex Credits 等价', value: credits(totals.credits?.value), note: totals.credits_purchase_usd ? `购买均价${money(totals.credits_purchase_usd)}/${cny(totals.credits_purchase_cny)}` : '未录入购买批次'},
    {key: 'fx', label: '实时 USD/CNY', value: fx.rate ? `1USD=${cny(fx.rate)}` : '等待汇率', note: `ECB ${fx.rate_date || '-'} · ${fxState}`},
    {key: 'cash', label: '实际新增现金支出', value: money(totals.actual_incremental_cash), note: '套餐内会话为0；监控程序不调用模型'}
  ];
}

function renderCards(snapshot) {
  const items = dashboardCards(snapshot);
  const container = $('#cards');
  const currentKeys = [...container.querySelectorAll('[data-card]')].map(element => element.dataset.card).join('|');
  const nextKeys = items.map(item => item.key).join('|');
  if (currentKeys !== nextKeys) {
    container.innerHTML = items.map(card).join('');
    return;
  }
  for (const item of items) {
    const element = [...container.querySelectorAll('[data-card]')].find(node => node.dataset.card === item.key);
    updateText(element?.querySelector('[data-card-label]'), item.label);
    if (item.parts) item.parts.forEach((part, i) => updateText(element?.querySelector(`[data-card-part="${i}"]`), part.value));
    else updateText(element?.querySelector('[data-card-value]'), item.value);
    updateText(element?.querySelector('[data-card-note]'), item.note);
  }
}

function render(snapshot, forceStructure = false) {
  const started = performance.now();
  if (snapshot.realtime_config?.coalesce_ms) refreshCoalesceMS = Math.min(250, Math.max(100, snapshot.realtime_config.coalesce_ms));
  state = snapshot;
  renderCards(snapshot);
  if (forceStructure) renderedStructure = '';
  renderGroups(forceStructure);
  updateText($('#updated'), `最后更新 ${stamp(snapshot.generated_at)}（北京时间）`);
  const start = snapshot.period === 'all' ? '开始监控' : stamp(snapshot.range_start);
  updateText($('#rangeText'), `${start} → ${stamp(snapshot.range_end)}`);
  updateText($('#rangeRule'), rangeRule(snapshot.period));
  updateText($('#dataCoverage'), snapshot.data_start ? `已采集数据始于 ${stamp(snapshot.data_start)}（北京时间）` : '尚无已采集的用量记录');
  if (typeof window !== 'undefined') window.meterDiagnostics = {...window.meterDiagnostics, applied_revision: snapshot.revision, server_epoch: snapshot.server_epoch, applied_at: new Date().toISOString(), apply_ms: performance.now() - started, server_build_ms: snapshot.server_build_ms};
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
    group.cache_write_visible = group.records.every(item => item.cache_write_visible !== false && item.data_quality !== 'CACHE_WRITE_UNKNOWN');
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

function domKey(...parts) {
  return parts.map(part => encodeURIComponent(String(part ?? ''))).join('|');
}

function statusPill(status) {
  const className = status === 'EXACT' ? 'exact' : status === 'ESTIMATED_LIVE' || status === 'LOWER_BOUND' ? 'estimated' : 'stale';
  return `<span class="pill ${className}" data-session-status>${esc(status)}</span>`;
}

function metric(key, label, value, note = '') {
  return `<div class="metric" data-metric="${esc(key)}"><div class="metric-label">${esc(label)}</div><div class="metric-value" data-metric-value>${esc(value)}</div><div class="pricing-note" data-metric-note>${esc(note)}</div></div>`;
}

function recordRow(record) {
  return `<tr data-record-key="${esc(domKey(record.host_id, record.conversation_id))}">
    <td data-record-field="type">${record.parent_conversation_id ? '内部调用' : '主会话'}</td>
    <td data-record-field="id">${esc(shortID(record.conversation_id))}</td>
    <td data-record-field="model">${esc(record.model)}</td>
    <td data-record-field="effort">${esc(record.reasoning_effort)}</td>
    <td data-record-field="total">${token(record.total_tokens)}</td>
    <td data-record-field="input">${token(record.input_tokens)}</td>
    <td data-record-field="output">${token(record.output_tokens)}</td>
    <td data-record-field="time">${ago(record.last_event_at)}</td>
    <td><button type="button" class="detail-button secondary" data-detail="${esc(record.conversation_id)}">详情</button></td>
  </tr>`;
}

function recordRows(records) {
  return records.map(recordRow).join('');
}

function sessionMetricValues(group) {
  const fx = Number(state.exchange_rate?.rate || 0);
  const write = group.cache_write_visible ? tokenM(group.cache_write_input_tokens) : '不可见';
  const writeNote = group.cache_write_visible && Number(group.cache_write_input_tokens || 0) === 0 ? '日志明确上报0' : group.cache_write_visible ? '' : '日志未提供该字段';
  return {
    total: {value: tokenM(group.total_tokens), note: `${group.records.length}条记录`},
    input: {value: tokenM(group.input_tokens), note: ''},
    cache_read: {value: tokenM(group.cached_input_tokens), note: `${trimNumber(group.cache_hit_rate, 1)}%`},
    cache_write: {value: write, note: writeNote},
    output: {value: tokenM(group.output_tokens), note: `推理${tokenM(group.reasoning_output_tokens)}`},
    openai: {value: money(group.api_cost), note: cny(group.api_cost * fx)},
    vercel: {value: money(group.vercel_cost), note: cny(group.vercel_cost * fx)},
    credits: {value: credits(group.credits), note: ''}
  };
}

function sessionCard(group) {
  const values = sessionMetricValues(group);
  const key = domKey(group.host_id, group.root_id);
  return `<article class="session-card" data-session-key="${esc(key)}">
    <div class="session-heading">
      <span class="session-title" data-session-name>${esc(group.name)}</span>
      <span class="session-id">${esc(shortID(group.root_id))}</span>
      ${statusPill(group.status)}
      <span data-session-model>${esc(group.model)} · ${esc(group.reasoning_effort)}</span>
      <span class="session-time" data-session-time>${duration(group.started_at)} · ${ago(group.last_event_at)}</span>
    </div>
    <div class="session-metrics">
      ${metric('total', '总Token', values.total.value, values.total.note)}
      ${metric('input', '输入', values.input.value, values.input.note)}
      ${metric('cache_read', '缓存读取', values.cache_read.value, values.cache_read.note)}
      ${metric('cache_write', '缓存写入', values.cache_write.value, values.cache_write.note)}
      ${metric('output', '输出', values.output.value, values.output.note)}
      ${metric('openai', 'OpenAI', values.openai.value, values.openai.note)}
      ${metric('vercel', 'Vercel', values.vercel.value, values.vercel.note)}
      ${metric('credits', 'Credits', values.credits.value, values.credits.note)}
    </div>
    <details class="child-records" data-node-key="records:${esc(key)}">
      <summary data-record-summary>展开${group.records.length}条原始记录（内部工具调用已聚拢）</summary>
      <div class="table-wrap"><table><thead><tr><th>类型</th><th>ID</th><th>模型</th><th>推理</th><th>总Token</th><th>输入</th><th>输出</th><th>最后事件</th><th>操作</th></tr></thead><tbody></tbody></table></div>
      <div class="record-pagination"><span data-record-count></span><button type="button" class="secondary" data-more-records>再显示100条</button></div>
    </details>
  </article>`;
}

function groupedView() {
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

  const visibleHosts = [...hosts.values()].filter(host => {
    const hostGroups = groups.filter(group => group.host_id === host.host_id);
    return !query || hostGroups.length || JSON.stringify(host).toLowerCase().includes(query);
  });
  return {query, groups, hosts: visibleHosts};
}

function projectStats(hostID, project, projectGroups) {
  const totals = {total_tokens: 0, api_cost: 0, vercel_cost: 0, credits: 0, records: 0};
  for (const group of projectGroups) {
    totals.total_tokens += Number(group.total_tokens || 0);
    totals.api_cost += Number(group.api_cost || 0);
    totals.vercel_cost += Number(group.vercel_cost || 0);
    totals.credits += Number(group.credits || 0);
    totals.records += group.records.length;
  }
  const exactTotals = (state.project_totals || []).find(item => item.host_id === hostID && item.project === project);
  if (exactTotals) {
    totals.total_tokens = Number(exactTotals.total_tokens || 0);
    totals.api_cost = Number(exactTotals.api_cost || 0);
    totals.vercel_cost = Number(exactTotals.vercel_cost || 0);
    totals.credits = Number(exactTotals.credits || 0);
    totals.records = Number(exactTotals.records || 0);
  }
  totals.sessions = exactTotals ? Number(exactTotals.sessions || 0) : projectGroups.length;
  const fx = Number(state.exchange_rate?.rate || 0);
  return {
    token: `Token ${tokenM(totals.total_tokens)}`,
    openai: `OpenAI ${money(totals.api_cost)}/${cny(totals.api_cost * fx)}`,
    vercel: `Vercel ${money(totals.vercel_cost)}/${cny(totals.vercel_cost * fx)}`,
    credits: credits(totals.credits),
    count: `${totals.sessions}个会话 · ${totals.records}条记录`
  };
}

function viewStructure(view) {
  const hosts = view.hosts.map(host => host.host_id).sort();
  const groups = view.groups.map(group => [group.host_id, group.root_id, group.project]);
  groups.sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right)));
  return JSON.stringify({
    query: view.query,
    hosts,
    groups
  });
}

function openDetailsState() {
  const result = new Map();
  document.querySelectorAll('#sessionGroups details[data-node-key]').forEach(element => result.set(element.dataset.nodeKey, element.open));
  return result;
}

function restoreOpenDetails(saved) {
  document.querySelectorAll('#sessionGroups details[data-node-key]').forEach(element => {
    if (saved.has(element.dataset.nodeKey)) element.open = saved.get(element.dataset.nodeKey);
  });
}

function groupsHTML(view) {
  return view.hosts.map(host => {
    const hostGroups = view.groups.filter(group => group.host_id === host.host_id);
    const [icon, kind] = deviceName(host.platform);
    const rawCount = hostGroups.reduce((total, group) => total + group.records.length, 0);
    const projects = new Map();
    for (const group of hostGroups) {
      if (!projects.has(group.project)) projects.set(group.project, []);
      projects.get(group.project).push(group);
    }
    const projectHTML = [...projects.entries()].map(([project, projectGroups]) => {
      const stats = projectStats(host.host_id, project, projectGroups);
      const projectKey = domKey(host.host_id, project);
      return `<details class="project-group" open data-node-key="project:${esc(projectKey)}" data-project-key="${esc(projectKey)}">
        <summary title="这里是归属于该项目的 Codex 会话用量；监控服务本身不调用模型">
          <span class="project-title">📁 ${esc(project)}</span>
          <span class="project-totals"><span data-project-field="token">${esc(stats.token)}</span><span data-project-field="openai">${esc(stats.openai)}</span><span data-project-field="vercel">${esc(stats.vercel)}</span><span data-project-field="credits">${esc(stats.credits)}</span></span>
          <span class="project-count" data-project-field="count">${esc(stats.count)}</span>
        </summary>
        <div class="project-sessions">${projectGroups.map(sessionCard).join('')}</div>
      </details>`;
    }).join('');
    const empty = projectHTML || '<div class="empty-device">此时间范围暂无会话，但设备仍在正常上报心跳。</div>';
    const hostKey = domKey(host.host_id);
    return `<details class="device-group" open data-node-key="device:${esc(hostKey)}" data-device-key="${esc(hostKey)}">
      <summary><span class="device-icon">${icon}</span><span class="device-name">${esc(host.alias)}</span><span class="device-kind">${kind}</span><span class="pill ${host.online ? 'online' : 'offline'}" data-device-online>${host.online ? '在线' : '离线'}</span><span class="device-count" data-device-count>${hostGroups.length}个聚合会话 · ${rawCount}条原始记录</span></summary>
      <div class="project-list">${empty}</div>
    </details>`;
  }).join('') || '<div class="empty-device">没有符合条件的会话。</div>';
}

function updateStatus(element, status) {
  if (!element) return;
  const className = status === 'EXACT' ? 'exact' : status === 'ESTIMATED_LIVE' || status === 'LOWER_BOUND' ? 'estimated' : 'stale';
  element.className = `pill ${className}`;
  updateText(element, status);
}

function updateRecordRow(row, record) {
  const values = {
    type: record.parent_conversation_id ? '内部调用' : '主会话',
    id: shortID(record.conversation_id),
    model: record.model || '',
    effort: record.reasoning_effort || '',
    total: token(record.total_tokens),
    input: token(record.input_tokens),
    output: token(record.output_tokens),
    time: ago(record.last_event_at)
  };
  for (const [field, value] of Object.entries(values)) updateText(row.querySelector(`[data-record-field="${field}"]`), value);
}

function patchRecordRows(article, group) {
  const details = article.querySelector('.child-records');
  if (!details?.open) return;
  const limit = Number(details.dataset.recordLimit || 100);
  const records = group.records.slice(0, limit);
  updateText(details.querySelector('[data-record-count]'), `已显示${records.length}/${group.records.length}条`);
  details.querySelector('[data-more-records]').hidden = records.length >= group.records.length;
  const body = article.querySelector('tbody');
  if (!body) return;
  const existing = new Map([...body.querySelectorAll('[data-record-key]')].map(row => [row.dataset.recordKey, row]));
  const desired = new Set(records.map(record => domKey(record.host_id, record.conversation_id)));
  for (const [key, row] of existing) {
    if (!desired.has(key) && !selectionTouches(row)) {
      row.remove();
      existing.delete(key);
    }
  }
  for (const record of [...records].reverse()) {
    const key = domKey(record.host_id, record.conversation_id);
    if (!existing.has(key)) {
      body.insertAdjacentHTML('afterbegin', recordRow(record));
      existing.set(key, body.firstElementChild);
    }
  }
  for (const record of records) updateRecordRow(existing.get(domKey(record.host_id, record.conversation_id)), record);
}

function patchGroups(view) {
  const nodes = (selector, key) => new Map([...document.querySelectorAll(selector)].map(node => [node.dataset[key], node]));
  const devices = nodes('[data-device-key]', 'deviceKey');
  const projectNodes = nodes('[data-project-key]', 'projectKey');
  const sessionNodes = nodes('[data-session-key]', 'sessionKey');
  for (const host of view.hosts) {
    const hostGroups = view.groups.filter(group => group.host_id === host.host_id);
    const element = devices.get(domKey(host.host_id));
    if (!element) continue;
    const online = element.querySelector('[data-device-online]');
    if (online) online.className = `pill ${host.online ? 'online' : 'offline'}`;
    updateText(online, host.online ? '在线' : '离线');
    updateText(element.querySelector('[data-device-count]'), `${hostGroups.length}个聚合会话 · ${hostGroups.reduce((total, group) => total + group.records.length, 0)}条原始记录`);
  }
  const projects = new Map();
  for (const group of view.groups) {
    const key = domKey(group.host_id, group.project);
    if (!projects.has(key)) projects.set(key, []);
    projects.get(key).push(group);
  }
  for (const [key, projectGroups] of projects) {
    const element = projectNodes.get(key);
    if (!element) continue;
    const stats = projectStats(projectGroups[0].host_id, projectGroups[0].project, projectGroups);
    for (const [field, value] of Object.entries(stats)) updateText(element.querySelector(`[data-project-field="${field}"]`), value);
  }
  for (const group of view.groups) {
    const article = sessionNodes.get(domKey(group.host_id, group.root_id));
    if (!article) continue;
    updateText(article.querySelector('[data-session-name]'), group.name);
    updateStatus(article.querySelector('[data-session-status]'), group.status);
    updateText(article.querySelector('[data-session-model]'), `${group.model} · ${group.reasoning_effort}`);
    updateText(article.querySelector('[data-session-time]'), `${duration(group.started_at)} · ${ago(group.last_event_at)}`);
    const values = sessionMetricValues(group);
    for (const [key, item] of Object.entries(values)) {
      const metricElement = [...article.querySelectorAll('[data-metric]')].find(node => node.dataset.metric === key);
      updateText(metricElement?.querySelector('[data-metric-value]'), item.value);
      updateText(metricElement?.querySelector('[data-metric-note]'), item.note);
    }
    updateText(article.querySelector('[data-record-summary]'), `展开${group.records.length}条原始记录（内部工具调用已聚拢）`);
    patchRecordRows(article, group);
  }
}

function renderGroups(force = false) {
  const view = groupedView();
  const nextStructure = viewStructure(view);
  const container = $('#sessionGroups');
  const changed = force || nextStructure !== renderedStructure;
  if (changed && container.children.length && selectionTouches(container)) {
    structurePending = true;
    patchGroups(view);
    return;
  }
  if (changed) {
    const open = openDetailsState();
    container.innerHTML = groupsHTML(view);
    restoreOpenDetails(open);
    renderedStructure = nextStructure;
    structurePending = false;
    return;
  }
  patchGroups(view);
}

function csrf() {
  return decodeURIComponent((document.cookie.match(/(?:^|; )meter_csrf=([^;]+)/) || [])[1] || '');
}

async function renameSession(id) {
  const name = $('#rename').value;
  const response = await fetch(`/api/sessions/${encodeURIComponent(id)}`, {method: 'PATCH', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf()}, body: JSON.stringify({name})});
  if (response.ok) {
    $('#dialog').close();
    loadPeriod(true);
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

function rangeRule(period) {
  return ({today: '今天：北京时间00:00起，统计到现在', '24h': '最近24小时：从现在向前滚动24小时', week: '本周：北京时间周一00:00起', month: '本月：北京时间1日00:00起', all: '全部：开始监控以来的已采集用量', custom: '自定义：含开始时间，不含结束时间；均为北京时间'})[period] || '';
}

function rangeQuery(period, from = '', to = '') {
  const query = new URLSearchParams({period});
  if (period === 'custom') {
    // datetime-local has no zone. Interpret it explicitly as Beijing time,
    // independent of the phone, browser, or VPS timezone.
    const start = new Date(`${from}+08:00`);
    const end = to ? new Date(`${to}+08:00`) : new Date();
    if (!from || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) throw new Error('请选择有效的开始和结束时间');
    if (start >= end) throw new Error('结束时间必须晚于开始时间');
    query.set('from', start.toISOString());
    if (to) query.set('to', end.toISOString());
  }
  return query.toString();
}

// At most one refresh is in flight. Manual changes cancel it, and the sequence
// check rejects a late response even when the network ignores cancellation.
function createRangeLoader(fetcher, onData, onStatus) {
  let sequence = 0;
  let current;
  let pending;
  const load = async (query, replace = false) => {
    if (current) {
      pending = {query, replace};
      if (replace) { sequence++; current.abort(); }
      return;
    }
    const controller = new AbortController();
    current = controller;
    const ticket = ++sequence;
    const started = performance.now();
    const timeout = setTimeout(() => controller.abort(), 15000);
    onStatus('loading', replace);
    try {
      const response = await fetcher(`/api/snapshot?${query}`, {signal: controller.signal, cache: 'no-store'});
      if (response.status === 401) throw new Error('登录已过期，请重新登录');
      if (!response.ok) throw new Error(response.status === 400 ? await response.text() : '数据暂时加载失败，请重试');
      const data = await response.json();
      if (ticket !== sequence || controller.signal.aborted) return;
      if (data.error) throw new Error('数据暂时不可用，请重试');
      onData(data, {request_ms: performance.now() - started});
      onStatus('ready', replace);
    } catch (error) {
      if (ticket === sequence) onStatus('error', replace, error.name === 'AbortError' ? '加载超时，请点击重试' : error.message);
    } finally {
      clearTimeout(timeout);
      if (current === controller) current = null;
      if (pending) {
        const next = pending;
        pending = undefined;
        // Consume the dirty flag immediately, without another debounce. A
        // notification never aborts the slow request already in progress.
        void load(next.query, next.replace);
      }
    }
  };
  load.cancel = () => { sequence++; pending = undefined; current?.abort(); };
  load.busy = () => !!current;
  return load;
}

function debounce(action, delay) {
  let timer;
  const run = () => { clearTimeout(timer); timer = setTimeout(action, delay); };
  run.cancel = () => clearTimeout(timer);
  return run;
}

let appliedQuery = 'period=today';
let refreshTimer;
let rangeLoader;
let eventStream;
let rangeDraft = false;
let refreshCoalesceMS = 200;
const autoApplyRange = debounce(() => applyRange(), 300);
function loadPeriod(manual = false) {
  if (rangeDraft) return;
  if (manual) clearTimeout(refreshTimer);
  if (manual) refreshTimer = undefined;
  return rangeLoader(appliedQuery, manual === true);
}
function scheduleRefresh() {
  if (refreshTimer || document.hidden) return;
  refreshTimer = setTimeout(() => { refreshTimer = undefined; loadPeriod(); }, refreshCoalesceMS);
}
function choosePeriod() {
  autoApplyRange.cancel();
  rangeDraft = false;
  const period = $('#period').value;
  $('#customRange').hidden = period !== 'custom';
  if (period === 'custom') {
    if (!$('#from').value) {
      const local = new Date(Date.now() + 8 * 3600000).toISOString().slice(0, 16);
      $('#from').value = `${local.slice(0, 10)}T00:00`;
      $('#to').value = '';
    }
    applyRange();
    return;
  }
  appliedQuery = rangeQuery(period);
  loadPeriod(true);
}
function applyRange() {
  autoApplyRange.cancel();
  try {
    appliedQuery = rangeQuery($('#period').value, $('#from').value, $('#to').value);
    rangeDraft = false;
    loadPeriod(true);
  } catch (error) {
    rangeDraft = true;
    rangeLoader.cancel();
    $('#loadStatus').textContent = `${error.message}；下方保留上次有效结果`;
    $('#loadStatus').dataset.state = 'error';
    $('#cards').setAttribute('aria-busy', 'false');
    $('#retryRange').hidden = true;
  }
}
function timeInputChanged() {
  rangeDraft = true;
  rangeLoader.cancel();
  $('#loadStatus').textContent = '正在按所选时间筛选…';
  $('#loadStatus').dataset.state = 'loading';
  $('#cards').setAttribute('aria-busy', 'true');
  autoApplyRange();
}
function connect() {
  eventStream?.close();
  const events = new EventSource('/events?notify=1');
  eventStream = events;
  events.onopen = () => {
    $('#sse').textContent = '实时已连接';
    $('#sse').className = 'pill online';
  };
  events.addEventListener('ready', scheduleRefresh);
  events.addEventListener('changed', scheduleRefresh);
  events.onerror = () => {
    $('#sse').textContent = '重连中';
    $('#sse').className = 'pill offline';
    // EventSource reconnects itself. One slow fallback timer below covers both
    // disconnected streams and changes to online/active status without events.
  };
}

function startDashboard() {
  rangeLoader = createRangeLoader(fetch, (data,timing) => {
    window.meterDiagnostics = {...window.meterDiagnostics, snapshot_request_ms: timing.request_ms};
    render(data);
  }, (status, manual, message) => {
    if (status === 'loading' && !manual && state.generated_at) return;
    $('#loadStatus').dataset.state = status;
    $('#loadStatus').textContent = status === 'loading' ? '正在加载所选范围…' : status === 'error' ? message : `已筛选：${periodLabels[state.period] || '今天'}`;
    $('#retryRange').hidden = status !== 'error';
    $('#cards').setAttribute('aria-busy', String(status === 'loading'));
  });
  $('#applyRange').onclick = applyRange;
  $('#period').onchange = choosePeriod;
  for (const id of ['#from', '#to']) {
    $(id).oninput = timeInputChanged;
    $(id).onchange = timeInputChanged;
  }
  $('#retryRange').onclick = () => loadPeriod(true);
  $('#filter').oninput = () => renderGroups(true);
  $('#addWindows').onclick = () => enroll('windows');
  $('#addLinux').onclick = () => enroll('linux');
  $('#addPurchase').onclick = purchaseDialog;
  $('#close').onclick = () => $('#dialog').close();
  $('#sessionGroups').addEventListener('click', event => {
    const selection = document.getSelection?.();
    if (event.target.closest('summary') && selection && !selection.isCollapsed) {
      event.preventDefault();
      return;
    }
    const button = event.target.closest('[data-detail]');
    if (button) {
      event.stopPropagation();
      detail(button.dataset.detail);
    }
    const more = event.target.closest('[data-more-records]');
    if (more) {
      const details = more.closest('.child-records');
      details.dataset.recordLimit = Number(details.dataset.recordLimit || 100) + 100;
      const article = details.closest('[data-session-key]');
      const group = groupedSessions(state.sessions || []).find(group => domKey(group.host_id, group.root_id) === article.dataset.sessionKey);
      if (group) patchRecordRows(article, group);
    }
  });
  $('#sessionGroups').addEventListener('toggle', event => {
    if (!event.target.matches('.child-records') || !event.target.open) return;
    const article = event.target.closest('[data-session-key]');
    const group = groupedSessions(state.sessions || []).find(group => domKey(group.host_id, group.root_id) === article.dataset.sessionKey);
    if (group) patchRecordRows(article, group);
  }, true);
  document.addEventListener('selectionchange', () => {
    const selection = document.getSelection?.();
    if (structurePending && (!selection || selection.isCollapsed)) renderGroups(true);
  });
  loadPeriod(true);
  connect();
  window.addEventListener('pagehide', () => eventStream?.close());
  window.addEventListener('pageshow', event => { if (event.persisted) { connect(); loadPeriod(true); } });
  setInterval(() => { if (!document.hidden) loadPeriod(); }, 15000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) loadPeriod(); });
}

if (typeof document !== 'undefined') startDashboard();
if (typeof module !== 'undefined' && module.exports) module.exports = {token, tokenM, groupedSessions, displayProjectName, dashboardCards, sessionMetricValues, viewStructure, updateText, rangeQuery, rangeRule, createRangeLoader, debounce};

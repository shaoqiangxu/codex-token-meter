(function (root) {
  'use strict';
  const defaults = {coalesce_ms: 200, heartbeat_ms: 5000, delayed_ms: 12000, offline_ms: 30000, probe_ms: 30000};
  function connectionClock(now = () => performance.now()) {
    const created = now();
    let config = {...defaults}, opened = false, generation = 0, heartbeatGeneration = -1;
    let heartbeatAt = null, receivedAt = null, appliedAt = null, expectedEpoch = '', expectedRevision = 0;
    let appliedEpoch = '', appliedRevision = -1, failed = false;
    return {
      configure(value) { config = {...config, ...value}; },
      open() { opened = true; failed = false; generation++; },
      close() { opened = false; },
      fail() { opened = false; failed = true; },
      heartbeat(value) {
        if (!value || typeof value.server_epoch !== 'string' || !value.server_epoch || !Number.isSafeInteger(value.revision)) return false;
        if (expectedEpoch !== value.server_epoch) expectedRevision = 0;
        expectedEpoch = value.server_epoch;
        expectedRevision = Math.max(expectedRevision, value.revision);
        heartbeatAt = receivedAt = now(); heartbeatGeneration = generation;
        return true;
      },
      applied(value) { appliedEpoch = value.server_epoch; appliedRevision = value.data_revision ?? value.revision; appliedAt = now(); receivedAt = now(); },
      received() { receivedAt = now(); },
      get expectedEpoch() { return expectedEpoch; },
      get expectedRevision() { return expectedRevision; },
      status() {
        const age = now() - (heartbeatAt ?? created);
        let connection = age >= config.offline_ms ? 'offline' : age >= config.delayed_ms ? 'delayed' : !opened || heartbeatGeneration !== generation ? 'verifying' : 'online';
        const synced = connection === 'online' && appliedEpoch === expectedEpoch && appliedRevision >= expectedRevision;
        return {connection, synced, heartbeat_age_ms: heartbeatAt === null ? null : age, last_received_age_ms: receivedAt === null ? null : now() - receivedAt, applied_age_ms: appliedAt === null ? null : now() - appliedAt, failed};
      }
    };
  }

  function acceptsSnapshot(current, next, expectedEpoch = '') {
    if (!next || !next.server_epoch || !Number.isSafeInteger(next.revision) || !next.query_key) return false;
    if (expectedEpoch && expectedEpoch !== next.server_epoch) return false;
    if (current.server_epoch === next.server_epoch && current.query_key === next.query_key) {
      if (next.revision < current.revision) return false;
      if (next.revision === current.revision && Date.parse(next.range_end) < Date.parse(current.range_end)) return false;
    }
    return true;
  }

  function applyNumbers(current, message) {
    if (!message || message.server_epoch !== current.server_epoch || message.query_key !== current.query_key) return {resync: true};
    if (!Number.isSafeInteger(message.revision)) return {resync: true};
    if (message.revision < current.revision || (message.revision === current.revision && Date.parse(message.range_end) <= Date.parse(current.range_end))) return {ignored: true};
    if (message.base_revision !== current.revision || message.base_range_end !== current.range_end) return {resync: true};
    if (!Array.isArray(message.sessions) || !Array.isArray(message.removed)) return {resync: true};
    const key = row => `${row.host_id}\u0000${row.conversation_id}`;
    const rows = new Map((current.sessions || []).map(row => [key(row), row]));
    for (const id of message.removed) rows.delete(id);
    for (const row of message.sessions) rows.set(key(row), row);
    return {value: {...current, ...message, sessions: [...rows.values()]}};
  }

  function runtimeStatus(snapshot) {
    const rows = snapshot.runtime || [];
    const hosts = new Map((snapshot.hosts || []).map(host => [host.host_id, host]));
    let live = 0, running = 0, idle = 0, unknown = 0;
    for (const row of rows) {
      const host = hosts.get(row.host_id);
      const fresh = host?.connection_state === 'online' && row.evidence_age_ms < 300000;
      if (row.runtime_state === 'running' && fresh) { running++; live += Number(row.live_estimate || 0); }
      else if (row.runtime_state === 'idle') idle++;
      else unknown++;
    }
    for (const host of hosts.values()) if (!host.telemetry || !rows.some(row => row.host_id === host.host_id) || host.connection_state !== 'online') unknown++;
    if (live > 0) return {state: 'ESTIMATED_LIVE', live, note: '仅可见输出的本地估算，不含隐藏推理；未入精确账本'};
    if (running) return {state: 'RUNNING', live: null, note: '有运行证据，无可见delta；当前不可估算'};
    if (idle && !unknown) return {state: 'IDLE', live: null, note: '已收到明确完成或空闲证据'};
    return {state: 'UNKNOWN', live: null, note: '运行证据不足；EXACT仅代表用量精确，不代表空闲'};
  }

  // Animate only a highlight. The authoritative number is never interpolated.
  function highlight(element, {now = () => performance.now(), raf = requestAnimationFrame, cancel = cancelAnimationFrame, reduced = false, duration = 450} = {}) {
    if (!element) return () => {};
    let frame, stopped = false;
    const start = now();
    const clear = () => { stopped = true; if (frame !== undefined) cancel(frame); element.style.removeProperty('--confirmed-glow'); element.classList.remove('confirmed-change'); };
    if (reduced) return clear;
    element.classList.add('confirmed-change');
    const step = timestamp => {
      if (stopped) return;
      const progress = Math.max(0, Math.min(1, (timestamp - start) / duration));
      if (progress >= 1) { clear(); return; }
      element.style.setProperty('--confirmed-glow', String(1 - progress));
      frame = raf(step);
    };
    frame = raf(step);
    return clear;
  }
  const api = {defaults, connectionClock, acceptsSnapshot, applyNumbers, runtimeStatus, highlight};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.MeterRealtime = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);

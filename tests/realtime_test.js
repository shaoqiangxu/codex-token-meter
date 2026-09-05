const assert = require('node:assert/strict');
const {connectionClock, acceptsSnapshot, applyNumbers, runtimeStatus, highlight} = require('../web/realtime.js');
const {token, dashboardCards} = require('../web/app.js');
assert.match(dashboardCards({last_ledger_at:'0001-01-01T00:00:00Z'})[0].note, /未验证/, 'zero timestamps mean unverified, not year 1');
for (let n = 1; n <= 99; n++) assert.equal(token(1000000 + n), (1000000 + n).toLocaleString('en-US'));

let time = 0;
const clock = connectionClock(() => time);
clock.open();
assert.equal(clock.status().synced, false, 'onopen is not proof of synchronization');
assert.equal(clock.heartbeat({server_epoch: 'one', revision: 1}), true);
assert.equal(clock.status().synced, false, 'a heartbeat is not a snapshot');
clock.applied({server_epoch: 'one', revision: 1});
assert.equal(clock.status().synced, true);
for (let i = 0; i < 8; i++) { time += 5000; clock.heartbeat({server_epoch: 'one', revision: 1}); }
assert.equal(clock.status().synced, true, 'no new usage does not mean offline');
time += 12001; assert.equal(clock.status().connection, 'delayed');
time += 18000; assert.equal(clock.status().connection, 'offline');
clock.open(); assert.equal(clock.status().synced, false);
clock.heartbeat({server_epoch: 'two', revision: 0});
assert.equal(clock.status().synced, false, 'a restart requires a new authoritative snapshot');
clock.applied({server_epoch: 'two', revision: 0});
assert.equal(clock.status().synced, true);

const base = {server_epoch: 'one', revision: 1, query_key: 'today|start', range_end: '2026-09-05T10:00:00Z', totals: {total_tokens: 1000000, api: {value: 2}}, sessions: [{host_id: 'h', conversation_id: 'a', total_tokens: 1000000}]};
const message = {server_epoch: 'one', revision: 2, base_revision: 1, base_range_end: base.range_end, query_key: base.query_key, range_end: '2026-09-05T10:00:01Z', totals: {total_tokens: 1000001, api: {value: 2.000004}}, sessions: [{host_id: 'h', conversation_id: 'a', total_tokens: 1000001}], removed: []};
const result = applyNumbers(base, message).value;
assert.equal(result.totals.total_tokens, 1000001);
assert.equal(result.sessions[0].total_tokens, 1000001);
assert.equal(base.totals.total_tokens, 1000000, 'input snapshot was mutated');
assert.equal(applyNumbers(result, message).ignored, true, 'duplicate messages must not accumulate');
assert.equal(applyNumbers(base, {...message, base_revision: 0}).resync, true);
assert.equal(applyNumbers(base, {...message, server_epoch: 'two'}).resync, true);
assert.equal(applyNumbers(base, {...message, query_key: 'custom|start|end'}).resync, true, 'today must not leak into a historical selection');
assert.equal(acceptsSnapshot(result, base), false, 'out-of-order snapshots must not roll back a newer value');
assert.equal(acceptsSnapshot(base, message, 'two'), false);
const correction = {...message, revision: 3, base_revision: 2, base_range_end: result.range_end, range_end: '2026-09-05T10:00:02Z', totals: {total_tokens: 5, api: {value: .1}}, sessions: [{host_id: 'h', conversation_id: 'a', total_tokens: 5}]};
assert.equal(applyNumbers(result, correction).value.totals.total_tokens, 5, 'legitimate corrections can decrease');

const host = {host_id: 'h', telemetry: {}, connection_state: 'online'};
const runtime = {host_id: 'h', conversation_id: 'a', runtime_state: 'running', live_estimate: 0, evidence_age_ms: 0};
assert.equal(runtimeStatus({hosts: [host], runtime: [runtime]}).state, 'RUNNING');
assert.equal(runtimeStatus({hosts: [host], runtime: [{...runtime, live_estimate: 37}]}).live, 37);
assert.equal(runtimeStatus({hosts: [host], runtime: [{...runtime, runtime_state: 'idle'}]}).state, 'IDLE');
assert.equal(runtimeStatus({totals: {live_estimate: 0}, sessions: [{status: 'EXACT'}]}).state, 'UNKNOWN');
assert.equal(runtimeStatus({hosts: [{...host, connection_state: 'offline'}], runtime: [runtime]}).state, 'UNKNOWN');

let callback, frames = 0, cancelled = 0;
const styles = new Map(), classes = new Set();
const element = {textContent: '1,000,001', style: {setProperty: (k, v) => styles.set(k, Number(v)), removeProperty: k => styles.delete(k)}, classList: {add: k => classes.add(k), remove: k => classes.delete(k)}};
const options = {now: () => 0, raf: fn => { callback = fn; return ++frames; }, cancel: () => cancelled++};
highlight(element, options);
for (const tick of [20, 250, 440]) { callback(tick); assert.ok(styles.get('--confirmed-glow') >= 0 && styles.get('--confirmed-glow') <= 1); assert.equal(element.textContent, '1,000,001'); }
callback(450);
assert.equal(styles.size, 0); assert.equal(classes.size, 0);
const stop = highlight(element, options); stop(); callback(40); assert.equal(styles.size, 0, 'backgrounded animation must stay cancelled');
const beforeFrames = frames; highlight(element, {...options, reduced: true}); assert.equal(frames, beforeFrames);
assert.equal(result.totals.api.value, 2.000004, 'visual effects must not change authoritative costs');
console.log('Small integers, absolute frames, gaps, restart, heartbeat freshness, runtime evidence and reduced-motion highlights: passed');

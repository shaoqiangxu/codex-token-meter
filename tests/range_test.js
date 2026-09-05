const assert = require('node:assert/strict');
const {rangeQuery, rangeRule, createRangeLoader, dashboardCards, debounce} = require('../web/app.js');

async function main() {
  const preset = rangeQuery('24h', '2026-09-01T00:00', '2026-09-02T00:00');
  assert.equal(preset, 'period=24h', 'presets must discard stale custom dates');
  const custom = new URLSearchParams(rangeQuery('custom', '2026-09-05T00:00', '2026-09-06T00:00'));
  assert.equal(custom.get('from'), '2026-09-04T16:00:00.000Z');
  assert.equal(custom.get('to'), '2026-09-05T16:00:00.000Z');
  assert.throws(() => rangeQuery('custom', '', ''));
  assert.throws(() => rangeQuery('custom', '2026-09-06T00:00', '2026-09-05T00:00'));
  assert.match(rangeRule('today'), /北京时间00:00/);
  assert.equal(dashboardCards({period: '24h'})[0].label, '最近24小时精确 Token');

  const requests = [], rendered = [], statuses = [];
  const loader = createRangeLoader((url, options) => new Promise(resolve => requests.push({url, signal: options.signal, resolve})), value => rendered.push(value), status => statuses.push(status));
  const response = period => ({ok: true, json: async () => ({period})});
  const old = loader('period=today', true);
  await loader('period=today');
  await loader('period=today');
  assert.equal(requests.length, 1, 'live notifications must coalesce while a request is in flight');
  const latest = loader(custom.toString(), true);
  assert.equal(requests[0].signal.aborted, true);
  requests[1].resolve(response('custom'));
  await latest;
  requests[0].resolve(response('today')); // Simulate cancellation ignored by transport.
  await old;
  assert.deepEqual(rendered, [{period: 'custom'}], 'late responses must not replace the selected range');
  const failure = loader('period=all', true);
  requests[2].resolve({ok: false, status: 503});
  await failure;
  assert.equal(statuses.at(-1), 'error');
  const retry = loader('period=all', true);
  requests[3].resolve(response('all'));
  await retry;
  assert.equal(rendered.at(-1).period, 'all', 'failed loads must be retryable');
  const draft = loader('period=today', true);
  loader.cancel();
  assert.ok(requests[4].signal.aborted, 'editing a date must cancel the old range immediately');
  requests[4].resolve(response('today'));
  await draft;
  assert.equal(rendered.at(-1).period, 'all', 'an old request must not overwrite an in-progress date edit');
  let changes = 0;
  const autoApply = debounce(() => changes++, 5);
  autoApply(); autoApply(); autoApply();
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(changes, 1, 'input and change events should produce one automatic filter');
  autoApply(); autoApply.cancel();
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(changes, 1, 'switching presets must cancel pending date edits');
  console.log('Range, cancellation, coalescing and pricing labels: passed');
}
main().catch(error => { console.error(error); process.exitCode = 1; });

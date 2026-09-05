// Isolated synthetic integration only. This script refuses production ports.
const fs = require('node:fs'), crypto = require('node:crypto'), assert = require('node:assert/strict');
const {chromium, webkit} = require(process.env.METER_PLAYWRIGHT || 'playwright');
const origin = 'http://127.0.0.1:18787';
const config = JSON.parse(fs.readFileSync(process.env.METER_BROWSER_CONFIG, 'utf8'));
assert.equal(config.public_url, origin);
assert.equal(config.admin_user, 'test', 'refuse to inject into a non-test deployment');
const payload = `${config.admin_user}|${Math.floor(Date.now()/1000)+3600}`;
const cookie = Buffer.from(payload).toString('base64url') + '.' + crypto.createHmac('sha256', config.session_secret).update(payload).digest('base64url');
const auth = {Cookie: `meter_session=${cookie}`};
const root = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
let input, sequence = 0, healthSequence = 0;
const healthEpoch = `synthetic-browser-${Date.now()}`;
const timings = {numeric_delivery_ms: [], upload_request_ms: [], ingest_server_timing: [], browser_apply_ms: [], server_build_ms: [], errors: []};
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function ingest(events = []) {
  const now = new Date().toISOString();
  const telemetry = {agent_epoch: healthEpoch, report_seq: ++healthSequence, agent_version: 'synthetic', last_scan_at: now, last_usage_at: now, last_upload_at: now, pending_events: 0, scan_ms: 2, upload_ms: 5, scan_age_ms: 0};
  const response = await fetch(origin + '/api/ingest', {method: 'POST', headers: {'Content-Type': 'application/json', Authorization: 'Bearer synthetic'}, body: JSON.stringify({host_id: 'h', events, telemetry})});
  assert.equal(response.status, 200, 'synthetic ingest failed');
  return response.headers.get('server-timing');
}
async function snapshot() {
  const response = await fetch(origin + '/api/snapshot?period=today', {headers: auth});
  assert.equal(response.status, 200);
  return response.json();
}
async function usage(increase) {
  input += increase;
  const event = {event_id: `browser-${Date.now()}-${++sequence}`, host_id: 'h', source_file_id: root, conversation_id: root, event_type: 'exact_usage', timestamp: new Date().toISOString(), model: 'gpt-5.6-sol', repo_name: 'Realtime Test', data_quality: 'EXACT', counts: {input_tokens: input, cached_input_tokens: 100, cache_write_input_tokens: 0, cache_write_visible: true, output_tokens: 20, total_tokens: input + 20}};
  const began=performance.now();
  const serverTiming=await ingest([event]);
  timings.upload_request_ms.push(Math.round(performance.now()-began));
  timings.ingest_server_timing.push(serverTiming);
  return event;
}
async function waitTotal(page, total) {
  await page.waitForFunction(total => state.totals.total_tokens === total && document.querySelector('[data-card="exact"] [data-card-value]').textContent === total.toLocaleString('en-US'), total, {timeout: 20000});
}
async function fits(page) {
  const dimensions = await page.evaluate(() => ({viewport: innerWidth, document: document.documentElement.scrollWidth}));
  assert.ok(dimensions.document <= dimensions.viewport + 1, JSON.stringify(dimensions));
}
async function newPage(engine, width, reduced = false) {
  const browser = await engine.launch({headless: true});
  const context = await browser.newContext({viewport: {width, height: 900}, isMobile: width < 700, hasTouch: width < 700, locale: 'zh-CN', timezoneId: 'America/Los_Angeles', reducedMotion: reduced ? 'reduce' : 'no-preference'});
  await context.addCookies([{name: 'meter_session', value: cookie, url: origin, httpOnly: true}]);
  const page = await context.newPage();
  await page.addInitScript(() => {
    window.streamProbe=[];
    const Native=EventSource;
    window.EventSource=class extends Native {
      addEventListener(type, listener, ...options) {
        super.addEventListener(type,event=>{
          if(type==='numbers'||type==='resync'){
            const value=JSON.parse(event.data);
            window.streamProbe.push({type,revision:value.revision,base:value.base_revision,current:state.revision,base_end:value.base_range_end,current_end:state.range_end});
          }
          return listener.call(this,event);
        },...options);
      }
    };
  });
  page.on('pageerror', err => timings.errors.push(err.message));
  await page.goto(origin);
  await page.waitForFunction(() => document.querySelector('#sse').textContent === '已同步' && document.querySelector('#loadStatus').dataset.state === 'ready', {timeout: 20000});
  return {browser, context, page};
}
async function desktop() {
  const {browser, context, page} = await newPage(chromium, 1440);
  try {
    await fits(page);
    let requests = 0;
    page.on('request', r => { if (r.url().includes('/api/snapshot?')) requests++; });
    let expected = await page.evaluate(() => state.totals.total_tokens);
    const beforeRequests = requests;
    for (const increase of [1,23,99]) {
      const start = performance.now();
      const event = await usage(increase); expected += increase;
      await waitTotal(page, expected);
      timings.numeric_delivery_ms.push(Math.round(performance.now() - start));
      const diag = await page.evaluate(() => window.meterDiagnostics);
      timings.browser_apply_ms.push(diag.apply_ms); timings.server_build_ms.push(diag.server_build_ms);
      await ingest([event]);
      assert.equal(await page.evaluate(() => state.totals.total_tokens), expected, 'duplicate event added tokens');
    }
    timings.steady_snapshot_gets = requests - beforeRequests;
    if(timings.steady_snapshot_gets) console.log(JSON.stringify({stream_probe:await page.evaluate(()=>window.streamProbe)}));
    assert.ok(timings.steady_snapshot_gets <= 1, 'only an initial base race may need one resync; updates must not GET history for every change');
    const authoritative = await snapshot();
    const visible = await page.evaluate(() => state.totals);
    for (const field of ['total_tokens','input_tokens','output_tokens']) assert.equal(visible[field], authoritative.totals[field]);
    assert.equal(visible.api.value, authoritative.totals.api.value, 'price must remain authoritative');

    let intercepted = 0, firstFinished = 0, trailingStarted = 0;
    await page.route('**/api/snapshot?**', async route => {
      intercepted++;
      if (intercepted === 1) { await delay(1000); firstFinished = performance.now(); }
      else if (intercepted === 2) trailingStarted = performance.now();
      await route.continue();
    });
    const slow = page.evaluate(() => loadPeriod(true));
    await page.waitForTimeout(100);
    await page.evaluate(() => { scheduleRefresh(); scheduleRefresh(); });
    await slow;
    await page.waitForFunction(() => !rangeLoader.busy());
    assert.equal(intercepted, 2);
    timings.trailing_request_after_release_ms = Math.round(trailingStarted - firstFinished);
    assert.ok(timings.trailing_request_after_release_ms < 1000, 'trailing refresh did not run promptly');
    await page.unroute('**/api/snapshot?**');

    const idleRequests = requests;
    await page.waitForTimeout(5500);
    assert.equal(requests, idleRequests, 'heartbeat triggered full history aggregation/GET');
    assert.match(await page.locator('#syncSummary').innerText(), /连接正常，等待下一次用量上报/);

    const beforeOffline = await page.evaluate(() => ({total: state.totals.total_tokens, cost: state.totals.api.value}));
    await context.setOffline(true);
    await usage(51); expected += 51;
    await delay(31000);
    assert.deepEqual(await page.evaluate(() => ({total: state.totals.total_tokens, cost: state.totals.api.value})), beforeOffline, 'offline counters or prices grew');
    assert.match(await page.locator('#sse').innerText(), /失联/);
    await context.setOffline(false);
    await waitTotal(page, expected);
    await page.waitForFunction(() => document.querySelector('#sse').textContent === '已同步');
    timings.offline_30s_recovery = 'passed';

    await page.selectOption('#period', 'custom');
    await page.fill('#from', '2026-01-01T00:00'); await page.fill('#to', '2026-01-02T00:00');
    await page.waitForFunction(() => state.period === 'custom' && state.totals.total_tokens === 0);
    await usage(2); expected += 2;
    await delay(2000);
    assert.equal(await page.evaluate(() => state.totals.total_tokens), 0, 'today push contaminated custom range');
    await page.selectOption('#period', 'today'); await waitTotal(page, expected);

    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', {persisted:true})));
    await usage(3); expected += 3;
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', {persisted:true})));
    await waitTotal(page, expected);
    const response = await fetch(origin+'/api/export?format=json', {headers: auth});
    const exported = await response.json();
    assert.equal(exported.reduce((sum,row) => sum + row.total_tokens,0), expected, 'export diverged from confirmed integers');
  } finally { await browser.close(); }
}
async function mobile(width) {
  const {browser, page} = await newPage(webkit, width, true);
  try {
    await fits(page);
    const detail = page.locator('.child-records').first();
    await detail.locator(':scope > summary').click();
    await page.waitForFunction(() => document.querySelector('.child-records[open] tbody tr'));
    const old = await page.evaluate(() => {
      const node = document.querySelector('[data-card="exact"] [data-card-value]');
      const range = document.createRange(); range.selectNodeContents(node);
      getSelection().removeAllRanges(); getSelection().addRange(range);
      window.selectedNode = node; window.openDetail = document.querySelector('.child-records[open]');
      return {text:getSelection().toString(), total:state.totals.total_tokens};
    });
    await usage(1);
    await page.waitForFunction(total => state.totals.total_tokens === total, old.total+1);
    assert.equal(await page.evaluate(() => getSelection().toString()), old.text, 'selection was replaced');
    assert.equal(await page.evaluate(() => window.openDetail.open && window.selectedNode === document.querySelector('[data-card="exact"] [data-card-value]')), true);
    await page.evaluate(() => getSelection().removeAllRanges()); await waitTotal(page, old.total+1);
    assert.equal(await page.locator('.confirmed-change').count(), 0, 'reduced-motion must skip effects');
    await fits(page);
    await page.evaluate(() => { document.querySelectorAll('.project-group,.child-records').forEach(el=>el.open=false); scrollTo(0,0); });
    if (process.env.METER_BROWSER_OUTPUT) await page.screenshot({path:`${process.env.METER_BROWSER_OUTPUT}/mobile-${width}.png`,fullPage:true});
  } finally { await browser.close(); }
}
(async () => {
  await ingest();
  const initial = await snapshot();
  assert.equal(initial.hosts[0].alias, '合成测试设备');
  input = initial.sessions.find(row=>row.conversation_id===root).input_tokens;
  const beat = setInterval(() => ingest().catch(err=>timings.errors.push(err.message)),5000);
  try { await desktop(); await mobile(402); await mobile(320); assert.deepEqual(timings.errors,[]); console.log(JSON.stringify(timings,null,2)); }
  finally { clearInterval(beat); }
})().catch(err => {console.error(err.stack); process.exitCode=1});

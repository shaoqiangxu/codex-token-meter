const assert = require('node:assert/strict');
const fs = require('node:fs');
const {token, tokenM, groupedSessions, dashboardCards, sessionMetricValues, viewStructure, updateText} = require('../web/app.js');

assert.equal(token(1234500), '1.2345M');
assert.equal(token(999999), '999999');
assert.equal(tokenM(0), '0.0000M');

const cards = dashboardCards({
  totals: {
    total_tokens: 1234500,
    input_tokens: 1200000,
    output_tokens: 34500,
    reasoning_output_tokens: 1200,
    cached_input_tokens: 900000,
    cache_write_input_tokens: 0,
    cache_write_visible: true
  },
  sessions: [{status: 'STALE', data_quality: 'LOWER_BOUND', last_event_at: new Date().toISOString()}],
  exchange_rate: {}
});
assert.equal(cards.find(item => item.key === 'exact').value, '1.2345M');
assert.equal(cards.find(item => item.key === 'io').value, '1.2000M/0.0345M');
assert.match(cards.find(item => item.key === 'cache').note, /明确上报写入为0/);
assert.equal(cards.find(item => item.key === 'generating').value, '日志不可估算');

const baseRows = [{host_id: 'h', conversation_id: 'child', parent_conversation_id: 'root', project: 'project', name: 'task', total_tokens: 10}];
const changedRows = [...baseRows, {host_id: 'h', conversation_id: 'child-2', parent_conversation_id: 'root', project: 'project', name: 'task', total_tokens: 20}];
const baseGroup = groupedSessions(baseRows)[0];
const changedGroup = groupedSessions(changedRows)[0];
assert.equal(sessionMetricValues(baseGroup).total.value, '0.0000M');
assert.equal(sessionMetricValues(baseGroup).cache_write.note, '日志明确上报0');
const hosts = [{host_id: 'h'}];
assert.equal(viewStructure({query: '', hosts, groups: [baseGroup]}), viewStructure({query: '', hosts, groups: [changedGroup]}), 'numeric and internal-record updates must not rebuild the page structure');
assert.equal(viewStructure({query: '', hosts, groups: [baseGroup, changedGroup]}), viewStructure({query: '', hosts, groups: [changedGroup, baseGroup]}), 'activity-driven ordering must not rebuild the page structure');
assert.notEqual(viewStructure({query: '', hosts, groups: [baseGroup]}), viewStructure({query: '', hosts, groups: [baseGroup, {...baseGroup, root_id: 'new-root'}]}), 'a new logical session must change the structure');

const repairedGroups = groupedSessions([
  {host_id: 'h', conversation_id: 'original', project: 'Example Project', name: 'Original task', total_tokens: 110, api_cost: 1},
  {host_id: 'h', conversation_id: 'msg_legacy', parent_conversation_id: 'original', project: 'Example Project', name: 'Original task', total_tokens: 40, api_cost: .5},
  {host_id: 'h', conversation_id: 'different-task', project: 'Example Project', name: 'Different task', total_tokens: 10, api_cost: .1}
]);
assert.equal(repairedGroups.length, 2, 'a legacy message is not a task, but another native task remains distinct');
assert.equal(new Set(repairedGroups.map(group => group.project)).size, 1, 'explicit project aliases must share one project');
assert.equal(repairedGroups.find(group => group.root_id === 'original').total_tokens, 150);
assert.equal(repairedGroups.find(group => group.root_id === 'original').api_cost, 1.5);
assert.equal(repairedGroups.find(group => group.root_id === 'original').records.length, 2, 'keep the original message record available for auditing');

const selected = {textContent: '可复制内容'};
global.document = {
  getSelection: () => ({isCollapsed: false, rangeCount: 1, getRangeAt: () => ({intersectsNode: node => node === selected})})
};
updateText(selected, '刷新值');
assert.equal(selected.textContent, '可复制内容', 'live updates must not replace selected text');
global.document = {getSelection: () => ({isCollapsed: true, rangeCount: 0})};
updateText(selected, '刷新值');
assert.equal(selected.textContent, '刷新值');
delete global.document;

assert.match(fs.readFileSync(require.resolve('../web/style.css'), 'utf8'), /body>#_copy\._copied-button\{display:none!important\}/);

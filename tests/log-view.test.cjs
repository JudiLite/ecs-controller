'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const logs = require('../static/log-view.js');
const context = vm.createContext({ console, setTimeout, clearTimeout });
vm.runInContext(fs.readFileSync(path.join(__dirname, '../static/vue.global.prod.js'), 'utf8'), context);
const Vue = context.Vue;
const record = (id, type = 'info', message = `message ${id}`, created = 1760000000 + id * 60) => ({ id, type, message, created_at: created, time_str: `2026-10-15 11:${String(id % 60).padStart(2, '0')}:00` });
const page = (data, more = false, extra = {}) => ({ data, has_more: more, next_before_id: data.at(-1)?.id || 0, latest_id: data[0]?.id || 0, server_time: 1760009000, monitor_interval_seconds: 60, last_heartbeat: null, ...extra });
const response = (payload, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => payload });
const drain = () => new Promise(resolve => setImmediate(resolve));
function fixture(extra = {}) {
    const requests = [], timers = new Map(), notices = [], copied = [];
    let id = 0, resets = 0, pollStarts = 0, pollStops = 0;
    const view = logs.create({
        Vue, tab: Vue.ref('action'), items: Vue.ref([]), auto: Vue.ref(true),
        fetch: (url, options) => new Promise(resolve => requests.push({ url, options, resolve })),
        setTimeout: fn => { timers.set(++id, fn); return id; }, clearTimeout: key => timers.delete(key),
        setInterval: () => { pollStarts++; return 1; }, clearInterval: () => { pollStops++; },
        notify: (...args) => notices.push(args), copy: value => { copied.push(value); }, confirm: async () => true,
        onReset: () => { resets++; }, ...extra
    });
    return { view, requests, notices, copied, counts: () => ({ resets, pollStarts, pollStops }), flushSearch: () => { const pending = [...timers.values()]; timers.clear(); pending.forEach(fn => fn()); } };
}

test('reactive display rows expand and collapse without changing raw records', async t => {
    const f = fixture({ tab: Vue.ref('heartbeat') }); t.after(() => f.view.dispose());
    const entries = [record(3, 'heartbeat', 'loop', 1000), record(2, 'heartbeat', 'loop', 940), record(1, 'heartbeat', 'loop', 880)];
    const start = f.view.start(); f.requests[0].resolve(response(page(entries, true))); await start;
    assert.equal(f.view.displayRows.length, 1);
    const group = f.view.rows[0];
    f.view.toggleExpanded(group);
    assert.equal(f.view.displayRows.length, 4);
    await f.view.copyLog(f.view.displayRows[1]);
    assert.equal(f.copied[0].split('\n').length, 1);
    f.view.toggleExpanded(group);
    assert.equal(f.view.displayRows.length, 1);
    assert.equal(f.view.state.items.length, 3);
    assert.equal(f.view.state.nextBeforeID, 1);
    assert.equal(f.view.state.hasMore, true);
});

test('heartbeat merging keeps original records and splits gaps, dates and changed messages', () => {
    const a = record(5, 'heartbeat', 'loop', 1000), b = record(4, 'heartbeat', 'loop', 940), c = record(3, 'heartbeat', 'loop', 700);
    const d = { ...record(2, 'heartbeat', 'loop', 640), time_str: '2026-10-14 23:59:00' };
    const e = { ...record(1, 'heartbeat', 'changed', 580), time_str: '2026-10-14 23:58:00' };
    const input = [a, b, c, d, e], before = JSON.stringify(input);
    const rows = logs.buildRows(input, true, 60);
    assert.equal(rows.length, 4);
    assert.equal(rows[0].entries.length, 2);
    assert.equal(rows.flatMap(row => row.entries).length, input.length);
    assert.equal(JSON.stringify(input), before);
    assert.equal(logs.buildRows(input, false, 60).length, 5);
    assert.equal(logs.buildRows([record(1, 'error', 'same'), record(2, 'error', 'same')], true).length, 2);
});

test('expanded heartbeat records become sibling rows without duplicating source entries', () => {
    const entries = [record(3, 'heartbeat', 'loop', 1000), record(2, 'heartbeat', 'loop', 940), record(1, 'heartbeat', 'loop', 880)];
    const before = JSON.stringify(entries);
    const groups = logs.buildRows(entries, true, 60);
    const collapsed = logs.flattenRows(groups, {});
    assert.equal(collapsed.length, 1);
    assert.equal(collapsed[0].isGroup, true);
    const expanded = logs.flattenRows(groups, { [groups[0].key]: true });
    assert.equal(expanded.length, 4);
    assert.equal(expanded[0].isGroup, true);
    assert.deepEqual(expanded.slice(1).map(row => row.id), [3, 2, 1]);
    assert.ok(expanded.slice(1).every(row => row.isDetail && row.entries.length === 1 && row.groupKey === groups[0].key));
    assert.equal(new Set(expanded.map(row => row.key)).size, expanded.length);
    assert.equal(JSON.stringify(entries), before);
});

test('flattening preserves normal rows and independently expanded groups', () => {
    const groups = logs.buildRows([record(5, 'heartbeat', 'a', 1000), record(4, 'heartbeat', 'a', 940), record(3, 'error', 'failure', 900), record(2, 'heartbeat', 'b', 880), record(1, 'heartbeat', 'b', 820)], true, 60);
    const shown = logs.flattenRows(groups, { [groups[0].key]: true });
    assert.equal(shown.length, 5);
    assert.equal(shown.find(row => row.id === 3).isDetail, false);
    assert.equal(shown.at(-1).isGroup, true);
    assert.equal(shown.filter(row => row.isDetail).length, 2);
});

test('template renders heartbeat details in the shared grid, not a nested list', () => {
    const template = fs.readFileSync(path.join(__dirname, '../template.html'), 'utf8');
    assert.ok(template.includes('v-for="row in logView.displayRows"'));
    assert.ok(!template.includes('log-group-entries'));
    assert.ok(template.includes('grid-template-columns: var(--logs-columns)'));
    assert.ok(template.includes('scrollbar-width: thin'));
    const styles = template.slice(template.indexOf('.logs-panel {'), template.indexOf('.desktop-visible {'));
    assert.ok(!styles.includes('scrollbar-width: none'));
    const scroll = styles.match(/\.logs-scroll\s*\{([^}]+)\}/)[1];
    assert.match(scroll, /overflow: auto/);
    assert.match(scroll, /scrollbar-gutter: auto/);
    assert.match(scroll, /padding-right: 0/);
    assert.ok(!scroll.includes('scrollbar-gutter: stable'));
});

test('common credentials are masked without hiding diagnostic codes', () => {
    const message = 'InvalidAccessKeyId.NotFound https://ecs.example/?AccessKeyId=example-ak&Signature=abc%2F123&RegionId=cn-hongkong password="secret with spaces" token:abc';
    const safe = logs.safeMessage(message);
    assert.match(safe, /InvalidAccessKeyId.NotFound/);
    assert.match(safe, /RegionId=cn-hongkong/);
    for (const secret of ['example-ak', 'abc%2F123', 'secret with spaces', 'token:abc']) assert.ok(!safe.includes(secret));
    assert.ok(!logs.safeMessage('Authorization: Bearer sensitive-value').includes('sensitive-value'));
    assert.ok(!logs.safeMessage('socks5://user:password@proxy.example/').includes('user:password'));
    assert.ok(!logs.safeMessage('-----BEGIN PRIVATE KEY-----\nSECRET\n-----END PRIVATE KEY-----').includes('SECRET'));
    assert.equal(logs.safeMessage('token refresh succeeded'), 'token refresh succeeded');
});

test('cursor loading appends older pages and polling never replaces the reading snapshot', async t => {
    const f = fixture(); t.after(() => f.view.dispose());
    const start = f.view.start();
    f.requests[0].resolve(response(page([record(10), record(9)], true)));
    await start;
    f.view.state.expanded['log-10'] = true;
    const more = f.view.loadMore();
    assert.equal(new URL(f.requests[1].url, 'http://test').searchParams.get('before_id'), '9');
    f.requests[1].resolve(response(page([record(9), record(8)], false, { latest_id: 10 })));
    await more;
    assert.deepEqual([...f.view.state.items.map(item => item.id)], [10, 9, 8]);
    const motionBeforePoll = f.view.motionKey;
    const poll = f.view.poll();
    f.requests[2].resolve(response(page([record(11), record(10)], true, { last_heartbeat: record(50, 'heartbeat') })));
    await poll;
    assert.equal(f.view.motionKey, motionBeforePoll);
    assert.deepEqual([...f.view.state.items.map(item => item.id)], [10, 9, 8]);
    assert.equal(f.view.state.pending, 'new');
    assert.equal(f.view.state.expanded['log-10'], true);
    assert.equal(f.view.state.lastHeartbeat.id, 50);
    assert.equal(f.counts().resets, 1);
    const refresh = f.view.refresh();
    f.requests[3].resolve(response(page([record(11), record(10)])));
    await refresh;
    assert.equal(f.view.state.pending, '');
    assert.equal(f.view.state.expanded['log-10'], undefined);
    assert.equal(f.counts().resets, 2);
});

test('filter changes abort requests and stale replies cannot cross categories', async t => {
    const f = fixture(); t.after(() => f.view.dispose());
    const first = f.view.start();
    const stale = f.requests[0];
    f.view.selectTab('heartbeat');
    assert.equal(stale.options.signal.aborted, true);
    f.requests[1].resolve(response(page([record(20, 'heartbeat')])));
    await drain();
    stale.resolve(response(page([record(10)])));
    await first;
    assert.equal(f.view.state.items[0].type, 'heartbeat');
    f.view.state.search = 'literal %_';
    assert.equal(f.view.state.loading, true);
    f.flushSearch();
    const search = f.requests.at(-1);
    assert.equal(new URL(search.url, 'http://test').searchParams.get('q'), 'literal %_');
    search.resolve(response(page([record(19, 'heartbeat')])));
    await drain();
    assert.equal(f.view.state.items[0].id, 19);
});

test('heartbeat tab resets an action-level filter', async t => {
    const f = fixture(); t.after(() => f.view.dispose());
    f.view.state.level = 'error';
    const start = f.view.start(); f.requests[0].resolve(response(page([]))); await start;
    f.view.selectTab('heartbeat');
    assert.equal(f.view.state.level, 'all');
    assert.equal(new URL(f.requests.at(-1).url, 'http://test').searchParams.get('level'), 'all');
    f.requests.at(-1).resolve(response(page([]))); await drain();
});

test('pause, hidden pages and stopped views do not poll', async t => {
    let visible = true;
    const f = fixture({ visible: () => visible }); t.after(() => f.view.dispose());
    const start = f.view.start(); f.requests[0].resolve(response(page([]))); await start;
    f.view.state.auto = false; await f.view.poll(); assert.equal(f.requests.length, 1);
    assert.ok(f.counts().pollStops > 0);
    f.view.state.auto = true; visible = false; await f.view.poll(); assert.equal(f.requests.length, 1);
    visible = true; f.view.stop(); await f.view.poll(); assert.equal(f.requests.length, 1);
});

test('read errors keep old rows and pagination cursor available for retry', async t => {
    const f = fixture(); t.after(() => f.view.dispose());
    const start = f.view.start(); f.requests[0].resolve(response(page([record(10)], true))); await start;
    const more = f.view.loadMore(); f.requests[1].resolve(response({ message: 'temporary failure' }, 500)); await more;
    assert.equal(f.view.state.items[0].id, 10);
    assert.equal(f.view.state.nextBeforeID, 10);
    assert.equal(f.view.state.error, 'temporary failure');
    const retry = f.view.retry(); f.requests[2].resolve(response(page([record(9)]))); await retry;
    assert.deepEqual([...f.view.state.items.map(item => item.id)], [10, 9]);
    const poll = f.view.poll(); f.requests[3].resolve(response({ message: 'offline' }, 500)); await poll;
    assert.ok(f.view.state.pollError);
    assert.equal(f.view.state.items.length, 2);
});

test('clear confirmation captures the intended category and never uses the search filter', async t => {
    let accept;
    const f = fixture({ confirm: () => new Promise(resolve => { accept = resolve; }) }); t.after(() => f.view.dispose());
    const start = f.view.start(); f.requests[0].resolve(response(page([record(10)]))); await start;
    const clear = f.view.clearSelected();
    f.view.selectTab('heartbeat');
    f.requests[1].resolve(response(page([record(20, 'heartbeat')]))); await drain();
    accept(true); await drain();
    const request = f.requests.at(-1);
    assert.equal(request.url, 'index.php?action=clear_logs');
    assert.deepEqual(JSON.parse(request.options.body), { tab: 'action' });
    request.resolve(response({ success: true })); await clear;
    assert.equal(f.view.state.items[0].type, 'heartbeat');
    assert.equal(f.view.state.clearing, false);
});

test('canceling clear preserves rows and does not send deletion requests', async t => {
    const f = fixture({ confirm: async () => false }); t.after(() => f.view.dispose());
    const start = f.view.start(); f.requests[0].resolve(response(page([record(10)]))); await start;
    await f.view.clearSelected();
    assert.equal(f.requests.length, 1);
    assert.equal(f.view.state.items.length, 1);
});

test('copying grouped records retains timestamps but masks secrets', async t => {
    const f = fixture(); t.after(() => f.view.dispose());
    const entries = [record(2, 'heartbeat', 'token=hidden-value', 1000), record(1, 'heartbeat', 'token=hidden-value', 940)];
    const row = logs.buildRows(entries, true, 60)[0];
    await f.view.copyLog(row);
    assert.equal(f.copied[0].split('\n').length, 2);
    assert.ok(!f.copied[0].includes('hidden-value'));
    assert.ok(f.copied[0].includes(entries[0].time_str));
});

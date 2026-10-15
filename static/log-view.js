(function (root, factory) {
    const api = factory(root);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.ECSLogView = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function (root) {
    'use strict';

    const levelLabel = (type) => ({ info: '信息', warning: '警告', error: '错误', heartbeat: '心跳' }[type] || '日志');
    const safeMessage = (value) => String(value ?? '')
        .replace(/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g, '[私钥已隐藏]')
        .replace(/(:\/\/)[^\s/]*@/g, '$1[凭据已隐藏]@')
        .replace(/(\bAuthorization\s*[:=]\s*)(?:Bearer|Basic)\s+[^\s&,;]+/gi, '$1[已隐藏]')
        .replace(/(["']?(?:AccessKeyId|AccessKeySecret|SignatureNonce|Signature|SecurityToken|Authorization|ECS_SETUP_TOKEN|token|password|secret)["']?\s*[:=：]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s&,;]+)/gi, '$1[已隐藏]');

    const buildRows = (items, merge, interval = 60) => {
        const rows = [];
        const maximumGap = Math.max(30, Number(interval) || 60) * 1.5 + 5;
        for (const item of items) {
            const previous = rows[rows.length - 1];
            const last = previous?.entries[previous.entries.length - 1];
            const gap = last ? Number(last.created_at) - Number(item.created_at) : NaN;
            const sameDay = last && String(last.time_str).slice(0, 10) === String(item.time_str).slice(0, 10);
            if (merge && item.type === 'heartbeat' && last?.type === 'heartbeat' && item.message === last.message && sameDay && Number.isFinite(gap) && gap > 0 && gap <= maximumGap) {
                previous.entries.push(item);
            } else {
                rows.push({ ...item, key: `log-${item.id}`, entries: [item] });
            }
        }
        return rows;
    };

    const create = (options) => {
        const Vue = options.Vue;
        const state = Vue.reactive({
            tab: options.tab || Vue.ref('action'),
            items: options.items || Vue.ref([]),
            auto: options.auto || Vue.ref(true),
            search: '', level: 'all', mergeHeartbeats: true,
            loading: false, loadingMore: false, polling: false, clearing: false,
            error: '', errorMode: '', pollError: '', pending: '',
            hasMore: false, nextBeforeID: 0, latestID: 0,
            lastHeartbeat: null, serverTime: 0, monitorInterval: 60,
            expanded: {}, menuOpen: false
        });
        const rows = Vue.computed(() => buildRows(state.items, state.tab === 'heartbeat' && state.mergeHeartbeats, state.monitorInterval));
        const fetcher = options.fetch || ((...args) => root.fetch(...args));
        const setTimer = options.setTimeout || root.setTimeout.bind(root);
        const clearTimer = options.clearTimeout || root.clearTimeout.bind(root);
        const setPoll = options.setInterval || root.setInterval.bind(root);
        const clearPoll = options.clearInterval || root.clearInterval.bind(root);
        const visible = options.visible || (() => !root.document?.hidden);
        const requests = new Map();
        let active = false;
        let disposed = false;
        let generation = 0;
        let interval = null;
        let debounce = null;
        const scopeKey = () => JSON.stringify([state.tab, state.level, state.search.trim()]);
        const clearDebounce = () => { if (debounce != null) clearTimer(debounce); debounce = null; };
        const cancelReads = () => {
            generation++;
            for (const controller of requests.values()) controller.abort();
            requests.clear();
            state.loading = state.loadingMore = state.polling = false;
        };
        const updateMetadata = (payload) => {
            const timestamp = Number(payload.server_time) || 0;
            if (timestamp && timestamp < state.serverTime) return;
            state.serverTime = timestamp;
            if (Object.prototype.hasOwnProperty.call(payload, 'last_heartbeat')) state.lastHeartbeat = payload.last_heartbeat;
            state.monitorInterval = Math.max(30, Number(payload.monitor_interval_seconds) || 60);
        };
        const resetItems = () => {
            state.items = [];
            state.hasMore = false;
            state.nextBeforeID = state.latestID = 0;
            state.pending = state.error = state.pollError = '';
            state.expanded = {};
        };
        const load = async (mode = 'replace') => {
            if (!active || disposed) return;
            const requestGeneration = generation;
            const scope = scopeKey();
            const controller = new AbortController();
            requests.set(mode, controller);
            const flag = mode === 'more' ? 'loadingMore' : mode === 'poll' ? 'polling' : 'loading';
            state[flag] = true;
            if (mode !== 'poll') state.error = '';
            const current = () => active && !disposed && generation === requestGeneration && scopeKey() === scope && requests.get(mode) === controller;
            try {
                const query = new URLSearchParams({ action: 'get_logs', tab: state.tab, level: state.tab === 'heartbeat' ? 'all' : state.level, limit: '50' });
                if (state.search.trim()) query.set('q', state.search.trim());
                if (mode === 'more') query.set('before_id', String(state.nextBeforeID));
                const response = await fetcher(`index.php?${query}`, { signal: controller.signal, cache: 'no-store' });
                const payload = await response.json();
                if (!current()) return;
                if (response.status === 403) options.onUnauthorized?.();
                if (!response.ok || payload.error || !Array.isArray(payload.data)) throw new Error(payload.message || '日志读取失败，请稍后重试');
                updateMetadata(payload);
                const latest = Number(payload.latest_id ?? payload.data[0]?.id) || 0;
                if (mode === 'poll') {
                    state.pollError = '';
                    if (latest > state.latestID) state.pending = 'new';
                    else if (latest < state.latestID) state.pending = 'changed';
                    return;
                }
                if (mode === 'more') {
                    const ids = new Set(state.items.map(item => item.id));
                    state.items = [...state.items, ...payload.data.filter(item => !ids.has(item.id))];
                    if (latest > state.latestID) state.pending = 'new';
                } else {
                    state.items = payload.data;
                    state.latestID = latest;
                    state.pending = state.pollError = '';
                    state.expanded = {};
                    options.onReset?.();
                }
                state.hasMore = !!payload.has_more;
                state.nextBeforeID = Number(payload.next_before_id ?? payload.data[payload.data.length - 1]?.id) || 0;
                if (state.hasMore && !state.nextBeforeID) throw new Error('日志分页信息无效，请刷新后重试');
            } catch (error) {
                if (!current() || error.name === 'AbortError') return;
                if (mode === 'poll') state.pollError = '自动刷新失败，已保留当前记录';
                else {
                    state.error = safeMessage(error.message || '日志请求失败，请稍后重试');
                    state.errorMode = mode;
                }
            } finally {
                if (requests.get(mode) === controller) {
                    requests.delete(mode);
                    state[flag] = false;
                }
            }
        };
        const refresh = () => {
            clearDebounce();
            cancelReads();
            return load();
        };
        const poll = () => {
            if (!active || !state.auto || !visible() || state.loading || state.loadingMore || state.polling || state.clearing || debounce != null) return Promise.resolve();
            return load('poll');
        };
        const configurePolling = () => {
            if (interval != null) clearPoll(interval);
            interval = active && state.auto ? setPoll(poll, 5000) : null;
        };
        const loadMore = () => {
            if (!active || !state.hasMore || state.loading || state.loadingMore) return Promise.resolve();
            const pollRequest = requests.get('poll');
            if (pollRequest) {
                requests.delete('poll');
                pollRequest.abort();
                state.polling = false;
            }
            return load('more');
        };
        const stopScope = Vue.watch(() => [state.tab, state.level, state.search], (next, previous) => {
            if (state.tab === 'heartbeat' && state.level !== 'all') { state.level = 'all'; return; }
            clearDebounce();
            cancelReads();
            resetItems();
            if (!active) return;
            if (next[2] !== previous[2]) {
                state.loading = true;
                debounce = setTimer(() => { debounce = null; refresh(); }, 300);
            } else refresh();
        }, { flush: 'sync' });
        const stopAuto = Vue.watch(() => state.auto, configurePolling, { flush: 'sync' });
        const start = () => {
            if (active || disposed) return Promise.resolve();
            active = true;
            resetItems();
            configurePolling();
            return refresh();
        };
        const stop = () => {
            active = false;
            clearDebounce();
            cancelReads();
            if (interval != null) clearPoll(interval);
            interval = null;
            state.menuOpen = false;
        };
        const clearSelected = async () => {
            if (state.clearing || disposed) return;
            const tab = state.tab;
            const name = tab === 'heartbeat' ? '心跳' : '动作';
            state.menuOpen = false;
            state.clearing = true;
            try {
                const accepted = await options.confirm(`确定清空全部${name}日志吗？这包括未加载及被筛选隐藏的记录，清空后无法恢复。`, { title: `清空${name}日志`, confirmLabel: '确认清空' });
                if (!accepted || disposed) return;
                const response = await fetcher('index.php?action=clear_logs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tab }) });
                const payload = await response.json();
                if (response.status === 403) options.onUnauthorized?.();
                if (!response.ok || !payload.success) throw new Error(payload.message || '日志清空失败');
                if (!disposed) options.notify?.(`${name}日志已清空`, 'success');
                if (active && state.tab === tab) await refresh();
            } catch (error) {
                if (!disposed) options.notify?.(safeMessage(error.message || '日志清空失败'), 'error');
            } finally {
                state.clearing = false;
            }
        };
        const dispose = () => { stop(); disposed = true; stopScope(); stopAuto(); };
        const copyLog = (row) => options.copy(row.entries.map(item => `${item.time_str} [${levelLabel(item.type)}] ${safeMessage(item.message)}`).join('\n'));
        const toggleExpanded = (row) => { state.expanded[row.key] = !state.expanded[row.key]; };
        const longMessage = (row) => { const text = safeMessage(row.message); return text.length > 80 || text.includes('\n'); };
        return Vue.reactive({
            state, rows, start, stop, dispose, refresh, poll, loadMore, clearSelected, copyLog, safeMessage, levelLabel, longMessage, toggleExpanded,
            retry: () => state.errorMode === 'more' ? loadMore() : refresh(),
            selectTab: (tab) => { state.menuOpen = false; state.tab = tab; },
            resetFilters: () => { state.search = ''; state.level = 'all'; },
            closeMenu: (event) => { state.menuOpen = false; event?.currentTarget?.querySelector('button[aria-haspopup]')?.focus(); },
            closeMenuOnBlur: (event) => { if (!event.currentTarget.contains(event.relatedTarget)) state.menuOpen = false; }
        });
    };
    return { create, safeMessage, buildRows, levelLabel };
});

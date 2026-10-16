'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../static/log-motion.js'), 'utf8');

function fixture(reduced = false) {
    const globalEvents = new Map(), localEvents = new Map(), mediaEvents = new Map(), animations = [];
    const media = { matches: reduced, addEventListener: (name, fn) => mediaEvents.set(name, fn), removeEventListener: name => mediaEvents.delete(name) };
    const context = vm.createContext({ matchMedia: () => media, addEventListener: (name, fn) => globalEvents.set(name, fn), removeEventListener: name => globalEvents.delete(name) });
    vm.runInContext(source, context);
    let height = 100;
    const element = {
        style: { minHeight: '' },
        querySelectorAll: () => [],
        getBoundingClientRect: () => ({ top: 0, bottom: Math.max(height, Number.parseFloat(element.style.minHeight) || 0), height: Math.max(height, Number.parseFloat(element.style.minHeight) || 0) }),
        addEventListener: (name, fn) => localEvents.set(name, fn), removeEventListener: name => localEvents.delete(name),
        animate: (frames, options) => {
            let reject;
            const item = { frames, options, canceled: false, finished: new Promise((_, fail) => { reject = fail; }), cancel() { this.canceled = true; reject(new Error('canceled')); } };
            animations.push(item);
            return item;
        }
    };
    const api = context.ECSLogMotion;
    api.directive.mounted(element);
    return { api, element, media, animations, globalEvents, localEvents, mediaEvents, height: value => { height = value; }, close: () => api.directive.beforeUnmount(element) };
}

test('polling metadata and busy indicators alone do not mark the list as changed', () => {
    const f = fixture();
    assert.equal(f.api.changed({ key: 'same', busy: false }, { key: 'same', busy: true }), false);
    f.api.directive.beforeUpdate(f.element, { oldValue: { key: 'same' }, value: { key: 'same' } });
    f.api.directive.updated(f.element, { value: { key: 'same' } });
    assert.equal(f.animations.length, 0);
    assert.equal(f.api.changed({ key: 'a' }, { key: 'b' }), true);
    f.close();
});

test('group height transition is bounded and cleaned up on user interruption', async () => {
    const f = fixture();
    f.api.directive.beforeUpdate(f.element, { oldValue: { key: 'a' }, value: { key: 'b' } });
    f.height(300);
    f.api.directive.updated(f.element, { value: { key: 'b' } });
    assert.equal(f.animations.length, 1);
    assert.equal(f.animations[0].options.duration, 200);
    assert.deepEqual(JSON.parse(JSON.stringify(f.animations[0].frames)), [{ height: '100px' }, { height: '300px' }]);
    f.localEvents.get('wheel')();
    assert.equal(f.animations[0].canceled, true);
    f.close();
    await Promise.resolve();
    assert.equal(f.localEvents.size, 0);
    assert.equal(f.globalEvents.size, 0);
    assert.equal(f.mediaEvents.size, 0);
});

test('filter loading holds the reading area until a result arrives', () => {
    const f = fixture(); f.height(300);
    f.api.directive.beforeUpdate(f.element, { oldValue: { key: 'old' }, value: { key: 'new', busy: true } });
    f.height(80);
    f.api.directive.updated(f.element, { value: { key: 'new', busy: true } });
    assert.equal(f.element.style.minHeight, '300px');
    f.api.directive.beforeUpdate(f.element, { oldValue: { key: 'new', busy: true }, value: { key: 'new', busy: false } });
    f.api.directive.updated(f.element, { value: { key: 'new', busy: false } });
    assert.equal(f.element.style.minHeight, '');
    assert.equal(f.animations.length, 1);
    f.close();
});

test('reduced motion skips JavaScript animations and cancels active transitions', () => {
    const reduced = fixture(true);
    reduced.api.directive.beforeUpdate(reduced.element, { oldValue: { key: 'a' }, value: { key: 'b' } });
    reduced.height(300);
    reduced.api.directive.updated(reduced.element, { value: { key: 'b' } });
    assert.equal(reduced.animations.length, 0);
    reduced.close();
    const f = fixture();
    f.api.directive.beforeUpdate(f.element, { oldValue: { key: 'a' }, value: { key: 'b' } });
    f.height(300); f.api.directive.updated(f.element, { value: { key: 'b' } });
    f.media.matches = true; f.mediaEvents.get('change')();
    assert.equal(f.animations[0].canceled, true);
    f.close();
});

test('motion markup is scoped to logs and global reduced-motion styles remain present', () => {
    const template = fs.readFileSync(path.join(__dirname, '../template.html'), 'utf8');
    assert.ok(template.includes('directives: { logMotion: ECSLogMotion.directive }'));
    assert.ok(template.includes('v-log-motion="{ key: logView.motionKey, busy: logView.state.loading }"'));
    assert.ok(template.includes('<transition name="logs-menu">'));
    assert.ok(template.includes('data-log-key="row.key"'));
    assert.ok(template.includes('prefers-reduced-motion: reduce'));
    assert.ok(template.includes('logView.state.loadingMore ? \'加载中\''));
});

(function (root, factory) {
    const api = factory(root);
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.ECSLogMotion = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function (root) {
    'use strict';
    const duration = 200;
    const states = new WeakMap();
    const changed = (previous, next) => previous?.key !== next?.key;
    const intersects = (rect, viewport) => rect.bottom > viewport.top && rect.top < viewport.bottom;
    const realRows = el => [...el.querySelectorAll('.log-row[data-log-key]')].filter(row => !row.closest('[data-log-ghost]'));
    const restoreHold = (el, state) => {
        el.style.minHeight = state.originalMinHeight;
        state.holding = false;
    };
    const cancel = (el, state, release = false) => {
        for (const animation of state.animations) animation.cancel();
        state.animations.clear();
        state.overlay?.remove();
        state.overlay = null;
        state.pending = null;
        if (release) restoreHold(el, state);
    };
    const animate = (state, element, frames, milliseconds = duration) => {
        const animation = element.animate(frames, { duration: milliseconds, easing: 'cubic-bezier(.22,.61,.36,1)' });
        state.animations.add(animation);
        animation.finished.catch(() => {}).finally(() => state.animations.delete(animation));
        return animation;
    };
    const directive = {
        mounted(el) {
            const media = root.matchMedia?.('(prefers-reduced-motion: reduce)');
            const state = { animations: new Set(), overlay: null, pending: null, holding: false, originalMinHeight: el.style.minHeight, media };
            state.interrupt = () => cancel(el, state, true);
            state.preference = () => { if (media.matches) state.interrupt(); };
            for (const event of ['wheel', 'touchstart', 'pointerdown', 'keydown']) el.addEventListener(event, state.interrupt, { passive: true });
            root.addEventListener('resize', state.interrupt);
            media?.addEventListener?.('change', state.preference);
            states.set(el, state);
        },
        beforeUpdate(el, binding) {
            const state = states.get(el);
            if (!state || (!changed(binding.oldValue, binding.value) && !(state.holding && !binding.value?.busy))) return;
            if (state.media?.matches || typeof el.animate !== 'function') { cancel(el, state, true); return; }
            const viewport = el.getBoundingClientRect();
            const positions = new Map();
            const visible = [];
            for (const row of realRows(el)) {
                const rect = row.getBoundingClientRect();
                positions.set(row.dataset.logKey, rect);
                if (visible.length < 16 && intersects(rect, viewport)) {
                    const clone = row.cloneNode(true);
                    clone.removeAttribute('data-log-key');
                    clone.setAttribute('aria-hidden', 'true');
                    clone.setAttribute('inert', '');
                    visible.push({ key: row.dataset.logKey, rect, clone });
                }
            }
            const snapshot = { height: viewport.height, viewport, positions, visible, changed: changed(binding.oldValue, binding.value) };
            cancel(el, state);
            state.pending = snapshot;
        },
        updated(el, binding) {
            const state = states.get(el);
            const snapshot = state?.pending;
            if (!snapshot) return;
            state.pending = null;
            if (state.media?.matches) { cancel(el, state, true); return; }
            const rows = realRows(el);
            if (binding.value?.busy && !rows.length && snapshot.height > 0) {
                el.style.minHeight = `${snapshot.height}px`;
                state.holding = true;
            } else restoreHold(el, state);
            const viewport = el.getBoundingClientRect();
            const targetHeight = viewport.height;
            const positions = new Map(rows.map(row => [row.dataset.logKey, { row, rect: row.getBoundingClientRect() }]));
            if (Math.abs(snapshot.height - targetHeight) > 1) animate(state, el, [{ height: `${snapshot.height}px` }, { height: `${targetHeight}px` }]);
            const leaving = snapshot.visible.filter(item => !positions.has(item.key) || !intersects(positions.get(item.key).rect, viewport));
            if (leaving.length) {
                const overlay = el.ownerDocument.createElement('div');
                overlay.dataset.logGhost = '';
                overlay.setAttribute('aria-hidden', 'true');
                overlay.setAttribute('inert', '');
                Object.assign(overlay.style, { position: 'absolute', pointerEvents: 'none', left: '0', top: `${el.scrollTop}px`, width: '100%', height: `${snapshot.height}px`, overflow: 'hidden', zIndex: '1' });
                for (const item of leaving) {
                    Object.assign(item.clone.style, { position: 'absolute', top: `${item.rect.top - snapshot.viewport.top}px`, left: `${item.rect.left - snapshot.viewport.left}px`, width: `${item.rect.width}px`, margin: '0', background: root.getComputedStyle(el).getPropertyValue('--panel').trim() });
                    overlay.append(item.clone);
                }
                el.append(overlay);
                state.overlay = overlay;
                const fade = animate(state, overlay, [{ opacity: 1 }, { opacity: 0 }], 140);
                fade.finished.catch(() => {}).finally(() => { overlay.remove(); if (state.overlay === overlay) state.overlay = null; });
            }
            let count = 0;
            for (const [key, item] of positions) {
                if (count >= 16 || !intersects(item.rect, viewport)) continue;
                const previous = snapshot.positions.get(key);
                if (!previous) {
                    animate(state, item.row, [{ opacity: 0, transform: 'translateY(4px)' }, { opacity: 1, transform: 'translateY(0)' }], 160);
                    count++;
                } else {
                    const delta = previous.top - item.rect.top;
                    if (Math.abs(delta) > 1) {
                        const shift = Math.max(-24, Math.min(24, delta));
                        animate(state, item.row, [{ transform: `translateY(${shift}px)` }, { transform: 'translateY(0)' }]);
                        count++;
                    }
                }
            }
        },
        beforeUnmount(el) {
            const state = states.get(el);
            if (!state) return;
            cancel(el, state, true);
            for (const event of ['wheel', 'touchstart', 'pointerdown', 'keydown']) el.removeEventListener(event, state.interrupt);
            root.removeEventListener('resize', state.interrupt);
            state.media?.removeEventListener?.('change', state.preference);
            states.delete(el);
        }
    };
    return { directive, changed, intersects, duration };
});

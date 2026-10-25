/* Shared focus and scroll behavior for the existing workspace dialogs. */
(() => {
    let activeOverlay = null;
    let returnFocus = null;
    let previousOverflow = '';
    const selector = '.account-modal-overlay, .credential-modal-overlay, .dialog-overlay';
    const focusable = (root) => [...root.querySelectorAll('button, input, select, textarea, a[href], [tabindex="0"]')].filter(el => !el.disabled && el.getClientRects().length);
    const syncDialog = () => {
        const overlays = [...document.querySelectorAll(selector)];
        const next = overlays.at(-1) || null;
        if (next === activeOverlay) return;
        if (next) {
            if (!activeOverlay) {
                returnFocus = document.activeElement;
                previousOverflow = document.body.style.overflow;
                document.body.style.overflow = 'hidden';
            }
            activeOverlay = next;
            next.setAttribute('role', 'dialog');
            next.setAttribute('aria-modal', 'true');
            const heading = next.querySelector('h2');
            if (heading) {
                heading.id ||= 'workspace-dialog-title';
                next.setAttribute('aria-labelledby', heading.id);
            }
            const workspace = document.querySelector('.app-container');
            if (workspace && !workspace.contains(next)) workspace.inert = true;
            requestAnimationFrame(() => {
                if (next === activeOverlay) (focusable(next)[0] || next).focus({ preventScroll: true });
            });
        } else {
            activeOverlay = null;
            document.body.style.overflow = previousOverflow;
            const workspace = document.querySelector('.app-container');
            if (workspace) workspace.inert = false;
            if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
            returnFocus = null;
        }
    };
    document.addEventListener('keydown', event => {
        if (!activeOverlay || event.defaultPrevented) return;
        const items = focusable(activeOverlay);
        if (event.key === 'Escape') {
            if (activeOverlay.querySelector('.select-shell.open, .time-picker.open')) return;
            const close = items.find(el => el.tagName === 'BUTTON' && ['关闭', '取消'].includes(el.textContent.trim()));
            if (close) { event.preventDefault(); close.click(); }
        } else if (event.key === 'Tab' && items.length) {
            const first = items[0], last = items.at(-1);
            if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
            else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
        }
    });
    window.addEventListener('resize', () => {
        if (window.innerWidth >= 1024) document.getElementById('mobile-more')?.close();
    }, { passive: true });
    new MutationObserver(syncDialog).observe(document.getElementById('app'), { childList: true, subtree: true });
    syncDialog();
})();

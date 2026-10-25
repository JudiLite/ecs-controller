/* Portable backup controls. Passwords remain in memory and are cleared on success. */
window.ECSBackupUI = ({ ref, watch, settingsDirty, openAlert }) => {
    const backupPassword = ref('');
    const backupPasswordRepeat = ref('');
    const restorePassword = ref('');
    const backupFile = ref(null);
    const backupBusy = ref('');
    const backupExportError = ref('');
    const backupRestoreError = ref('');
    const backupPreview = ref(null);
    const restoreConfirmation = ref('');
    const focusControl = (id) => requestAnimationFrame(() => document.getElementById(id)?.focus());
    const clearPreview = () => { backupPreview.value = null; restoreConfirmation.value = ''; backupRestoreError.value = ''; };
    watch(restorePassword, clearPreview);
    const selectBackupFile = (event) => {
        clearPreview();
        backupFile.value = event.target.files?.[0] || null;
        if (backupFile.value?.size > 32 * 1024 * 1024) {
            backupFile.value = null;
            event.target.value = '';
            backupRestoreError.value = '备份文件不能超过 32 MB';
        }
    };
    const checkResponse = async (response) => {
        if (!response.ok) {
            let message = '请求失败，请稍后重试';
            try { const data = await response.json(); message = data.error || data.message || message; } catch (_) { /* Keep a useful fallback for proxy errors. */ }
            throw new Error(message);
        }
        return response;
    };
    const download = async (response) => {
        const blob = await response.blob();
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = /filename="([^"]+)"/.exec(response.headers.get('Content-Disposition') || '')?.[1] || 'ecs-backup.ecsbak';
        document.body.appendChild(anchor); anchor.click(); anchor.remove();
        setTimeout(() => URL.revokeObjectURL(url), 60000);
    };
    const exportBackup = async () => {
        if (backupBusy.value || settingsDirty.value) return;
        backupExportError.value = '';
        if (Array.from(backupPassword.value).length < 12) { backupExportError.value = '备份密码至少需要 12 个字符'; focusControl('backup-password'); return; }
        if (backupPassword.value !== backupPasswordRepeat.value) { backupExportError.value = '两次输入的密码不一致'; focusControl('backup-password-repeat'); return; }
        backupBusy.value = 'export';
        try {
            await download(await checkResponse(await fetch('index.php?action=export_backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: backupPassword.value }) })));
            backupPassword.value = ''; backupPasswordRepeat.value = '';
            openAlert('加密备份已下载', 'success');
        } catch (error) { backupExportError.value = error.message; focusControl('backup-export-error'); }
        finally { backupBusy.value = ''; }
    };
    const backupRequest = async (action) => {
        const form = new FormData();
        form.set('backup', backupFile.value);
        form.set('password', restorePassword.value);
        form.set('confirmation', restoreConfirmation.value);
        form.set('fingerprint', backupPreview.value?.fingerprint || '');
        return (await checkResponse(await fetch(`index.php?action=${action}`, { method: 'POST', body: form }))).json();
    };
    const inspectBackup = async () => {
        if (backupBusy.value || !backupFile.value || settingsDirty.value) return;
        backupRestoreError.value = ''; backupPreview.value = null; restoreConfirmation.value = '';
        backupBusy.value = 'inspect';
        try { backupPreview.value = await backupRequest('inspect_backup'); focusControl('restore-preview-title'); }
        catch (error) { backupRestoreError.value = error.message; focusControl('backup-restore-error'); }
        finally { backupBusy.value = ''; }
    };
    const restoreBackup = async () => {
        if (backupBusy.value || !backupPreview.value || restoreConfirmation.value !== '恢复' || settingsDirty.value) return;
        backupRestoreError.value = ''; backupBusy.value = 'restore';
        try {
            const data = await backupRequest('restore_backup');
            restorePassword.value = ''; backupPassword.value = ''; backupPasswordRepeat.value = '';
            openAlert(data.message, 'success');
            setTimeout(() => window.location.reload(), 2200);
        } catch (error) { backupRestoreError.value = error.message; backupBusy.value = ''; focusControl('backup-restore-error'); }
    };
    const downloadRestoreBackup = async () => {
        if (backupBusy.value) return;
        backupBusy.value = 'download';
        try { await download(await checkResponse(await fetch('index.php?action=download_restore_backup', { method: 'POST' }))); }
        catch (error) { openAlert(error.message, 'error'); }
        finally { backupBusy.value = ''; }
    };
    return { backupPassword, backupPasswordRepeat, restorePassword, backupFile, backupBusy, backupExportError, backupRestoreError, backupPreview, restoreConfirmation, selectBackupFile, exportBackup, inspectBackup, restoreBackup, downloadRestoreBackup };
};

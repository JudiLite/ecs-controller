'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const repository = path.resolve(__dirname, '..');
const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'ecs-deploy-tests-'));
const bash = process.env.ECS_TEST_BASH || (process.platform === 'win32' ? 'C:/Program Files/Git/bin/bash.exe' : 'bash');
const setup = fs.readFileSync(path.join(repository, 'setup.sh'), 'utf8').replace(/\r/g, '');
const service = fs.readFileSync(path.join(repository, 'ecs'), 'utf8').replace(/\r/g, '');
const installer = fs.readFileSync(path.join(repository, 'install.sh'), 'utf8').replace(/\r/g, '');
const setupBoundary = setup.indexOf('\nif [[ "${1:-}" == "--update" ]]');
assert.ok(setupBoundary > 0);
const setupFixture = path.join(temporaryRoot, 'setup-functions.sh');
const serviceFixture = path.join(temporaryRoot, 'service-menu.sh');
fs.writeFileSync(setupFixture, setup.slice(0, setupBoundary));
fs.writeFileSync(serviceFixture, service.slice(service.indexOf('show_menu() {'), service.indexOf('\nif [[ "${1:-}"')));

test.after(() => {
    assert.equal(path.dirname(temporaryRoot), path.resolve(os.tmpdir()));
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
});

function run(body, input = '', options = {}) {
    const directory = options.directory || fs.mkdtempSync(path.join(temporaryRoot, 'scenario-'));
    return spawnSync(bash, ['-c', 'set -euo pipefail\nsource "$1"\nexec 3<&0\n' + body, 'deployment-test', (options.service ? serviceFixture : setupFixture).replace(/\\/g, '/')], {
        cwd: repository,
        env: { ...process.env, ECS_DEPLOY_DIR: directory.replace(/\\/g, '/') },
        input,
        encoding: 'utf8',
        timeout: 10000
    });
}

const wizard = [
    'require_root() { :; }',
    'init_terminal() { exec 3<&0; }',
    'persist_self() { :; }',
    'save_state() { echo SAVED; }',
    'install_docker() { echo DOCKER; }',
    'install_native() { echo "NATIVE:$ECS_RECONFIGURE:$APP_PORT"; }'
].join('\n');

function success(result) {
    assert.ifError(result.error);
    assert.equal(result.status, 0, result.stderr + result.stdout);
}

test('Enter installs Docker with defaults, without asking unused HTTP or HTTPS ports', () => {
    const result = run(wizard + '\nmain_menu', '\n'.repeat(6));
    success(result);
    assert.match(result.stdout, /SAVED\nDOCKER/);
    assert.match(result.stdout, /服务器IP:43211/);
    assert.doesNotMatch(result.stderr, /HTTP 对外端口|HTTPS|证书/);
    assert.equal((result.stdout.match(/ECS Controller 部署向导/g) || []).length, 1);
});

test('invalid menu choices retry before using option one', () => {
    const result = run(wizard + '\nmain_menu', 'bad\n' + '\n'.repeat(6));
    success(result);
    assert.match(result.stderr, /无效选项/);
    assert.match(result.stdout, /DOCKER/);
});

test('closed input cancels instead of accepting an install default', () => {
    const result = run(wizard + '\nmain_menu');
    assert.equal(result.status, 130, result.stderr);
    assert.doesNotMatch(result.stdout, /SAVED|DOCKER|NATIVE/);
});

test('closed yes/no input cancels even when called from an if statement', () => {
    const result = run('if yes_no "域名" n; then echo YES; else echo NO; fi\necho CONTINUED');
    assert.equal(result.status, 130, result.stderr);
    assert.doesNotMatch(result.stdout, /YES|NO|CONTINUED/);
});

test('invalid mode, container names, and ports can be corrected without restarting', () => {
    const result = run('load_state\ncollect_config\nprintf "RESULT:%s:%s:%s" "$INSTALL_MODE" "$CONTAINER_NAME" "$APP_PORT"', 'wrong\n1\nBad Name\nvalid-name\nn\n0\n65536\n08\n');
    success(result);
    assert.match(result.stdout, /RESULT:docker:valid-name:8/);
    assert.match(result.stderr, /无效选项/);
    assert.match(result.stderr, /容器名称只能/);
    assert.equal((result.stderr.match(/端口请输入/g) || []).length, 2);
});

test('invalid yes/no answers retry rather than silently choosing no', () => {
    const result = run('if yes_no "域名" n; then echo YES; else echo NO; fi', 'later\nYES\n');
    success(result);
    assert.match(result.stdout, /YES/);
    assert.match(result.stderr, /请输入 y 或 n/);
});

test('a pasted domain URL is normalized and bad domain input can be corrected', () => {
    const result = run('DOMAIN=""\nresult=$(ask_domain)\necho "$result"', 'bad domain\nhttps://ECS.Example.com/path\n');
    success(result);
    assert.equal(result.stdout.trim(), 'ecs.example.com');
    assert.match(result.stderr, /请输入有效域名/);
});

test('HTTP and HTTPS cannot use the same external port', () => {
    const result = run('load_state\ncollect_config\necho "PORTS:$HTTP_PORT:$HTTPS_PORT"', '\n\ny\nexample.com\ny\n443\n443\n8443\n');
    success(result);
    assert.match(result.stdout, /PORTS:443:8443/);
    assert.match(result.stderr, /端口不能相同/);
});

test('existing native configuration is reused by Enter without repeating questions', () => {
    const directory = fs.mkdtempSync(path.join(temporaryRoot, 'saved-'));
    fs.writeFileSync(path.join(directory, '.setup_state'), 'INSTALL_MODE=native\nAPP_PORT=4545\n');
    const result = run(wizard + '\nmain_menu', '\n\n', { directory });
    success(result);
    assert.match(result.stdout, /NATIVE:1:4545/);
    assert.doesNotMatch(result.stdout, /第 1 步/);
    assert.doesNotMatch(result.stderr, /容器名称|是否使用域名/);
});

test('choosing modify allows changing a saved deployment before confirming', () => {
    const directory = fs.mkdtempSync(path.join(temporaryRoot, 'modify-'));
    fs.writeFileSync(path.join(directory, '.setup_state'), 'APP_PORT=4545\n');
    const result = run(wizard + '\nmain_menu', '\n2\n\n\nn\n9898\n\n', { directory });
    success(result);
    assert.match(result.stdout, /服务器IP:9898/);
    assert.match(result.stdout, /DOCKER/);
});

test('canceling at the summary does not save configuration or install services', () => {
    const result = run(wizard + '\nmain_menu', '\n\n\nn\n\n0\n');
    success(result);
    assert.doesNotMatch(result.stdout, /SAVED|DOCKER|NATIVE/);
});

test('a failed Docker install never falls back to a native install', () => {
    const result = run(wizard + '\ninstall_docker() { echo DOCKER_FAILED; return 1; }\nmain_menu', '\n'.repeat(6));
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stdout, /DOCKER_FAILED/);
    assert.doesNotMatch(result.stdout, /NATIVE/);
});

test('native installation receives the selected loopback port and explicit configuration flag', () => {
    const result = run(wizard + '\nmain_menu', '\n2\nn\n4546\n\n');
    success(result);
    assert.match(result.stdout, /NATIVE:1:4546/);
    assert.match(result.stdout, /127.0.0.1:4546/);
    assert.doesNotMatch(result.stdout, /DOCKER/);
});

test('service menu uses option one on Enter and retries invalid choices', () => {
    const result = run('run_service() { echo "ACTION:$1"; }\nshow_menu', 'invalid\n\n', { service: true });
    success(result);
    assert.match(result.stdout, /ACTION:start/);
    assert.match(result.stdout, /无效选项/);
});

test('service menu exits safely on EOF', () => {
    const result = run('run_service() { echo "ACTION:$1"; }\nshow_menu', '', { service: true });
    success(result);
    assert.doesNotMatch(result.stdout, /ACTION:/);
});

const runtimeStart = installer.indexOf('if [ ! -f "$env_file" ]; then');
const runtimeEnd = installer.indexOf('\necho "正在配置 $service_manager 后台服务...', runtimeStart);
assert.ok(runtimeStart > 0 && runtimeEnd > runtimeStart);
const runtimeConfig = installer.slice(runtimeStart, runtimeEnd);

for (const configure of [0, 1]) {
    test('native runtime configuration ' + (configure ? 'applies confirmed port changes and preserves other settings' : 'is preserved during a normal update'), () => {
        const directory = fs.mkdtempSync(path.join(temporaryRoot, 'runtime-'));
        const original = 'TZ=Asia/Shanghai\nECS_DATA_DIR=/saved-data\nECS_HTTP_ADDR=127.0.0.1:43211\nECS_COOKIE_SECURE=0\n';
        fs.writeFileSync(path.join(directory, 'runtime.env'), original);
        const result = run('env_file="$DEPLOY_DIR/runtime.env"\nlisten_addr=127.0.0.1:4546\ncookie_secure=1\nECS_RECONFIGURE=' + configure + '\n' + runtimeConfig, '', { directory });
        success(result);
        const contents = fs.readFileSync(path.join(directory, 'runtime.env'), 'utf8');
        if (configure) {
            assert.match(contents, /ECS_HTTP_ADDR=127.0.0.1:4546/);
            assert.match(contents, /ECS_COOKIE_SECURE=1/);
            assert.match(contents, /ECS_DATA_DIR=\/saved-data/);
            assert.equal((contents.match(/ECS_HTTP_ADDR=/g) || []).length, 1);
        } else {
            assert.equal(contents, original);
        }
    });
}

for (const https of ['n', 'y']) {
    test('Docker domain deployment ' + (https === 'y' ? 'publishes HTTP and HTTPS' : 'does not reserve an unused HTTPS port'), () => {
        const directory = fs.mkdtempSync(path.join(temporaryRoot, 'proxy-'));
        fs.mkdirSync(path.join(directory, 'app'));
        const result = run('load_state\nUSE_DOMAIN=y\nDOMAIN=example.com\nENABLE_HTTPS=' + https + '\nwrite_docker_files', '', { directory });
        success(result);
        const compose = fs.readFileSync(path.join(directory, 'docker-compose.yml'), 'utf8');
        assert.match(compose, /"80:80"/);
        if (https === 'y') assert.match(compose, /"443:443"/);
        else assert.doesNotMatch(compose, /:443"/);
    });
}

'use strict';

// 通过已有浏览器验证真实脚本；所有账号、验证码和任务接口均由本地模拟服务提供。
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require('playwright-core');

const gateway = 'http://chromium-manager:10102';
const panelPath = '/webman/3rdparty/pan-xunlei-com/index.cgi/';
const panelHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<link rel="stylesheet" href="/remote/panel.css"><script defer src="/remote/panel.js"></script>
<style>body{margin:0;font:14px sans-serif}aside{position:fixed;inset:0 auto 0 0;width:200px;background:#f7f8fa}
nav{padding:30px 10px}nav button{display:block;width:180px;height:42px;border:0;background:transparent;text-align:left}
main{margin-left:200px;padding:30px}.active{background:#e7eefb;color:#3b83fa}</style></head>
<body><aside><nav><button class="active" id="native-home"><span>下载列表</span></button>
<button id="native-cloud"><span>云盘</span></button></nav></aside><main id="native-content">原下载列表</main>
<script>
let nativeRoute = location.hash.split('?')[0] || '#/home';
let nativeQuery = location.hash.includes('?') ? '?' + location.hash.split('?')[1] : '';
window.addEventListener('hashchange', () => { nativeQuery = location.hash.includes('?') ? '?' + location.hash.split('?')[1] : ''; });
document.getElementById('native-home').onclick = () => {
  if (nativeRoute === '#/home') return;
  nativeRoute = '#/home'; location.hash = nativeRoute;
  document.getElementById('native-content').textContent = '原下载列表';
};
document.getElementById('native-cloud').onclick = () => {
  nativeRoute = '#/cloud';
  Promise.resolve().then(() => { location.hash = nativeRoute + nativeQuery; });
  document.getElementById('native-content').textContent = '原云盘';
};
</script></body></html>`;

let state, progress = 20, phase = 'PHASE_TYPE_RUNNING', taskCalls = 0, actionCalls = 0, smsCalls = 0;
let taskGate = null, smsGate = null;
function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}
async function waitForRequest(check, message) {
  const deadline = Date.now() + 5000;
  while (!check() && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(check(), message);
}
function account(loggedIn) {
  return { logged_in: loggedIn, username: '测试账号', review_required: !loggedIn, mobile: '138****0000', target: loggedIn ? '设备一' : '', target_name: '测试下载设备', directory: '/downloads' };
}
state = account(false);

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    const json = (data, code = 200) => {
      res.writeHead(code, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
      res.end(JSON.stringify(data));
    };
    if (url.pathname === panelPath) { res.setHeader('Content-Type', 'text/html; charset=utf-8'); res.end(panelHTML); return; }
    if (url.pathname.startsWith('/remote/')) {
      const name = path.basename(url.pathname);
      res.setHeader('Content-Type', name.endsWith('.html') ? 'text/html; charset=utf-8' : name.endsWith('.css') ? 'text/css' : 'text/javascript');
      res.end(await fs.readFile(path.join(__dirname, 'static', name))); return;
    }
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {};
    if (url.pathname === '/api/remote/status') { json(state); return; }
    if (url.pathname === '/api/remote/login') {
      state = { ...account(false), username: body.username };
      json({ code: 'review_required', message: '本次登录需要短信验证' }, 422); return;
    }
    if (url.pathname === '/api/remote/review/send') {
      smsCalls++;
      if (smsGate) await smsGate.promise;
      json({ ok: true }); return;
    }
    if (url.pathname === '/api/remote/review/check') {
      json({ code: 'invalid_request', message: '验证码错误，请重试' }, 400); return;
    }
    if (url.pathname === '/api/remote/tasks') {
      taskCalls++;
      const second = url.searchParams.get('page_token') === 'second';
      const data = { tasks: [{ id: second ? '任务二' : '任务一', name: second ? '第二页测试文件.zip' : '测试下载文件.zip', phase, progress, size: 104857600, done: 1048576 * progress, speed: 1048576 }], next_page_token: second ? '' : 'second' };
      const gate = taskGate; taskGate = null;
      if (gate) await gate.promise;
      json(data); return;
    }
    if (/\/api\/remote\/tasks\/.+\/pause$/.test(url.pathname)) {
      actionCalls++; phase = 'PHASE_TYPE_PAUSED'; json({ ok: true }); return;
    }
    json({ message: '模拟接口不存在' }, 404);
  } catch (error) { res.writeHead(500); res.end(String(error)); }
});

(async () => {
  let browser, page;
  const errors = [];
  try {
    const acquired = await fetch(gateway + '/agent/acquire', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'B' }) });
    assert.ok(acquired.ok, '未能获取浏览器');
    await new Promise((resolve) => server.listen(0, '0.0.0.0', resolve));
    const origin = 'http://dev-box:' + server.address().port;
    browser = await chromium.connectOverCDP(gateway + '/cdp/B');
    page = await browser.contexts()[0].newPage();
    page.on('pageerror', (error) => errors.push(error.message));
    await page.setViewportSize({ width: 1280, height: 850 });
    await page.goto(origin + panelPath + '#/home?keep=1');
    const menu = page.locator('[data-xlp-remote-menu]');
    await menu.click();
    const frameElement = page.locator('#xlp-remote-view');
    await frameElement.waitFor({ state: 'visible' });
    await page.locator('#native-home span').click();
    await frameElement.waitFor({ state: 'hidden' });
    assert.equal(new URL(page.url()).hash, '#/home?keep=1', '原菜单没有恢复当前路由或丢失其他参数');
    await menu.click();
    await page.locator('#native-cloud span').click();
    await frameElement.waitFor({ state: 'hidden' });
    assert.ok(page.url().includes('#/cloud?keep=1'), '跨路由切换失效');
    await menu.click();
    await page.reload();
    await frameElement.waitFor({ state: 'visible' });
    await page.locator('#native-cloud').focus();
    await page.keyboard.press('Enter');
    await frameElement.waitFor({ state: 'hidden' });
    await page.setViewportSize({ width: 760, height: 850 });
    await menu.click();
    await page.locator('#native-home').click();
    await frameElement.waitFor({ state: 'hidden' });
    await page.setViewportSize({ width: 1280, height: 850 });
    await menu.click();
    await page.goBack();
    await frameElement.waitFor({ state: 'hidden' });
    await page.goForward();
    await frameElement.waitFor({ state: 'visible' });
    console.log('通过：侧栏同路由、跨路由、键盘、窄屏、刷新恢复及历史导航');

    await page.goto(origin + '/remote/view.html');
    await page.locator('#account-button').click();
    assert.equal(await page.locator('#review-form').isVisible(), false, '首次打开显示了历史验证');
    const login = async (username) => {
      await page.locator('#username').fill(username);
      await page.locator('#password').fill('测试密码');
      await page.locator('#login-submit').click();
      await page.locator('#review-form').waitFor({ state: 'visible' });
    };
    await login('账号一');
    await page.locator('#send-sms').click();
    await page.waitForFunction(() => document.getElementById('send-sms').textContent.includes('秒后重发'));
    await page.locator('#sms-code').fill('123456');
    await page.locator('#review-submit').click();
    await page.waitForFunction(() => document.getElementById('login-message').textContent.includes('验证码错误'));
    await page.getByRole('button', { name: '关闭登录', exact: true }).click();
    await page.locator('#account-button').click();
    assert.equal(await page.locator('#review-form').isVisible(), false, '关闭重开仍显示上次短信验证');
    await page.evaluate(() => loadStatus());
    assert.equal(await page.locator('#review-form').isVisible(), false, '状态刷新重新显示历史验证');
    assert.equal(await page.locator('#sms-code').inputValue(), '');
    assert.equal(await page.locator('#send-sms').textContent(), '获取验证码');
    assert.equal(await page.locator('#password').inputValue(), '');
    const screenshotDir = path.resolve(__dirname, '../../artifacts/remoteweb');
    await fs.mkdir(screenshotDir, { recursive: true });
    await page.screenshot({ path: path.join(screenshotDir, '登录重新打开.png') });
    await login('账号一');
    await page.locator('#username').fill('账号二');
    assert.equal(await page.locator('#review-form').isVisible(), false, '换账号仍显示旧验证');
    await login('账号二');
    const delayedSMS = deferred(); smsGate = delayedSMS;
    const beforeSMS = smsCalls;
    await page.locator('#send-sms').click();
    await page.waitForFunction(() => document.getElementById('send-sms').disabled);
    await waitForRequest(() => smsCalls > beforeSMS, '未发送短信请求');
    await page.getByRole('button', { name: '关闭登录', exact: true }).click();
    await page.locator('#account-button').click();
    const smsResponse = page.waitForResponse((response) => response.url().endsWith('/review/send'));
    delayedSMS.resolve(); smsGate = null; await smsResponse;
    await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 50)));
    assert.equal(await page.locator('#review-form').isVisible(), false);
    assert.equal(await page.locator('#send-sms').textContent(), '获取验证码', '旧短信响应恢复了倒计时');
    assert.equal(await page.locator('#login-message').textContent(), '', '旧请求覆盖新登录提示');
    console.log('通过：登录失败后重开、换账号、清理倒计时和忽略旧短信响应');

    state = account(true);
    await page.clock.install();
    await page.goto(origin + '/remote/view.html');
    await page.waitForFunction(() => document.querySelector('progress')?.value === 20);
    progress = 35;
    await page.locator('[data-filter="active"]').click();
    await page.waitForFunction(() => document.querySelector('progress')?.value === 35);
    progress = 45;
    await page.locator('#refresh-button').click();
    await page.waitForFunction(() => document.querySelector('progress')?.value === 45);
    await page.locator('#load-more').click();
    await page.waitForFunction(() => document.querySelectorAll('.task').length === 2);
    progress = 60;
    await page.clock.runFor(15000);
    await page.waitForFunction(() => [...document.querySelectorAll('progress')].length === 2 && [...document.querySelectorAll('progress')].every((node) => node.value === 60));
    await page.screenshot({ path: path.join(screenshotDir, '分页任务进度.png') });
    console.log('通过：分类切换、手动刷新和多页任务自动刷新');

    const delayedTasks = deferred(); taskGate = delayedTasks;
    const beforeTasks = taskCalls;
    await page.clock.runFor(15000);
    await waitForRequest(() => taskCalls > beforeTasks, '未发起定时刷新');
    await page.locator('.task').last().getByRole('button', { name: '暂停', exact: true }).click();
    await page.waitForFunction(() => document.getElementById('notice').textContent.includes('操作已提交'));
    delayedTasks.resolve();
    await page.waitForFunction(() => document.getElementById('notice').hidden);
    await page.locator('[data-filter="paused"]').click();
    await page.waitForFunction(() => document.querySelectorAll('.task-meta .paused').length === 2);
    assert.equal(actionCalls, 1, '暂停操作重复提交');
    assert.equal(await page.locator('#notice').isVisible(), false, '刷新后操作提示未清除');
    await page.evaluate(() => notice('操作已提交，正在更新任务状态', true));
    await page.clock.runFor(5100);
    assert.equal(await page.locator('#notice').isVisible(), false, '成功提示未自动消失');
    await page.evaluate(() => { notice('临时成功提示', true); notice('测试刷新错误'); });
    await page.clock.runFor(5100);
    assert.equal(await page.locator('#notice').textContent(), '测试刷新错误', '旧计时器清除了新错误');
    await page.reload();
    assert.equal(await page.locator('#notice').isVisible(), false, '刷新页面保留了旧提示');
    assert.deepEqual(errors, [], '浏览器出现脚本错误');
    console.log('通过：暂停与刷新并发、刷新后清除提示、提示自动消失和保留新错误');
  } finally {
    taskGate?.resolve(); smsGate?.resolve();
    if (page) await page.close();
    if (browser) await browser.close();
    server.closeAllConnections(); server.close();
    await fetch(gateway + '/agent/release', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'B' }) });
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });

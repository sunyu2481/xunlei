'use strict';

const assert = require('node:assert/strict');
const nodeTest = require('node:test');
const { createApp, account, taskPage, settle } = require('./test_helpers.cjs');
const test = (name, body) => nodeTest(name, { timeout: 2000 }, body);

for (const failure of [false, true]) {
  test('目录导航忽略旧' + (failure ? '错误' : '成功') + '响应及收尾', async () => {
    const app = await createApp();
    const first = app.run("openFolders('/旧目录', () => {})");
    const old = app.take('/folders');
    const root = app.node('folder-root').emit('click');
    app.take('/folders').respond([{ name: '新目录', path: '/新目录' }]);
    await root;
    if (failure) old.respond({ message: '旧目录错误' }, 502); else old.respond([{ name: '旧子目录', path: '/旧目录/子目录' }]);
    await first;
    assert.equal(app.node('folder-path').textContent, '设备目录');
    assert.equal(app.node('folder-select').disabled, true);
    assert.equal(app.node('folder-back').disabled, true);
    assert.equal(app.node('folder-message').textContent, '');
    assert.equal(app.node('folders').children[0].children[1].textContent, '新目录');
  });
}

test('目录关闭重开后旧响应不能启用按钮或覆盖选择回调', async () => {
  const app = await createApp();
  const first = app.run("openFolders('/旧目录', () => { throw new Error('不应选择旧目录'); })");
  const old = app.take('/folders');
  app.node('folder-dialog').close(); await app.node('folder-dialog').closed;
  const second = app.run("openFolders('/新目录', (directory) => { document.getElementById('create-directory').value = directory; })");
  const current = app.take('/folders');
  old.respond([]); await first;
  assert.equal(app.node('folder-select').disabled, true);
  current.respond([]); await second;
  await app.node('folder-select').emit('click');
  assert.equal(app.node('create-directory').value, '/新目录');
});

test('切换设备使旧目录失效且保存回调绑定打开时的目标', async () => {
  const app = await createApp();
  const pending = app.node('directory-button').emit('click');
  const old = app.take('/folders');
  await app.status(account('设备乙'));
  old.respond([]); await pending;
  assert.equal(app.node('folder-dialog').open, false);
  await app.node('folder-select').emit('click');
  assert.equal(app.requests.filter((request) => request.method === 'PUT').length, 0);
  assert.equal(app.node('folder-dialog').open, false);

  const next = app.node('directory-button').emit('click');
  app.take('/folders').respond([]); await next;
  const saving = app.node('folder-select').emit('click');
  const settings = app.take('/settings');
  assert.deepEqual(JSON.parse(settings.body), { target: '设备乙', directory: '/下载 目录/' });
  settings.respond({}); await settle();
  app.take('/status').respond(account('设备乙')); await saving;
});

for (const failure of [false, true]) {
  test('状态刷新忽略旧' + (failure ? '错误' : '成功') + '响应', async () => {
    const app = await createApp();
    const first = app.node('refresh-button').emit('click');
    const old = app.take('/status');
    await app.status(account('设备乙', '新账号'));
    if (failure) old.fail(); else old.respond(account());
    await settle();
    assert.equal(app.node('account-name').textContent, '新账号');
    assert.equal(app.node('device-name').textContent, '设备乙');
    assert.equal(app.node('notice').textContent, '');
    assert.equal(app.requests.filter((request) => !request.taken).length, 0, '已失效的状态刷新不应继续任务查询');
    await first;
  });
}

for (const failure of [false, true]) {
  test('旧会话任务' + (failure ? '错误' : '成功') + '及收尾不影响新会话刷新', async () => {
    const app = await createApp();
    const first = app.run('refreshTasks()');
    const old = app.take('/tasks');
    await app.status(account('设备乙', '新账号'));
    app.run('openCreate()');
    const second = app.run('refreshTasks()');
    const current = app.take('/tasks');
    if (failure) old.respond({ code: 'login_required', message: '旧账号登录失效' }, 422); else old.respond(taskPage('旧任务'));
    await first;
    assert.equal(app.node('create-dialog').open, true);
    assert.equal(app.node('login-dialog').open, false);
    assert.equal(app.node('notice').textContent, '');
    assert.equal(app.node('refresh-button').disabled, true, '旧请求不得解除新请求的忙碌状态');
    current.respond(taskPage('新任务')); await second;
    assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '新任务');
    assert.equal(app.node('refresh-button').disabled, false);
  });
}

test('当前任务认证错误重新核对已登录状态后保留创建窗口', async () => {
  const app = await createApp();
  app.run('openCreate()');
  const pending = app.run('refreshTasks()');
  app.take('/tasks').respond({ code: 'login_required', message: '登录已恢复' }, 422);
  await settle(); app.take('/status').respond(account()); await pending;
  assert.equal(app.node('create-dialog').open, true);
  assert.equal(app.node('login-dialog').open, false);
});

test('当前任务认证错误确认退出后仍打开登录窗口', async () => {
  const app = await createApp();
  app.run('openCreate()');
  const pending = app.run('refreshTasks()');
  app.take('/tasks').respond({ code: 'login_required', message: '请重新登录' }, 422);
  await settle(); app.take('/status').respond({ ...account(), logged_in: false }); await pending;
  assert.equal(app.node('create-dialog').open, false);
  assert.equal(app.node('login-dialog').open, true);
  assert.equal(app.node('login-message').textContent, '请重新登录');
});

for (const failure of [false, true]) {
  test('隐藏页签取消排队分页并忽略旧' + (failure ? '错误' : '成功'), async () => {
    const app = await createApp({ embedded: true });
    await app.visible(true);
    app.take('/status').respond(account()); await settle();
    app.take('/tasks').respond(taskPage('第一页', '第二页')); await settle();
    const more = app.run('refreshTasks(true)');
    app.take('/tasks').respond(taskPage('第二页')); await more;
    const pending = app.run('refreshTasks()');
    const old = app.take('/tasks');
    await app.filters[1].emit('click');
    await app.visible(false);
    if (failure) old.respond({ code: 'login_required', message: '隐藏后的旧错误' }, 422); else old.respond(taskPage('过期第一页', '第二页'));
    await settle();
    assert.equal(app.requests.filter((request) => !request.taken).length, 0, '隐藏后不应继续分页、排队或状态查询');
    await pending;
    await app.tick(15000);
    await app.run('refreshTasks(true)');
    assert.equal(app.requests.filter((request) => !request.taken).length, 0, '隐藏后不应继续分页、排队或定时查询');
    assert.equal(old.signal.aborted, true, '隐藏时应取消只读请求');
    assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '第一页');
    assert.equal(app.node('notice').textContent, '');
    assert.equal(app.node('login-dialog').open, false);
  });
}

test('重新显示立即刷新且旧请求收尾不能清除新刷新队列', async () => {
  const app = await createApp({ embedded: true });
  await app.visible(true);
  app.take('/status').respond(account()); await settle();
  const old = app.take('/tasks');
  await app.visible(false);
  await app.visible(true);
  app.take('/status').respond(account()); await settle();
  const current = app.take('/tasks');
  await app.run('refreshTasks()');
  old.respond(taskPage('旧任务')); await settle();
  assert.equal(app.node('refresh-button').disabled, true);
  current.respond(taskPage('新任务')); await settle();
  app.take('/tasks').respond(taskPage('最终任务')); await settle();
  assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '最终任务');
  assert.equal(app.node('refresh-button').disabled, false);
});

test('浏览器页面隐藏和恢复沿用刷新生命周期', async () => {
  const app = await createApp();
  const pending = app.run('refreshTasks()');
  const old = app.take('/tasks');
  await app.hidden(true);
  old.respond(taskPage('隐藏旧任务')); await pending;
  await app.tick(15000);
  assert.equal(app.requests.filter((request) => !request.taken).length, 0);
  assert.equal(old.signal.aborted, true);
  await app.hidden(false);
  app.take('/status').respond(account()); await settle();
  app.take('/tasks').respond(taskPage('恢复任务')); await settle();
  assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '恢复任务');
});

test('正常多页刷新保留已加载页数、去重和校验进度', async () => {
  const app = await createApp();
  const first = app.run('refreshTasks()');
  app.take('/tasks').respond(taskPage('任务甲', '第二页')); await first;
  const more = app.run('refreshTasks(true)');
  app.take('/tasks').respond(taskPage('任务乙')); await more;
  const refreshing = app.run('refreshTasks()');
  app.take('/tasks').respond(taskPage('任务甲', '第二页')); await settle();
  const next = app.take('/tasks');
  assert.equal(next.url.searchParams.get('page_token'), '第二页');
  next.respond({ tasks: [...taskPage('任务甲').tasks, ...taskPage('任务乙').tasks], next_page_token: '' }); await refreshing;
  assert.equal(app.node('task-list').children.length, 2);
  assert.equal(app.node('task-list').children[0].children[1].children[1].value, 40);
  assert.equal(app.node('load-more').hidden, true);
});

test('最新状态查询失败时不接受更早的成功状态', async () => {
  const app = await createApp();
  const first = app.node('refresh-button').emit('click'), old = app.take('/status');
  const second = app.node('refresh-button').emit('click');
  app.take('/status').respond({ message: '本次查询失败' }, 502); await second;
  old.respond(account('旧设备', '旧账号')); await first;
  assert.equal(app.node('device-name').textContent, '设备甲');
  assert.equal(app.node('notice').textContent, '本次查询失败');
  assert.equal(app.requests.filter((request) => !request.taken).length, 0);
});

for (const failure of [false, true]) {
  test('状态初始化期间隐藏后恢复忽略旧' + (failure ? '错误' : '成功'), async () => {
    const app = await createApp({ initialize: false });
    const old = app.take('/status');
    await app.hidden(true);
    await app.hidden(false);
    app.take('/status').respond(account('设备乙', '新账号')); await settle();
    app.take('/tasks').respond(taskPage('新任务')); await settle();
    if (failure) old.fail(); else old.respond(account());
    await settle();
    assert.equal(old.signal.aborted, true);
    assert.equal(app.node('account-name').textContent, '新账号');
    assert.equal(app.node('notice').textContent, '');
    assert.equal(app.requests.filter((request) => !request.taken).length, 0);
  });
}

test('认证错误复核期间新状态优先且不打断新弹窗', async () => {
  const app = await createApp();
  const pending = app.run('refreshTasks()');
  app.take('/tasks').respond({ code: 'login_required', message: '旧登录错误' }, 422); await settle();
  const old = app.take('/status');
  await app.status(account('设备乙', '新账号'));
  app.run("openCreate(); notice('新提示', true)");
  old.respond({ ...account(), logged_in: false }); await pending;
  assert.equal(app.node('create-dialog').open, true);
  assert.equal(app.node('login-dialog').open, false);
  assert.equal(app.node('notice').textContent, '新提示');
});

test('认证错误复核网络失败时保留现有窗口和错误', async () => {
  const app = await createApp();
  app.run('openCreate()');
  const pending = app.run('refreshTasks()');
  app.take('/tasks').respond({ code: 'login_required', message: '需要检查登录状态' }, 422); await settle();
  app.take('/status').fail(); await pending;
  assert.equal(app.node('create-dialog').open, true);
  assert.equal(app.node('login-dialog').open, false);
  assert.equal(app.node('notice').textContent, '需要检查登录状态');
  assert.equal(app.node('refresh-button').disabled, false);
});

for (const failure of [false, true]) {
  test('设备窗口重开忽略旧' + (failure ? '认证错误' : '成功') + '与收尾', async () => {
    const app = await createApp();
    const first = app.run('openDevices()'), old = app.take('/devices');
    app.node('device-dialog').close(); await app.node('device-dialog').closed;
    const second = app.run('openDevices()'), current = app.take('/devices');
    if (failure) old.respond({ code: 'login_required', message: '旧设备查询错误' }, 422); else old.respond([{ target: '旧设备', name: '旧设备' }]);
    await first;
    assert.equal(app.node('device-save').disabled, true);
    assert.equal(app.node('reload-devices').disabled, true);
    assert.equal(app.requests.filter((request) => !request.taken).length, 0);
    current.respond([{ target: '设备甲', name: '设备甲' }]); await second;
    assert.equal(app.node('device-select').children[1].textContent, '设备甲');
    assert.equal(app.node('device-save').disabled, false);
    assert.equal(app.node('reload-devices').disabled, false);
  });
}

test('目录选择完成时不关闭重新打开的窗口或启用新导航按钮', async () => {
  const app = await createApp();
  const first = app.run("openFolders('/旧目录', () => new Promise((resolve) => { globalThis.finishSelection = resolve; }))");
  app.take('/folders').respond([]); await first;
  const selecting = app.node('folder-select').emit('click');
  app.node('folder-dialog').close(); await app.node('folder-dialog').closed;
  const second = app.run("openFolders('/新目录', () => {})"), current = app.take('/folders');
  app.run('finishSelection()'); await selecting;
  assert.equal(app.node('folder-dialog').open, true);
  assert.equal(app.node('folder-select').disabled, true);
  current.respond([]); await second;
  assert.equal(app.node('folder-path').textContent, '/新目录');
});

test('迟到的弹窗关闭事件不取消重开后的目录查询', async () => {
  const app = await createApp();
  const pending = app.run("openFolders('/新目录', () => {})"), current = app.take('/folders');
  await app.node('folder-dialog').emit('close');
  current.respond([]); await pending;
  assert.equal(current.signal.aborted, false);
  assert.equal(app.node('folder-select').disabled, false);
});

test('分页中途失败保留原任务并允许后续刷新', async () => {
  const app = await createApp();
  const first = app.run('refreshTasks()');
  app.take('/tasks').respond(taskPage('任务甲', '第二页')); await first;
  const more = app.run('refreshTasks(true)');
  app.take('/tasks').respond(taskPage('任务乙')); await more;
  const pending = app.run('refreshTasks()');
  app.take('/tasks').respond(taskPage('尚未完成的新任务', '第二页')); await settle();
  app.take('/tasks').fail(); await pending;
  assert.equal(app.node('task-list').children.length, 2);
  assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '任务甲');
  assert.match(app.node('notice').textContent, /网络连接/);
  assert.equal(app.node('refresh-button').disabled, false);
  const retry = app.run('refreshTasks()');
  app.take('/tasks').respond(taskPage('重试任务')); await retry;
  assert.equal(app.node('task-list').children[0].children[1].children[0].textContent, '重试任务');
});

const submissionStorageKey = 'xlp-remote-submissions';
const submissionLinks = (count, offset = 0) => Array.from({ length: count }, (_, index) => 'https://files.example.test/下载-' + (index + offset));
const magnet = 'magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
function preview(url = magnet, version = '相同内容', kind = 'files') {
  const files = kind === 'single' ? [{ id: '单文件', parent_id: '', name: '唯一.mp4', is_dir: false, size: '100' }] : [
    { id: '根', parent_id: '', name: '资料包', is_dir: true, size: '1000' },
    { id: '零', parent_id: '根', name: '影片.mp4', is_dir: false, index: 0, size: '100' },
    { id: '子目录', parent_id: '根', name: '子目录', is_dir: true, size: '500' },
    { id: '二', parent_id: '子目录', name: '剧集.mkv', is_dir: false, index: 2, size: '200' },
    { id: '三', parent_id: '子目录', name: '字幕.srt', is_dir: false, index: 3, size: '300' },
    { id: '五', parent_id: '根', name: '说明.txt', is_dir: false, index: 5, size: '400' },
    { id: '空', parent_id: '根', name: '空目录', is_dir: true, size: '0' },
  ];
  return { preview_id: '预览编号', resource_version: version, target: account().target, url, kind, name: '资料包', complete: true, total_file_count: kind === 'single' ? 1 : 4, total_size: kind === 'single' ? '100' : '1000', expires_at: new Date(Date.now() + 600000).toISOString(), files };
}
const descendants = (node) => node.children.flatMap((n) => [n, ...descendants(n)]);
const checkbox = (app, name) => descendants(app.node('create-results')).find((n) => n.type === 'checkbox' && n.attributes.get('aria-label') === name);
const tool = (app, label) => descendants(app.node('create-results')).find((n) => n.tagName === 'BUTTON' && n.textContent === label);
const records = (app) => JSON.parse(app.run('JSON.stringify(Object.values(submissions))'));
async function parseLinks(app, links, response = (url) => preview(url, '相同内容', 'single')) {
  app.node('links').value = links.join('\n'); await app.node('links').emit('input');
  const pending = app.node('resolve-links').emit('click');
  for (;;) {
    await settle();
    const r = app.requests.find((r) => !r.taken && r.url.pathname === '/api/remote/resources/resolve');
    if (!r) break;
    const request = app.take('/resources/resolve'), result = response(JSON.parse(request.body).url);
    request.respond(result, result.code ? 422 : 200);
  }
  await pending;
}
async function submitChosen(app, response = { code: 'submission_unknown', message: '结果未知' }) {
  const pending = app.node('create-form').emit('submit'), submitted = [];
  for (;;) {
    await settle();
    if (!app.requests.some((r) => !r.taken && r.url.pathname === '/api/remote/tasks')) break;
    const request = app.take('/tasks');
    if (request.method === 'GET') request.respond(taskPage());
    else { submitted.push(JSON.parse(request.body)); request.respond(response, response.code ? 409 : 200); }
  }
  await pending; return submitted;
}
async function checkOriginal(app, id, result = { status: 'unknown' }) {
  const pending = app.run('checkRecord(Object.values(submissions).find((r) => r.id === ' + JSON.stringify(id) + '), document.createElement("button"))');
  const request = app.take('/submissions/' + id); assert.equal(request.method, 'GET'); request.respond(result); await pending;
}

test('解析完整文件树和零选择都不会创建任务', async () => {
  const app = await createApp(); app.run('openCreate()');
  await parseLinks(app, [magnet], () => preview());
  assert.equal(app.requests.filter((r) => r.method === 'POST' && r.url.pathname.endsWith('/tasks')).length, 0);
  assert.equal(checkbox(app, '影片.mp4').checked, false, '磁力不得默认全选');
  assert.equal(checkbox(app, '空目录').disabled, true);
  assert.equal((await submitChosen(app)).length, 0);
  assert.match(app.node('selection-summary').textContent, /将提交 0 条/);
});

test('目录三态和视频选择只影响真实叶子索引', async () => {
  const app = await createApp(); app.run('openCreate()'); await parseLinks(app, [magnet], () => preview());
  let box = checkbox(app, '影片.mp4'); box.checked = true; await box.emit('change');
  assert.equal(checkbox(app, '资料包').indeterminate, true);
  box = checkbox(app, '子目录'); box.checked = true; await box.emit('change');
  assert.equal(checkbox(app, '子目录').checked, true);
  await tool(app, '取消全选').emit('click'); assert.equal(checkbox(app, '资料包').checked, false);
  await tool(app, '全选').emit('click'); assert.equal(checkbox(app, '资料包').checked, true);
  await tool(app, '仅选视频').emit('click');
  assert.equal(checkbox(app, '字幕.srt').checked, false);
  const sort = descendants(app.node('create-results')).find((n) => n.tagName === 'SELECT'); sort.value = 'size'; await sort.emit('change');
  const requests = await submitChosen(app, { id: '已创建任务' });
  assert.deepEqual(requests[0].file_indices, [0, 2]); assert.equal(requests[0].url, magnet);
  assert.equal(requests[0].directory, account().directory); assert.equal(requests[0].selection_mode, 'files');
});

test('批量解析部分失败时成功项选择保持并显示跳过数量', async () => {
  const app = await createApp(); app.run('openCreate()');
  await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click');
  await parseLinks(app, [magnet, 'https://example.test/失败'], () => ({ code: 'resource_unavailable', message: '解析失败' }));
  assert.equal(checkbox(app, '影片.mp4').checked, true);
  assert.match(app.node('selection-summary').textContent, /将提交 1 条，跳过 1 条/);
  const requests = await submitChosen(app, { id: '任务' }); assert.equal(requests.length, 1);
  assert.match(app.node('create-message').textContent, /跳过 1 条/);
});

for (const outcome of ['成功', '错误']) test('取消并关闭重开忽略旧解析' + outcome + '及收尾', async () => {
  const app = await createApp(); app.run('openCreate()'); app.node('links').value = magnet;
  const old = app.node('resolve-links').emit('click'), request = app.take('/resources/resolve');
  app.node('create-dialog').close(); await app.node('create-dialog').closed; assert.equal(request.signal.aborted, true);
  app.run('openCreate()'); const current = app.node('resolve-links').emit('click'), latest = app.take('/resources/resolve');
  if (outcome === '成功') request.respond(preview()); else request.fail(); await old;
  assert.equal(app.node('resolve-links').disabled, true);
  latest.respond(preview()); await current;
  assert.equal(app.node('resolve-links').disabled, false); assert.ok(checkbox(app, '影片.mp4'));
  assert.equal(records(app).length, 0);
});

test('编辑单条链接和设备切换使旧解析失效', async () => {
  const app = await createApp(); app.run('openCreate()');
  await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click');
  app.node('links').value += '\nhttps://example.test/旧'; await app.node('links').emit('input');
  const pending = app.node('resolve-links').emit('click'), old = app.take('/resources/resolve');
  const input = app.node('create-results').children[1].children[0]; input.value = 'https://example.test/新'; await input.emit('input');
  old.respond(preview('https://example.test/旧')); await pending;
  assert.equal(checkbox(app, '影片.mp4').checked, true);
  const next = app.node('resolve-links').emit('click'), late = app.take('/resources/resolve');
  await app.status(account('设备乙')); late.respond(preview()); await next;
  assert.equal(app.node('create-dialog').open, false); assert.equal(app.node('create-results').children.length, 0);
});

test('创建按钮连点只下发一次且选择被冻结', async () => {
  const app = await createApp(); app.run('openCreate()'); await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click');
  const pending = app.node('create-form').emit('submit'), request = app.take('/tasks');
  assert.equal(checkbox(app, '影片.mp4').disabled, true);
  await app.node('create-form').emit('submit');
  assert.equal(app.requests.filter((r) => r.method === 'POST' && r.url.pathname.endsWith('/tasks')).length, 1);
  let blocked = false; await app.node('create-dialog').emit('cancel', { preventDefault() { blocked = true; } }); assert.equal(blocked, true);
  request.fail(); await settle(); app.take('/tasks').respond(taskPage()); await pending;
  assert.equal(records(app)[0].state, 'unknown');
});

test('刷新未知项后只检查原编号，记录缺失也不能自动重发', async () => {
  const storage = new Map(), app = await createApp({ storage }); app.run('openCreate()');
  await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click');
  const sent = await submitChosen(app); app.node('create-dialog').close(); await app.node('create-dialog').closed;
  const restored = await createApp({ storage }); restored.run('openCreate()');
  await checkOriginal(restored, sent[0].request_id, { status: 'missing' });
  assert.equal(records(restored)[0].state, 'missing');
  await parseLinks(restored, [magnet], () => preview()); await tool(restored, '仅选视频').emit('click');
  assert.equal((await submitChosen(restored)).length, 0);
  assert.equal(restored.requests.filter((r) => r.method === 'POST' && r.url.pathname.endsWith('/tasks')).length, 0);
});

test('旧存储迁移保留超量未知编号且只核对', async () => {
  const links = submissionLinks(150), state = account();
  const old = Object.fromEntries(links.map((link, index) => [JSON.stringify([state.target, state.directory, link]), { id: index.toString(16).padStart(32, '0'), done: false }]));
  const storage = new Map([[submissionStorageKey, JSON.stringify(old)]]), app = await createApp({ storage }); app.run('openCreate()');
  assert.equal(records(app).length, 150);
  await checkOriginal(app, Object.values(old)[149].id); assert.equal(records(app).length, 150);
  await parseLinks(app, [submissionLinks(1, 150)[0]]); assert.equal((await submitChosen(app)).length, 0);
  const migrated = JSON.parse(storage.get(submissionStorageKey)); assert.equal(migrated.version, 2); assert.equal(Object.keys(migrated.records).length, 150);
  assert.equal(app.requests.filter((r) => r.method === 'POST' && r.url.pathname.endsWith('/tasks')).length, 0);
});

test('一百条未知记录容量满仍检查已有编号，关闭只释放成功项', async () => {
  const storage = new Map(), app = await createApp({ storage }), links = submissionLinks(100); app.run('openCreate()');
  await parseLinks(app, links.slice(0, 50)); const first = await submitChosen(app);
  await parseLinks(app, links.slice(50)); await submitChosen(app); assert.equal(records(app).length, 100);
  await parseLinks(app, submissionLinks(1, 100)); assert.equal((await submitChosen(app)).length, 0);
  await checkOriginal(app, first[0].request_id, { status: 'confirmed', task_id: '找到原任务' }); assert.equal(records(app).length, 100);
  app.node('create-dialog').close(); await app.node('create-dialog').closed; assert.equal(records(app).length, 99);
  app.run('openCreate()'); await parseLinks(app, submissionLinks(1, 100)); assert.equal((await submitChosen(app)).length, 1);
  assert.equal(records(app).length, 100); await checkOriginal(app, first[1].request_id);
});

test('内容相同恢复草稿选择，内容变化清空旧选择', async () => {
  const storage = new Map(), app = await createApp({ storage }); app.run('openCreate()'); await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click');
  const same = await createApp({ storage }); same.run('openCreate()'); await parseLinks(same, [magnet], () => preview());
  assert.equal(checkbox(same, '影片.mp4').checked, true); assert.equal(checkbox(same, '剧集.mkv').checked, true);
  const changed = await createApp({ storage }); changed.run('openCreate()'); await parseLinks(changed, [magnet], () => preview(magnet, '新的内容'));
  assert.equal(checkbox(changed, '影片.mp4').checked, false); assert.match(changed.node('create-results').children[0].children[1].textContent, /内容已变化/);
});

test('预览过期和存储不可用都不会绕过提交保护', async () => {
  const storage = { get() {}, set() { throw new Error('存储不可用'); } }, app = await createApp({ storage }); app.run('openCreate()');
  await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click'); assert.match(app.node('selection-summary').textContent, /存储不可用/);
  app.run('entries[0].preview.expires_at = new Date(0).toISOString()'); await app.tick(1000);
  assert.equal(app.node('create-submit').disabled, true); assert.equal((await submitChosen(app)).length, 0);
  await parseLinks(app, [magnet], () => preview()); await tool(app, '仅选视频').emit('click'); const sent = await submitChosen(app);
  await parseLinks(app, [magnet], () => preview()); assert.equal((await submitChosen(app)).length, 0); await checkOriginal(app, sent[0].request_id);
});

test('旧迅雷包装链接和目录尾斜线迁移后仍只核对', async () => {
  const link = 'thunder://' + Buffer.from('AA' + magnet + 'ZZ').toString('base64'), state = account();
  const id = '0123456789abcdef0123456789abcdef';
  const old = { [JSON.stringify([state.target, state.directory.replace(/\/$/, ''), link])]: { id, done: false } };
  const storage = new Map([[submissionStorageKey, JSON.stringify(old)]]), app = await createApp({ storage }); app.run('openCreate()');
  await parseLinks(app, [link], () => preview(magnet)); await tool(app, '仅选视频').emit('click');
  assert.equal((await submitChosen(app)).length, 0); await checkOriginal(app, id);
});

test('迟到的新建窗口关闭事件不取消重开后的解析', async () => {
  const app = await createApp(); app.run('openCreate()'); app.node('links').value = magnet;
  const pending = app.node('resolve-links').emit('click'), request = app.take('/resources/resolve');
  await app.node('create-dialog').emit('close'); assert.equal(request.signal.aborted, false);
  request.respond(preview()); await pending; assert.ok(checkbox(app, '影片.mp4'));
});

test('旧已确认记录关闭清理前不会变成新的创建请求', async () => {
 const state=account(), link=submissionLinks(1)[0], id='0123456789abcdef0123456789abcdef';
 const old={ [JSON.stringify([state.target,state.directory,link])]:{id,done:true} };
 const app=await createApp({storage:new Map([[submissionStorageKey,JSON.stringify(old)]])});app.run('openCreate()');
 await parseLinks(app,[link]);assert.equal((await submitChosen(app)).length,0);
 app.node('create-dialog').close();await app.node('create-dialog').closed;app.run('openCreate()');
 await parseLinks(app,[link]);assert.equal((await submitChosen(app)).length,1);
});

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
const unknownSubmission = (request) => request.respond({ code: 'submission_unknown', message: '此前提交结果仍未确认，请检查原提交' }, 409);

async function submitLinks(app, links, respond = unknownSubmission) {
  app.node('links').value = links.join('\n');
  const pending = app.node('create-form').emit('submit'), submitted = [];
  for (;;) {
    await settle();
    if (!app.requests.some((request) => !request.taken && request.url.pathname === '/api/remote/tasks')) break;
    const request = app.take('/tasks');
    if (request.method === 'GET') request.respond(taskPage());
    else { submitted.push(JSON.parse(request.body)); respond(request); }
  }
  await pending;
  return submitted;
}

test('连续提交超过一百条未知结果后关闭重开仍沿用最早编号', async () => {
  const app = await createApp(), links = submissionLinks(150);
  let first;
  for (let offset = 0; offset < links.length; offset += 50) {
    app.run('openCreate()');
    const requests = await submitLinks(app, links.slice(offset, offset + 50));
    if (!offset) first = requests[0];
    app.node('create-dialog').close(); await app.node('create-dialog').closed;
  }
  app.run('openCreate()');
  const retried = await submitLinks(app, [links[0]]);
  assert.equal(retried.length, 1);
  assert.equal(retried[0].request_id, first.request_id, '未知提交必须保留原编号，不能改用新编号自动重发');
});

test('提交记录满额时拒绝新链接但仍检查已有编号', async () => {
  const storage = new Map(), app = await createApp({ storage }), links = submissionLinks(100);
  app.run('openCreate()');
  const first = await submitLinks(app, links.slice(0, 50));
  await submitLinks(app, links.slice(50));
  const before = storage.get(submissionStorageKey);
  const retried = await submitLinks(app, [submissionLinks(1, 100)[0], links[0]]);
  assert.equal(retried.length, 1, '容量不足时不得向接口发送新编号');
  assert.equal(retried[0].request_id, first[0].request_id);
  assert.equal(storage.get(submissionStorageKey), before, '拒绝新提交不得改变未知记录');
  assert.match(app.node('create-results').children[0].children[1].textContent, /已满|上限/);
  assert.match(app.node('create-results').children[0].children[1].textContent, /检查|核对/);
});

test('恢复超过一百条旧未知记录时全部保留且拒绝继续新增', async () => {
  const links = submissionLinks(150), status = account();
  const records = Object.fromEntries(links.map((link, index) => [JSON.stringify([status.target, status.directory, link]), { id: index.toString(16).padStart(32, '0'), done: false }]));
  const saved = JSON.stringify(records), storage = new Map([[submissionStorageKey, saved]]);
  const app = await createApp({ storage });
  app.run('openCreate()');
  const retried = await submitLinks(app, [links[0]]);
  assert.equal(retried[0].request_id, Object.values(records)[0].id);
  assert.equal(storage.get(submissionStorageKey), saved, '保存时不得截断已有的超限未知记录');
  assert.equal((await submitLinks(app, submissionLinks(1, 150))).length, 0);
  app.node('create-dialog').close(); await app.node('create-dialog').closed;
  const reloaded = await createApp({ storage });
  reloaded.run('openCreate()');
  const again = await submitLinks(reloaded, [links[0], links[149]]);
  assert.deepEqual(again.map((request) => request.request_id), [Object.values(records)[0].id, Object.values(records)[149].id]);
  assert.equal(storage.get(submissionStorageKey), saved);
});

test('满额后确认成功的记录在当前窗口复用并在关闭后释放容量', async () => {
  const storage = new Map(), app = await createApp({ storage }), links = submissionLinks(100);
  app.run('openCreate()');
  const first = await submitLinks(app, links.slice(0, 50));
  await submitLinks(app, links.slice(50));
  const confirmed = await submitLinks(app, [links[0]], (request) => request.respond({}));
  assert.equal(confirmed[0].request_id, first[0].request_id);
  assert.equal((await submitLinks(app, [links[0]])).length, 0, '当前窗口内成功项不得重复发送');
  const next = submissionLinks(1, 100);
  assert.equal((await submitLinks(app, next)).length, 0, '关闭窗口前仍保留成功项的去重记录');
  app.node('create-dialog').close(); await app.node('create-dialog').closed;
  assert.equal(Object.keys(JSON.parse(storage.get(submissionStorageKey))).length, 99);
  app.run('openCreate()');
  assert.equal((await submitLinks(app, next)).length, 1);
  assert.equal(Object.keys(JSON.parse(storage.get(submissionStorageKey))).length, 100);
  const retried = await submitLinks(app, [links[1]]);
  assert.equal(retried[0].request_id, first[1].request_id, '释放成功记录不得影响其他未知编号');
});

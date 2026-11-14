'use strict';

const $ = (id) => document.getElementById(id);
let status = {}, tasks = [], filter = 'all', nextPage = '', refreshing = false;
let creating = false, folderCurrent = '', folderHistory = [], folderSession = null;
let generation = 0, pagesLoaded = 0, submissions = {};
let loginGeneration = 0, verificationUsername = '', smsTimer = null, noticeTimer = null, refreshQueued = false;
let visibleInPanel = window.parent === window;
const queries = new Map();
const maxSubmissions = 100;
try { submissions = JSON.parse(sessionStorage.getItem('xlp-remote-submissions') || '{}'); } catch { /* 浏览器不支持存储时使用当前页面内存。 */ }
if (!submissions || typeof submissions !== 'object' || Array.isArray(submissions)) submissions = {};

function rememberSubmissions() {
  // 保留已有编号，容量不足时在提交入口拒绝新增，不能淘汰未知结果。
  try { sessionStorage.setItem('xlp-remote-submissions', JSON.stringify(submissions)); } catch { /* 存储满时仍可在本页检查提交结果。 */ }
}
function notice(message, success = false) {
  clearTimeout(noticeTimer);
  $('notice').textContent = message;
  $('notice').classList.toggle('success', success);
  $('notice').hidden = !message;
  if (message && success) noticeTimer = setTimeout(() => notice(''), 5000);
}
async function api(path, method = 'GET', body, signal) {
  const headers = {};
  if (method !== 'GET') { headers['Content-Type'] = 'application/json'; headers['X-XLP-Request'] = '1'; }
  let response;
  try {
    const timeout = AbortSignal.timeout(100000);
    response = await fetch('/api/remote' + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
  } catch {
    const error = new Error('网络连接中断或请求超时，请稍后检查结果'); error.code = 'network'; throw error;
  }
  let data;
  try { data = await response.json(); } catch { data = {}; }
  if (!response.ok) {
    const error = new Error(data.message || (response.status === 401 ? '面板认证已失效，请刷新页面重新认证' : '请求失败，请稍后重试'));
    error.code = data.code || 'network'; throw error;
  }
  return data;
}
function cancelQuery(kind) {
  queries.get(kind)?.controller.abort();
  queries.delete(kind);
}
function beginQuery(kind) {
  cancelQuery(kind);
  const query = { generation, controller: new AbortController() };
  queries.set(kind, query); return query;
}
function currentQuery(kind, query) {
  return !!query && queries.get(kind) === query && query.generation === generation;
}
function tasksVisible() { return visibleInPanel && !document.hidden; }
function cancelTaskRefresh() {
  cancelQuery('tasks'); refreshing = false; refreshQueued = false;
  $('refresh-button').disabled = false; $('load-more').disabled = false;
}
function resetFolders() {
  cancelQuery('folders'); folderSession = null; folderCurrent = ''; folderHistory = [];
  $('folder-select').disabled = true; $('folder-back').disabled = true;
}
function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  const use = document.createElementNS(svg.namespaceURI, 'use');
  use.setAttribute('href', '#i-' + name); svg.append(use); return svg;
}
function element(tag, className, text) {
  const node = document.createElement(tag); if (className) node.className = className;
  if (text !== undefined) node.textContent = text; return node;
}
function showDialog(id) { if (!$(id).open) $(id).showModal(); }
document.querySelectorAll('[data-close]').forEach((button) => button.addEventListener('click', () => {
  if (!creating) button.closest('dialog').close();
}));
$('create-dialog').addEventListener('cancel', (event) => { if (creating) event.preventDefault(); });

async function loadStatus() {
  const query = beginQuery('status');
  let data;
  try { data = await api('/status', 'GET', undefined, query.controller.signal); }
  catch (error) { if (!currentQuery('status', query)) return false; throw error; }
  if (!currentQuery('status', query)) return false;
  const previous = status;
  status = data;
  if (previous.target !== status.target || previous.username !== status.username || previous.logged_in !== status.logged_in) {
    generation++; tasks = []; nextPage = ''; pagesLoaded = 0;
    cancelTaskRefresh(); resetFolders(); $('folder-dialog').close();
    cancelQuery('devices'); $('device-dialog').close();
  }
  if (previous.username && previous.username !== status.username) { submissions = {}; rememberSubmissions(); }
  $('account-name').textContent = status.logged_in ? status.username : '未登录迅雷账号';
  $('account-button').textContent = status.logged_in ? '退出授权' : '登录迅雷';
  $('device-name').textContent = status.target_name || '请选择下载设备';
  $('current-directory').textContent = status.directory ? '保存位置：' + status.directory : '';
  $('status-text').textContent = status.logged_in ? (status.target ? '已连接账号 · ' + status.target_name : '已登录，请选择下载设备') : '未登录迅雷账号';
  renderLoginReview();
  renderTasks();
  return true;
}
function renderLoginReview() {
  const current = $('login-dialog').open && verificationUsername && verificationUsername === $('username').value.trim() && verificationUsername === status.username;
  $('review-form').hidden = !(current && status.review_required);
  $('review-mobile').textContent = current ? status.mobile || '' : '';
  $('verification-link').hidden = !(current && status.verification_url);
  if (current && status.verification_url) $('verification-link').href = status.verification_url;
  else $('verification-link').removeAttribute('href');
}
function resetLoginReview() {
  loginGeneration++; verificationUsername = '';
  clearInterval(smsTimer); smsTimer = null;
  $('sms-code').value = ''; $('send-sms').textContent = '获取验证码';
  ['login-submit', 'review-submit', 'send-sms'].forEach((id) => { $(id).disabled = false; });
  renderLoginReview();
}
function openLogin() {
  resetLoginReview();
  $('username').value = status.username || '';
  $('password').value = '';
  $('remember').checked = !!status.remember_password;
  $('login-message').textContent = '';
  showDialog('login-dialog');
}
$('login-dialog').addEventListener('close', () => { resetLoginReview(); $('password').value = ''; });
['username', 'password'].forEach((id) => $(id).addEventListener('input', () => { resetLoginReview(); $('login-message').textContent = ''; }));
async function sessionError(error, valid) {
  if (!valid() || !['login_required', 'captcha_required', 'review_required', 'verification_required'].includes(error.code)) return;
  const username = status.username;
  try { if (!await loadStatus()) return; } catch { return; }
  // 只有最新状态确认当前账号确实需要登录，才打断现有弹窗。
  if (status.logged_in || status.username !== username || !tasksVisible()) return;
  document.querySelectorAll('dialog[open]').forEach((dialog) => dialog.close());
  openLogin(); $('login-message').textContent = error.message;
}
$('account-button').addEventListener('click', async () => {
  if (!status.logged_in) { openLogin(); return; }
  if (!confirm('退出远程登录？已经下发的下载任务会继续运行。')) return;
  try {
    await api('/logout', 'POST', {}); submissions = {}; rememberSubmissions();
    await loadStatus(); notice('已退出远程登录', true);
  } catch (error) { notice(error.message); }
});
$('login-form').addEventListener('submit', async (event) => {
  event.preventDefault(); resetLoginReview();
  const token = loginGeneration, username = $('username').value.trim();
  $('login-submit').disabled = true; $('login-message').textContent = '正在登录…';
  try {
    await api('/login', 'POST', { username, password: $('password').value, remember_password: $('remember').checked });
    if (token !== loginGeneration) return;
    $('password').value = ''; $('login-dialog').close(); await loadStatus(); notice('登录成功', true); await openDevices();
  } catch (error) {
    if (token !== loginGeneration) return;
    $('login-message').textContent = error.message;
    try { await loadStatus(); } catch { /* 保留本次登录错误。 */ }
    if (token === loginGeneration && ['review_required', 'verification_required', 'captcha_required'].includes(error.code)) {
      verificationUsername = username; renderLoginReview();
    }
  } finally { if (token === loginGeneration) $('login-submit').disabled = false; }
});
$('send-sms').addEventListener('click', async () => {
  if ($('review-form').hidden) return;
  const token = loginGeneration;
  $('send-sms').disabled = true;
  try {
    await api('/review/send', 'POST', {});
    if (token !== loginGeneration) return;
    $('login-message').textContent = '验证码已发送，请查收短信';
    let remaining = 60; $('send-sms').textContent = remaining + ' 秒后重发';
    clearInterval(smsTimer);
    smsTimer = setInterval(() => {
      remaining--; $('send-sms').textContent = remaining + ' 秒后重发';
      if (remaining <= 0) { clearInterval(smsTimer); smsTimer = null; $('send-sms').textContent = '获取验证码'; $('send-sms').disabled = false; }
    }, 1000);
  } catch (error) { if (token === loginGeneration) { $('login-message').textContent = error.message; $('send-sms').disabled = false; } }
});
$('review-form').addEventListener('submit', async (event) => {
  event.preventDefault(); if ($('review-form').hidden) return;
  const token = loginGeneration;
  $('review-submit').disabled = true; $('login-message').textContent = '正在验证并登录…';
  try {
    await api('/review/check', 'POST', { code: $('sms-code').value.trim() });
    if (token !== loginGeneration) return;
    $('sms-code').value = ''; $('password').value = ''; $('login-dialog').close();
    await loadStatus(); notice('验证通过，已登录', true); await openDevices();
  } catch (error) {
    if (token !== loginGeneration) return;
    $('login-message').textContent = error.message;
    try { await loadStatus(); } catch { /* 保留验证错误。 */ }
  } finally { if (token === loginGeneration) $('review-submit').disabled = false; }
});

async function openDevices() {
  if (!status.logged_in) { openLogin(); return; }
  const query = beginQuery('devices'), valid = () => currentQuery('devices', query) && $('device-dialog').open;
  showDialog('device-dialog'); $('device-save').disabled = true; $('reload-devices').disabled = true;
  $('device-select').replaceChildren(); $('device-message').textContent = '正在读取账号绑定的设备…';
  try {
    const devices = await api('/devices', 'GET', undefined, query.controller.signal);
    if (!valid()) return;
    $('device-select').append(new Option('请选择下载设备', ''));
    devices.forEach((device) => $('device-select').append(new Option(device.name, device.target)));
    $('device-select').value = status.target || '';
    $('device-save').disabled = !devices.length;
    $('device-message').textContent = devices.length ? '' : '账号下暂未发现远程下载设备';
  } catch (error) { if (valid()) { $('device-message').textContent = error.message; await sessionError(error, valid); } }
  finally { if (valid()) $('reload-devices').disabled = false; }
}
$('device-dialog').addEventListener('close', () => { if (!$('device-dialog').open) cancelQuery('devices'); });
['device-summary', 'reload-devices'].forEach((id) => $(id).addEventListener('click', openDevices));
$('device-form').addEventListener('submit', async (event) => {
  event.preventDefault(); $('device-save').disabled = true;
  try {
    const target = $('device-select').value;
    await api('/settings', 'PUT', { target, directory: target === status.target ? status.directory : '' });
    $('device-dialog').close(); await loadStatus(); notice('已选择下载设备', true); await refreshTasks();
  } catch (error) { $('device-message').textContent = error.message; }
  finally { $('device-save').disabled = false; }
});

async function browseFolder(directory, remember = true) {
  const session = folderSession;
  if (!session || session.generation !== generation || !$('folder-dialog').open) return;
  const query = beginQuery('folders'), valid = () => currentQuery('folders', query) && folderSession === session && $('folder-dialog').open;
  $('folder-select').disabled = true; $('folder-back').disabled = true;
  $('folders').replaceChildren(element('p', 'muted', '正在读取设备目录…')); $('folder-message').textContent = '';
  try {
    const folders = await api('/folders?target=' + encodeURIComponent(session.target) + '&path=' + encodeURIComponent(directory), 'GET', undefined, query.controller.signal);
    if (!valid()) return;
    if (remember && directory !== folderCurrent) folderHistory.push(folderCurrent);
    folderCurrent = directory; $('folder-path').textContent = directory || '设备目录';
    $('folders').replaceChildren();
    if (!folders.length) $('folders').append(element('p', 'muted', '此目录下没有子文件夹'));
    folders.forEach((folder) => {
      const row = element('button', 'folder-row'); row.type = 'button';
      row.append(icon('folder'), element('span', '', folder.name)); row.title = folder.path;
      row.addEventListener('click', () => browseFolder(folder.path)); $('folders').append(row);
    });
    $('folder-select').disabled = !directory;
  } catch (error) {
    if (!valid()) return;
    $('folders').replaceChildren(); $('folder-message').textContent = error.message;
  } finally { if (valid()) $('folder-back').disabled = !folderHistory.length; }
}
async function openFolders(directory, callback) {
  if (!status.logged_in) { openLogin(); return; }
  if (!status.target) { await openDevices(); return; }
  resetFolders(); folderSession = { callback, target: status.target, generation };
  showDialog('folder-dialog'); await browseFolder(directory || '', false);
}
$('folder-root').addEventListener('click', () => browseFolder(''));
$('folder-back').addEventListener('click', () => browseFolder(folderHistory.pop() || '', false));
$('folder-dialog').addEventListener('close', () => { if (!$('folder-dialog').open) resetFolders(); });
$('folder-select').addEventListener('click', async () => {
  const session = folderSession, query = queries.get('folders');
  const valid = () => !!session && folderSession === session && currentQuery('folders', query) && $('folder-dialog').open;
  if (!valid() || !folderCurrent || $('folder-select').disabled) return;
  $('folder-select').disabled = true;
  try { await session.callback(folderCurrent, session.target); if (valid()) $('folder-dialog').close(); }
  catch (error) { if (valid()) $('folder-message').textContent = error.message; }
  finally { if (valid()) $('folder-select').disabled = false; }
});
$('directory-button').addEventListener('click', () => openFolders(status.directory, async (directory, target) => {
  await api('/settings', 'PUT', { target, directory }); await loadStatus(); notice('默认下载目录已保存', true);
}));
$('create-browse').addEventListener('click', () => openFolders($('create-directory').value, (directory) => { $('create-directory').value = directory; }));

function phaseOf(task) {
  const phase = task.phase || '';
  if (/COMPLETE|DONE/.test(phase)) return 'completed';
  if (/PAUSE|STOP/.test(phase)) return 'paused';
  if (/ERROR|FAIL/.test(phase)) return 'error';
  return 'active';
}
function sizeText(bytes) {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB']; let unit = 0;
  while (bytes >= 1024 && unit < units.length - 1) { bytes /= 1024; unit++; }
  return bytes.toFixed(unit ? 1 : 0) + ' ' + units[unit];
}
function renderTasks() {
  $('task-count').textContent = tasks.length + (nextPage ? '+' : '');
  const visible = tasks.filter((task) => filter === 'all' || phaseOf(task) === filter);
  $('task-list').replaceChildren(); $('empty-state').hidden = !!visible.length;
  $('load-more').hidden = !nextPage;
  if (!visible.length) {
    let title = '暂无下载任务', detail = '点击“新建下载”添加链接，文件将保存到所选设备', button = '新建下载';
    if (!status.logged_in) { title = '登录后使用远程下载'; detail = '登录迅雷账号，选择设备后即可开始下载'; button = '登录迅雷账号'; }
    else if (!status.target) { title = '请选择下载设备'; detail = '选择账号绑定的迅雷设备，远程管理下载任务'; button = '选择设备'; }
    else if (filter !== 'all') { title = '此分类下暂无任务'; detail = nextPage ? '可以加载更多任务，或切换到“全部任务”查看' : '可以切换到“全部任务”查看其他下载'; }
    $('empty-title').textContent = title; $('empty-detail').textContent = detail; $('empty-button').textContent = button;
  }
  visible.forEach((task) => {
    const row = element('article', 'task'), file = element('div', 'file-icon'), info = element('div', 'task-info');
    file.append(icon('download')); const name = element('h2', 'task-name', task.name); name.title = task.name;
    const progress = document.createElement('progress'); progress.max = 100; progress.value = task.progress; progress.setAttribute('aria-label', task.name + ' 下载进度');
    const phase = phaseOf(task), meta = element('div', 'task-meta');
    const phaseText = { completed: '已完成', paused: '已暂停', error: '下载失败', active: /PENDING/.test(task.phase) ? '排队中' : '下载中' }[phase];
    meta.append(element('span', phase === 'error' ? 'failed' : phase, phaseText), element('span', '', Number(task.progress || 0).toFixed(1) + '%'), element('span', '', sizeText(task.done) + ' / ' + sizeText(task.size)));
    if (task.speed > 0) meta.append(element('span', '', sizeText(task.speed) + '/秒'));
    if (task.directory) meta.title = '保存位置：' + task.directory;
    info.append(name, progress, meta);
    const actions = element('div', 'task-actions');
    if (phase !== 'completed') {
      const action = phase === 'active' ? 'pause' : 'resume';
      const button = element('button', '', action === 'pause' ? '暂停' : '继续');
      button.addEventListener('click', () => taskAction(task, action, button)); actions.append(button);
    }
    const remove = element('button', '', '删除'); remove.addEventListener('click', () => taskAction(task, 'delete', remove)); actions.append(remove);
    row.append(file, info, actions); $('task-list').append(row);
  });
}
async function refreshTasks(append = false) {
  if (!tasksVisible() || !status.logged_in || !status.target) return;
  if (refreshing) { refreshQueued = true; return; }
  const query = beginQuery('tasks'), valid = () => currentQuery('tasks', query) && tasksVisible();
  refreshing = true; $('refresh-button').disabled = true; $('load-more').disabled = true;
  const target = status.target, limit = append ? 1 : Math.max(1, pagesLoaded);
  try {
    const combined = append ? [...tasks] : [], seen = new Set();
    let pageToken = append ? nextPage : '', loaded = 0;
    do {
      if (!valid()) return;
      seen.add(pageToken);
      const data = await api('/tasks?target=' + encodeURIComponent(target) + '&page_token=' + encodeURIComponent(pageToken), 'GET', undefined, query.controller.signal);
      if (!valid()) return;
      combined.push(...data.tasks); loaded++;
      pageToken = data.next_page_token || '';
      if (seen.has(pageToken)) pageToken = '';
    } while (pageToken && loaded < limit);
    tasks = [...new Map(combined.map((task) => [task.id, task])).values()]; nextPage = pageToken;
    pagesLoaded = append ? pagesLoaded + loaded : loaded; renderTasks();
    if ($('notice').classList.contains('success')) notice('');
    $('status-text').textContent = '已更新 · ' + new Date().toLocaleTimeString('zh-CN');
  } catch (error) {
    if (!valid()) return;
    notice(error.message); $('status-text').textContent = '刷新失败，可稍后重试'; await sessionError(error, valid);
  }
  finally {
    if (currentQuery('tasks', query)) {
      const queued = refreshQueued;
      cancelTaskRefresh();
      if (queued && tasksVisible()) await refreshTasks();
    }
  }
}
async function taskAction(task, action, button) {
  if (action === 'delete' && !confirm('删除任务“' + task.name + '”？删除后的任务无法在此恢复。')) return;
  button.disabled = true;
  try {
    await api('/tasks/' + encodeURIComponent(task.id) + '/' + action, 'POST', { target: status.target });
    notice('操作已提交，正在更新任务状态', true); await refreshTasks();
  } catch (error) { notice(error.message); }
  finally { button.disabled = false; }
}
async function refreshView() {
  try { if (await loadStatus()) await refreshTasks(); } catch (error) { notice(error.message); }
}
$('refresh-button').addEventListener('click', refreshView);
$('load-more').addEventListener('click', () => refreshTasks(true));
document.querySelectorAll('[data-filter]').forEach((button) => button.addEventListener('click', () => {
  filter = button.dataset.filter;
  document.querySelectorAll('[data-filter]').forEach((tab) => { tab.classList.toggle('active', tab === button); tab.setAttribute('aria-selected', String(tab === button)); });
  if ($('notice').classList.contains('success')) notice('');
  renderTasks(); refreshTasks();
}));
function openCreate() {
  if (!status.logged_in) { openLogin(); return; }
  if (!status.target) { openDevices(); return; }
  $('create-device').textContent = status.target_name;
  $('create-directory').value = status.directory || '';
  $('create-message').textContent = ''; showDialog('create-dialog');
}
['new-button', 'empty-button'].forEach((id) => $(id).addEventListener('click', openCreate));
$('panel-back').addEventListener('click', () => {
  if (window.parent === window) { location.href = '/webman/3rdparty/pan-xunlei-com/index.cgi/#/home'; return; }
  window.parent.postMessage({ type: 'xlp:remote-close' }, location.origin);
});
window.addEventListener('message', (event) => {
  if (event.source !== window.parent || event.origin !== location.origin || event.data?.type !== 'xlp:remote-visibility') return;
  const previous = visibleInPanel;
  visibleInPanel = event.data.visible === true;
  if (previous !== visibleInPanel) updateTaskVisibility();
});
function updateTaskVisibility() {
  if (!tasksVisible()) {
    cancelTaskRefresh(); cancelQuery('status');
    if ($('notice').classList.contains('success')) notice('');
  } else refreshView();
}
document.addEventListener('visibilitychange', updateTaskVisibility);

function requestID() {
  const bytes = new Uint8Array(16); crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
}
$('create-form').addEventListener('submit', async (event) => {
  event.preventDefault(); if (creating) return;
  const links = [...new Set($('links').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean))];
  const directory = $('create-directory').value, target = status.target;
  if (!links.length || links.length > 50) { $('create-message').textContent = '每批请输入 1 至 50 条链接，每行一条'; return; }
  if (!directory) { $('create-message').textContent = '请先选择设备上的下载目录'; return; }
  creating = true; $('create-results').replaceChildren(); $('create-message').textContent = '正在逐条提交，请保持页面打开…';
  $('create-form').querySelectorAll('button, textarea, input').forEach((node) => { node.disabled = true; });
  let completed = 0;
  for (const link of links) {
    const key = JSON.stringify([target, directory, link]);
    let record = submissions[key];
    const row = element('div', 'result'), result = element('span', '', '正在提交…');
    const label = element('span', '', link); label.title = link; row.append(label, result); $('create-results').append(row);
    try {
      if (!record) {
        if (Object.keys(submissions).length >= maxSubmissions) throw new Error('提交记录已达上限，请先检查未完成项，关闭窗口清理已提交记录后再添加新链接。');
        record = { id: requestID(), done: false };
        submissions[key] = record; rememberSubmissions();
      }
      if (!record.done) await api('/tasks', 'POST', { request_id: record.id, target, directory, url: link });
      record.done = true; completed++; result.textContent = '已提交'; result.className = 'ok';
    } catch (error) { result.textContent = error.message; result.className = 'error'; }
    rememberSubmissions();
  }
  creating = false; $('create-form').querySelectorAll('button, textarea, input').forEach((node) => { node.disabled = false; });
  $('create-message').textContent = '已提交 ' + completed + ' / ' + links.length + ' 条。' + (completed < links.length ? '可再次点击检查或重试未完成项；已提交项不会重复创建。' : '可关闭此窗口查看下载进度。');
  $('create-submit').textContent = completed < links.length ? '检查或重试未完成项' : '立即下载';
  await refreshTasks();
});
$('create-dialog').addEventListener('close', () => {
  // 保留结果未知的请求编号，重新打开窗口也不会意外重复下发。
  Object.keys(submissions).forEach((key) => { if (submissions[key].done) delete submissions[key]; });
  rememberSubmissions(); $('create-results').replaceChildren(); $('create-submit').textContent = '立即下载';
});

(async () => {
  try {
    if (await loadStatus()) await refreshTasks();
    if (window.parent !== window) window.parent.postMessage({ type: 'xlp:remote-ready' }, location.origin);
  } catch (error) { notice(error.message); $('status-text').textContent = '连接失败，请刷新页面'; }
})();
setInterval(() => {
  if (tasksVisible() && !document.querySelector('dialog[open]')) refreshTasks();
}, 15000);

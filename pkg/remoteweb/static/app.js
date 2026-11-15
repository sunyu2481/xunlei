'use strict';

const $ = (id) => document.getElementById(id);
let status = {}, tasks = [], filter = 'all', nextPage = '', refreshing = false;
let creating = false, folderCurrent = '', folderHistory = [], folderSession = null;
let generation = 0, pagesLoaded = 0, submissions = {};
let loginGeneration = 0, verificationUsername = '', smsTimer = null, noticeTimer = null, refreshQueued = false;
let visibleInPanel = window.parent === window;
const queries = new Map();
const maxSubmissions = 100;
let createEpoch = 0, resolving = false, entries = [], drafts = [], storageAvailable = true;
function validRecord(r) {
  return r && typeof r.id === 'string' && r.id.length >= 16 && r.id.length <= 80 &&
    ['account', 'target', 'directory', 'url'].every((k) => typeof r[k] === 'string') &&
    ['unknown', 'confirmed', 'missing'].includes(r.state) &&
    ['legacy', 'files', 'single'].includes(r.mode) && typeof r.version === 'string' &&
    Array.isArray(r.indices) && r.indices.length <= 5000 && r.indices.every((n) => Number.isSafeInteger(n) && n >= 0);
}
try {
  const saved = JSON.parse(sessionStorage.getItem('xlp-remote-submissions') || '{}');
  if (saved?.version === 2 && saved.records && typeof saved.records === 'object' && !Array.isArray(saved.records)) {
    for (const [key, record] of Object.entries(saved.records)) if (validRecord(record)) submissions[key] = record;
    if (Array.isArray(saved.drafts)) drafts = saved.drafts.slice(0, 50).filter((d) => d && typeof d.url === 'string' && typeof d.account === 'string' && typeof d.target === 'string' && typeof d.version === 'string' && Array.isArray(d.indices) && d.indices.length <= 5000 && d.indices.every((n) => Number.isSafeInteger(n) && n >= 0));
  } else if (saved && typeof saved === 'object' && !Array.isArray(saved) && saved.version === undefined) {
    for (const [key, r] of Object.entries(saved)) {
      let intent; try { intent = JSON.parse(key); } catch { continue; }
      if (!Array.isArray(intent) || intent.length !== 3 || !intent.every((v) => typeof v === 'string') || !r || typeof r.id !== 'string' || r.id.length < 16 || r.id.length > 80 || typeof r.done !== 'boolean') continue;
      submissions[key] = { id: r.id, account: '', target: intent[0], directory: intent[1], url: intent[2], mode: 'legacy', version: '', indices: [], state: r.done ? 'confirmed' : 'unknown' };
    }
  }
} catch { storageAvailable = false; }
function rememberSubmissions() {
  try { sessionStorage.setItem('xlp-remote-submissions', JSON.stringify({ version: 2, records: submissions, drafts })); }
  catch { storageAvailable = false; }
}
function rememberDrafts() {
  drafts = entries.filter((e) => !e.record && !e.skip).slice(0, 50).map((e) => ({ url: e.raw, account: status.username || '', target: status.target || '', version: e.preview?.resource_version || e.restore?.version || '', indices: (e.preview ? [...e.selected] : e.restore?.indices || []).filter((n) => n >= 0) }));
  rememberSubmissions();
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
    error.code = data.code || 'network'; error.remoteCode = data.remote_code || 0; throw error;
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
    invalidateCreate();
  }
  for (const r of Object.values(submissions)) if (!r.account) r.account = status.username || '';
  renderHistory();
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
  createEpoch++;
  if (!$('links').value) $('links').value = drafts.filter((d) => d.account === status.username && d.target === status.target).map((d) => d.url).join('\n');
  syncEntries(); renderHistory();
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
function cancelResolve() {
  createEpoch++; cancelQuery('resolve'); resolving = false;
  for (const e of entries) { e.controller?.abort(); if (e.state === 'resolving') e.state = 'input'; }
  renderCreate();
}
function invalidateCreate() {
  cancelResolve(); entries = []; $('create-results').replaceChildren();
  $('create-submit').disabled = true;
  if ($('create-dialog').open && !creating) $('create-dialog').close();
}
function syncEntries() {
  const links = [...new Set($('links').value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean))];
  const old = new Map(entries.map((e) => [e.raw, e]));
  for (const e of entries) if (!links.includes(e.raw)) e.controller?.abort();
  entries = links.slice(0, 50).map((raw) => old.get(raw) || { raw, epoch: 0, state: 'input', selected: new Set(), skip: false, restore: drafts.find((d) => d.url === raw && d.account === status.username && d.target === status.target) });
  renderCreate(); rememberDrafts();
  return links.length > 0 && links.length <= 50;
}
function canSubmit(e) {
  return !e.skip && e.state === 'ready' && !e.record && e.preview?.complete === true && Date.parse(e.preview.expires_at) > Date.now() && e.selected.size > 0;
}
function directoryIntent(directory) {
  return '/' + directory.split('/').filter((segment) => segment !== '' && segment !== '.').join('/');
}
function summaryCreate() {
  const selected = entries.filter(canSubmit);
  let bytes = 0n, count = 0;
  for (const e of selected) for (const f of e.preview.files) if (!f.is_dir && e.selected.has(f.index ?? -1)) { bytes += BigInt(f.size); count++; }
  $('selection-summary').textContent = '将提交 ' + selected.length + ' 条，跳过 ' + (entries.length - selected.length) + ' 条；已选 ' + count + ' 个文件，共 ' + sizeText(Number(bytes)) + '。' + (!storageAvailable ? '浏览器存储不可用，刷新后无法保证恢复提交编号。' : '');
  $('create-submit').disabled = creating || resolving || !selected.length;
}
function renderHistory() {
  $('submission-history').replaceChildren();
  for (const r of Object.values(submissions)) {
    if (r.account !== status.username || r.target !== status.target) continue;
    const row = element('div', 'history-row');
    row.append(element('div', '', r.url + ' · ' + r.directory), element('span', '', r.state === 'confirmed' ? '已提交' : r.state === 'missing' ? '本地记录已缺失，请在设备中核对，不能自动重发' : '提交结果未知，请检查原提交'));
    if (r.state !== 'confirmed') {
      const button = element('button', '', '检查提交结果'); button.type = 'button';
      button.addEventListener('click', () => checkRecord(r, button)); row.append(button);
    }
    $('submission-history').append(row);
  }
}
async function checkRecord(r, button) {
  if (button.disabled || creating) return;
  button.disabled = true; const token = generation;
  try {
    const result = await api('/submissions/' + encodeURIComponent(r.id) + '?target=' + encodeURIComponent(r.target));
    if (['confirmed', 'unknown', 'missing'].includes(result.status)) { r.state = result.status; r.task_id = result.task_id || ''; rememberSubmissions(); }
    if (token === generation) { renderHistory(); renderCreate(); }
  } catch (error) { if (token === generation) $('create-message').textContent = error.message; }
  finally { button.disabled = false; }
}
function renderCreate() {
  const focusKey = document.activeElement?.dataset?.focusKey, scroll = $('create-results').scrollTop;
  let focusNode;
  function focusable(node, key) { node.dataset.focusKey = JSON.stringify(key); if (node.dataset.focusKey === focusKey) focusNode = node; return node; }
  for (const e of entries) e.treeScroll = e.tree?.scrollTop || 0;
  $('create-results').replaceChildren();
  $('resolve-links').disabled = creating || resolving;
  $('cancel-resolve').hidden = !resolving;
  for (const e of entries) {
    if (e.record?.state === 'confirmed') { e.state = 'confirmed'; e.message = '已提交'; }
    const card = element('section', 'resource-card'), input = focusable(element('input'), [e.raw, 'link']); input.value = e.raw; input.disabled = creating; input.setAttribute('aria-label', '编辑下载链接');
    input.addEventListener('input', () => {
      e.controller?.abort(); e.epoch++; e.raw = input.value.trim(); e.preview = null; e.selected.clear(); e.restore = null; e.record = null; e.state = 'input';
      $('links').value = entries.map((item) => item.raw).join('\n'); summaryCreate(); rememberDrafts();
    });
    input.addEventListener('change', renderCreate); card.append(input);
    const messages = { input: '尚未解析', resolving: '正在解析文件列表…', ready: '请选择需要下载的文件', resolve_failed: '解析失败', expired: '预览已过期，请重新解析', submitting: '正在提交，请保持页面打开', confirmed: '已提交', unknown: '结果未知，请检查原提交', rejected: '未创建，请修正后重试' };
    card.append(element('p', 'resource-status', e.message || messages[e.state] || ''));
    const tools = element('div', 'resource-tools');
    const skip = focusable(element('button', '', e.skip ? '恢复此条' : '跳过此条'), [e.raw, 'skip']); skip.type = 'button'; skip.disabled = creating;
    skip.addEventListener('click', () => { e.skip = !e.skip; renderCreate(); rememberDrafts(); }); tools.append(skip);
    if (e.preview) {
      card.append(element('h2', '', e.preview.name));
      const leaves = e.preview.files.filter((f) => !f.is_dir);
      for (const [label, predicate] of [['全选', () => true], ['取消全选', () => false], ['仅选视频', (f) => /\.(mp4|mkv|avi|mov|wmv|flv|webm|m4v|ts|m2ts|rmvb|rm|mpeg|mpg)$/i.test(f.name)]]) {
        const button = focusable(element('button', '', label), [e.raw, label]); button.type = 'button'; button.disabled = creating || !!e.record;
        button.addEventListener('click', () => { e.selected = new Set(leaves.filter(predicate).map((f) => f.index ?? -1)); renderCreate(); rememberDrafts(); }); tools.append(button);
      }
      const sort = focusable(element('select'), [e.raw, 'sort']); sort.setAttribute('aria-label', '文件排序'); sort.append(new Option('按名称排序', 'name'), new Option('按大小排序', 'size')); sort.value = e.sort || 'name'; sort.disabled = creating;
      sort.addEventListener('change', () => { e.sort = sort.value; renderCreate(); }); tools.append(sort);
      card.append(tools);
      const tree = element('div', 'file-tree'), children = new Map();
      for (const f of e.preview.files) { if (!children.has(f.parent_id)) children.set(f.parent_id, []); children.get(f.parent_id).push(f); }
      function descendants(f) { return f.is_dir ? (children.get(f.id) || []).flatMap(descendants) : [f]; }
      function draw(parent, container) {
        const list = [...(children.get(parent) || [])];
        list.sort((a, b) => e.sort === 'size' ? (BigInt(a.size) < BigInt(b.size) ? 1 : BigInt(a.size) > BigInt(b.size) ? -1 : a.name.localeCompare(b.name)) : a.name.localeCompare(b.name));
        for (const f of list) {
          const row = element('label', 'file-row'), checkbox = focusable(element('input'), [e.raw, 'file', f.id]); checkbox.type = 'checkbox';
          const files = descendants(f), checked = files.filter((n) => e.selected.has(n.index ?? -1)).length;
          checkbox.checked = !!files.length && checked === files.length; checkbox.indeterminate = checked > 0 && checked < files.length;
          checkbox.disabled = creating || e.skip || !!e.record || !files.length; checkbox.setAttribute('aria-label', f.name);
          checkbox.addEventListener('change', () => { for (const n of files) { if (checkbox.checked) e.selected.add(n.index ?? -1); else e.selected.delete(n.index ?? -1); } renderCreate(); rememberDrafts(); });
          row.append(checkbox, element('span', 'file-name', (f.is_dir ? '📁 ' : '') + f.name), element('span', 'file-size', sizeText(Number(f.size)))); container.append(row);
          if (f.is_dir) { const nested = element('div', 'file-children'); container.append(nested); draw(f.id, nested); }
        }
      }
      draw('', tree); card.append(tree); e.tree = tree;
      let size = 0n; for (const f of leaves) if (e.selected.has(f.index ?? -1)) size += BigInt(f.size);
      card.append(element('p', 'resource-status', '已选 ' + e.selected.size + ' / ' + leaves.length + ' 个文件，共 ' + sizeText(Number(size))));
    } else card.append(tools);
    $('create-results').append(card);
    if (e.tree) e.tree.scrollTop = e.treeScroll;
  }
  $('create-results').scrollTop = scroll;
  focusNode?.focus?.({ preventScroll: true });
  summaryCreate();
}
$('links').addEventListener('input', () => { if (!creating) syncEntries(); });
$('cancel-resolve').addEventListener('click', cancelResolve);
$('resolve-links').addEventListener('click', async () => {
  if (creating || resolving) return;
  if (!syncEntries()) { $('create-message').textContent = '每批请输入 1 至 50 条链接，每行一条'; return; }
  const token = ++createEpoch, session = generation, target = status.target;
  resolving = true; renderCreate();
  for (const e of [...entries]) {
    if (token !== createEpoch || session !== generation || !$('create-dialog').open) break;
    if (e.skip || e.record || canSubmit(e) || e.state === 'confirmed' || e.state === 'unknown') continue;
    const epoch = ++e.epoch, controller = new AbortController(); e.controller = controller;
    const valid = () => token === createEpoch && session === generation && e.epoch === epoch && entries.includes(e) && $('create-dialog').open;
    const restore = e.preview ? { version: e.preview.resource_version, indices: [...e.selected] } : e.restore;
    e.state = 'resolving'; e.message = ''; renderCreate();
    try {
      const p = await api('/resources/resolve', 'POST', { target, url: e.raw }, controller.signal);
      if (!valid()) continue;
      if (p.complete !== true || !['files', 'single'].includes(p.kind) || !Array.isArray(p.files)) throw new Error('资源列表不完整，不能创建任务');
      e.preview = p; e.selected = new Set(); e.state = 'ready';
      if (restore?.version === p.resource_version) {
        const available = new Set(p.files.filter((f) => !f.is_dir).map((f) => f.index ?? -1));
        e.selected = new Set(restore.indices.filter((n) => available.has(n)));
      } else if (restore?.version) e.message = '资源内容已变化，已清空旧选择，请重新确认';
      if (p.kind === 'single') e.selected.add(-1);
      e.restore = null;
    } catch (error) { if (valid()) { e.state = 'resolve_failed'; e.preview = null; e.message = error.message; } }
    finally { if (valid()) { renderCreate(); rememberDrafts(); } }
  }
  if (token === createEpoch && session === generation) { resolving = false; renderCreate(); }
});
$('create-form').addEventListener('submit', async (event) => {
  event.preventDefault(); if (creating || resolving) return;
  if (!syncEntries()) { $('create-message').textContent = '每批请输入 1 至 50 条链接，每行一条'; return; }
  const chosen = entries.filter(canSubmit), directory = $('create-directory').value, target = status.target, account = status.username;
  if (!directory) { $('create-message').textContent = '请先选择设备上的下载目录'; return; }
  if (!chosen.length) { $('create-message').textContent = '请先解析链接并选择文件；预览过期后需要重新解析'; return; }
  creating = true; const session = generation, token = createEpoch;
  $('links').disabled = true; $('create-browse').disabled = true;
  let completed = 0;
  for (const e of chosen) {
    if (session !== generation || token !== createEpoch) break;
    const p = e.preview, indices = [...e.selected].filter((n) => n >= 0).sort((a,b) => a-b);
    const key = JSON.stringify([account, target, directoryIntent(directory), p.url, p.kind, p.resource_version, indices]);
    let r = submissions[key];
    // 旧提交或历史未知结果可能没有内容版本，必须先核对，不能为同一链接自动分配新编号。
    const historical = Object.values(submissions).find((v) => v.account === account && v.target === target && directoryIntent(v.directory) === directoryIntent(directory) && (v.url === p.url || v.mode === 'legacy' && v.url === e.raw) && (v.state !== 'confirmed' || v.mode === 'legacy') && (v.mode === 'legacy' || v.mode === p.kind && v.version === p.resource_version && JSON.stringify(v.indices) === JSON.stringify(indices)));
    if (r || historical) { e.record = r || historical; e.state = e.record.state === 'confirmed' ? 'confirmed' : 'unknown'; e.message = '已有提交记录，请使用下方“检查提交结果”'; renderCreate(); continue; }
    if (Object.keys(submissions).length >= maxSubmissions) { e.message = '提交记录已达上限，请先核对未知结果，关闭窗口清理成功项'; renderCreate(); continue; }
    r = { id: requestID(), account, target, directory, url: p.url, mode: p.kind, version: p.resource_version, indices, state: 'unknown' };
    submissions[key] = r; e.record = r; e.state = 'submitting'; rememberDrafts(); renderCreate(); renderHistory();
    try {
      const result = await api('/tasks', 'POST', { request_id: r.id, target, directory, url: p.url, preview_id: p.preview_id, resource_version: p.resource_version, selection_mode: p.kind, file_indices: indices });
      if (!result.id) throw Object.assign(new Error('未收到任务编号，请检查原提交'), { code: 'submission_unknown' });
      r.state = 'confirmed'; r.task_id = result.id; completed++;
      if (session === generation && token === createEpoch) { e.state = 'confirmed'; e.message = '已提交'; }
    } catch (error) {
      const rejected = ['invalid_request', 'invalid_selection', 'selection_required', 'directory_required', 'device_required', 'settings_changed', 'preview_expired', 'preview_mismatch', 'rate_limited', 'resource_unavailable', 'resource_incomplete', 'resource_too_large', 'login_required', 'captcha_required', 'review_required', 'verification_required'].includes(error.code) || error.code === 'upstream' && error.remoteCode;
      if (rejected) { delete submissions[key]; e.record = null; }
      if (session === generation && token === createEpoch) { e.state = rejected ? (error.code === 'preview_expired' ? 'expired' : 'ready') : 'unknown'; e.message = error.message; }
    }
    rememberSubmissions();
    if (session === generation && token === createEpoch) { renderCreate(); renderHistory(); }
  }
  creating = false; $('links').disabled = false; $('create-browse').disabled = false;
  if (session === generation && token === createEpoch) {
    $('create-message').textContent = '已提交 ' + completed + ' / ' + chosen.length + ' 条，跳过 ' + (entries.length - chosen.length) + ' 条。未知结果只可检查原提交。';
    renderCreate(); renderHistory(); rememberDrafts();
  }
  await refreshTasks();
});
$('create-dialog').addEventListener('close', () => {
  if ($('create-dialog').open) return;
  cancelResolve();
  Object.keys(submissions).forEach((key) => { if (submissions[key].state === 'confirmed') delete submissions[key]; });
  entries = entries.filter((e) => !e.record); $('links').value = entries.map((e) => e.raw).join('\n');
  rememberDrafts(); $('create-results').replaceChildren();
});
setInterval(() => {
  if (!$('create-dialog').open || creating || resolving) return;
  let changed = false;
  for (const e of entries) if (e.state === 'ready' && e.preview && Date.parse(e.preview.expires_at) <= Date.now()) { e.state = 'expired'; e.message = '预览已过期，请重新解析'; changed = true; }
  if (changed) renderCreate();
}, 1000);

(async () => {
  try {
    if (await loadStatus()) await refreshTasks();
    if (window.parent !== window) window.parent.postMessage({ type: 'xlp:remote-ready' }, location.origin);
  } catch (error) { notice(error.message); $('status-text').textContent = '连接失败，请刷新页面'; }
})();
setInterval(() => {
  if (tasksVisible() && !document.querySelector('dialog[open]')) refreshTasks();
}, 15000);

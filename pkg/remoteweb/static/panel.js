(() => {
  'use strict';
  if (window.__xlpRemotePanel) return;
  window.__xlpRemotePanel = true;
  const parameter = 'xlpRemote';
  let menu = null, sidebar = null, frame = null, scheduled = false;
  const resizeObserver = new ResizeObserver(layout);
  let lastRoute = '#/home';

  function remoteRoute() {
    const at = location.hash.indexOf('?');
    return at >= 0 && new URLSearchParams(location.hash.slice(at + 1)).get(parameter) === '1';
  }
  function visible(node) {
    const rect = node.getBoundingClientRect();
    return rect.width > 20 && rect.height > 10;
  }
  function nativeMenu() {
    const matches = (node) => !node.closest('[data-xlp-remote-menu]') && /^下载列表(?:\s*[（(]?\d+[）)]?)?$/.test(node.textContent.trim()) && visible(node);
    function rowOf(label) {
      let row = null;
      for (let node = label, depth = 0; node && depth < 5 && matches(node); node = node.parentElement, depth++) {
        if (node.matches('ul, ol, nav, aside, body')) break;
        const rect = node.getBoundingClientRect();
        if (rect.width > 420 || rect.height > 90) break;
        if (rect.width >= 50 && rect.height >= 28) row = node;
      }
      return row;
    }
    const candidates = document.querySelectorAll('a, button, [role="menuitem"], li');
    for (const node of candidates) if (matches(node)) {
      const row = rowOf(node);
      if (row) return row;
    }
    for (const label of document.querySelectorAll('span, div, p')) {
      if (!matches(label)) continue;
      const row = rowOf(label);
      if (row) return row;
    }
    return null;
  }
  function sidebarOf(row) {
    let result = row.parentElement;
    for (let node = result; node && node !== document.body; node = node.parentElement) {
      const rect = node.getBoundingClientRect();
      if (rect.width >= 40 && rect.width <= 420 && rect.right <= innerWidth - 40 && rect.height >= row.getBoundingClientRect().height && rect.left < innerWidth / 2) result = node;
    }
    return result;
  }
  function markNativeItems() {
    if (!sidebar) return;
    sidebar.querySelectorAll('[data-xlp-native-menu]').forEach((node) => node.removeAttribute('data-xlp-native-menu'));
    for (const node of sidebar.querySelectorAll('[class], [aria-current]')) {
      if (node === menu || node.closest('[data-xlp-remote-menu]')) continue;
      if (Array.from(node.classList).some((name) => /(?:^|[-_])(active|selected|current)(?:$|[-_])/i.test(name)) || node.hasAttribute('aria-current')) node.setAttribute('data-xlp-native-menu', '');
    }
  }
  function layout() {
    if (!frame || !sidebar) return;
    const rect = sidebar.getBoundingClientRect();
    const left = Math.max(0, Math.min(rect.right, innerWidth));
    const top = Math.max(0, rect.top);
    frame.style.left = left + 'px'; frame.style.top = top + 'px';
    frame.style.width = Math.max(0, innerWidth - left) + 'px';
    frame.style.height = Math.max(0, innerHeight - top) + 'px';
  }
  function notifyFrame() {
    if (frame && frame.contentWindow) frame.contentWindow.postMessage({ type: 'xlp:remote-visibility', visible: remoteRoute() }, location.origin);
  }
  function sync() {
    const selected = remoteRoute();
    if (!selected) lastRoute = location.hash || '#/home';
    document.documentElement.classList.toggle('xlp-remote-open', selected && !!menu);
    if (menu) {
      menu.setAttribute('aria-current', selected ? 'page' : 'false');
      if (selected && !frame) {
        frame = document.createElement('iframe'); frame.id = 'xlp-remote-view'; frame.title = '远程下载';
        frame.src = '/remote/view.html'; frame.addEventListener('load', notifyFrame);
        document.body.append(frame);
      }
    }
    if (selected) markNativeItems();
    if (frame) frame.hidden = !selected;
    layout(); notifyFrame();
  }
  function openRemote(event) {
    event.preventDefault(); event.stopPropagation();
    const route = location.hash || '#/home';
    const at = route.indexOf('?');
    const params = new URLSearchParams(at >= 0 ? route.slice(at + 1) : '');
    params.set(parameter, '1');
    location.hash = (at >= 0 ? route.slice(0, at) : route) + '?' + params.toString();
    sync();
  }
  function install() {
    scheduled = false;
    if (menu && menu.isConnected && sidebar && sidebar.isConnected) { sync(); return; }
    const original = nativeMenu();
    if (!original) { if (frame) frame.hidden = true; return; }
    menu = original.cloneNode(true);
    menu.removeAttribute('id'); menu.removeAttribute('aria-current'); menu.removeAttribute('data-xlp-native-menu');
    menu.setAttribute('data-xlp-remote-menu', ''); menu.setAttribute('aria-label', '远程下载'); menu.setAttribute('tabindex', '0');
    if (!['A', 'BUTTON'].includes(menu.tagName)) menu.setAttribute('role', 'button');
    for (const node of [menu, ...menu.querySelectorAll('*')]) {
      node.removeAttribute('id'); node.removeAttribute('aria-current');
      for (const attribute of Array.from(node.attributes)) {
        const name = attribute.name;
        // 保留样式作用域，清除原菜单的路由标识，避免原页面同时选中新旧入口。
        if (name.startsWith('on') || (name.startsWith('data-') && !name.startsWith('data-v-') && name !== 'data-xlp-remote-menu')) node.removeAttribute(name);
      }
      for (const name of Array.from(node.classList)) if (/(?:^|[-_])(active|selected|current)(?:$|[-_])/i.test(name)) node.classList.remove(name);
      if (node.tagName === 'A') node.removeAttribute('href');
    }
    const walker = document.createTreeWalker(menu, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      if (node.textContent.includes('下载列表')) node.textContent = '远程下载';
      else if (/^\s*[（(]?\d+[）)]?\s*$/.test(node.textContent)) node.textContent = '';
    }
    menu.addEventListener('click', openRemote);
    menu.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') openRemote(event); });
    original.after(menu); sidebar = sidebarOf(original);
    resizeObserver.disconnect(); resizeObserver.observe(sidebar);
    sync();
  }
  function schedule() { if (!scheduled) { scheduled = true; requestAnimationFrame(install); } }
  new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true });
  window.addEventListener('hashchange', schedule);
  window.addEventListener('popstate', schedule);
  window.addEventListener('resize', layout);
  window.addEventListener('message', (event) => {
    if (!frame || event.source !== frame.contentWindow || event.origin !== location.origin) return;
    if (event.data?.type === 'xlp:remote-ready') notifyFrame();
    if (event.data?.type === 'xlp:remote-close') location.hash = lastRoute;
  });
  install();
})();

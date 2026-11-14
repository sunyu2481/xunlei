'use strict';

// 执行完整页面脚本，只模拟它使用的节点、事件、计时器和接口；不模拟布局或浏览器网络。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class Node {
  constructor(tag = 'div', attributes = '') {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    for (const attribute of attributes.matchAll(/([\w-]+)(?:="([^"]*)")?/g)) this.attributes.set(attribute[1], attribute[2] || '');
    this.textContent = '';
    this.value = this.attributes.get('value') || '';
    this.className = this.attributes.get('class') || '';
    this.hidden = this.attributes.has('hidden');
    this.disabled = false;
    this.open = false;
    this.dataset = { filter: this.attributes.get('data-filter') };
    this.classList = {
      contains: (name) => this.className.split(/\s+/).includes(name),
      toggle: (name, enabled) => {
        const names = new Set(this.className.split(/\s+/).filter(Boolean));
        if (enabled ?? !names.has(name)) names.add(name); else names.delete(name);
        this.className = [...names].join(' ');
      },
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; this.textContent = ''; }
  setAttribute(name, value) { this.attributes.set(name, value); }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  async emit(type, event = {}) {
    await Promise.all((this.listeners.get(type) || []).map((listener) => listener({ preventDefault() {}, ...event })));
  }
  showModal() { this.open = true; }
  close() {
    if (!this.open) return;
    this.open = false;
    this.closed = this.emit('close');
  }
  querySelectorAll(selector) {
    return this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]).filter((child) => selector.split(', ').includes(child.tagName.toLowerCase()));
  }
}

const account = (target = '设备甲', username = '模拟账号') => ({
  logged_in: true, username, target, target_name: target, directory: '/下载 目录/',
});
const taskPage = (name = '模拟任务', next = '') => ({
  tasks: [{ id: name, name, phase: 'PHASE_TYPE_RUNNING', progress: 40, done: 1000, size: 1000 }],
  next_page_token: next,
});
const settle = () => new Promise((resolve) => setImmediate(resolve));

async function createApp({ embedded = false, initialStatus = account(), initialize = true, storage = new Map() } = {}) {
  const nodes = new Map(), filters = [], requests = [], timers = new Map();
  const html = fs.readFileSync(path.join(__dirname, 'static/view.html'), 'utf8');
  for (const match of html.matchAll(/<([a-z][\w-]*)\b([^>]*\b(?:id|data-filter)="[^>]+)>/g)) {
    const node = new Node(match[1], match[2]);
    if (node.attributes.has('id')) nodes.set(node.attributes.get('id'), node);
    if (node.dataset.filter) filters.push(node);
  }
  const document = new Node();
  document.hidden = false;
  document.getElementById = (id) => { assert.ok(nodes.has(id), '页面没有节点：' + id); return nodes.get(id); };
  document.createElement = (tag) => new Node(tag);
  document.createElementNS = (namespace, tag) => Object.assign(new Node(tag), { namespaceURI: namespace });
  document.querySelectorAll = (selector) => {
    if (selector === '[data-filter]') return filters;
    if (selector === '[data-close]') return [];
    if (selector === 'dialog[open]') return [...nodes.values()].filter((node) => node.tagName === 'DIALOG' && node.open);
    throw new Error('尚未模拟的选择器：' + selector);
  };
  document.querySelector = (selector) => document.querySelectorAll(selector)[0] || null;
  const window = new Node();
  window.parent = embedded ? { postMessage() {} } : window;
  let timerID = 0;
  const setTimer = (callback, delay, repeat) => { timers.set(++timerID, { callback, delay, repeat }); return timerID; };
  const context = vm.createContext({
    document, window, location: { origin: 'https://panel.example.test' },
    sessionStorage: { getItem: (key) => storage.get(key), setItem: (key, value) => storage.set(key, value) },
    AbortController, AbortSignal, URL, Uint8Array, crypto: require('node:crypto').webcrypto,
    Option: class extends Node { constructor(text, value) { super('option'); this.textContent = text; this.value = value; } },
    confirm: () => true,
    setTimeout: (callback, delay) => setTimer(callback, delay, false),
    setInterval: (callback, delay) => setTimer(callback, delay, true),
    clearTimeout: (id) => timers.delete(id), clearInterval: (id) => timers.delete(id),
    fetch: (url, options) => new Promise((resolve, reject) => {
      // 即使取消也允许交付迟到响应，确保正确性不依赖网络是否及时响应取消。
      requests.push({ url: new URL(url, 'https://panel.example.test'), ...options, settled: false,
        respond(data, status = 200) { this.settled = true; resolve({ ok: status < 400, status, json: async () => data }); },
        fail(error = new Error('模拟网络故障')) { this.settled = true; reject(error); },
      });
    }),
  });
  const app = {
    nodes, document, window, filters, requests,
    run: (code) => vm.runInContext(code, context),
    node: (id) => document.getElementById(id),
    take(endpoint) {
      const request = requests.find((item) => !item.taken && item.url.pathname === '/api/remote' + endpoint);
      assert.ok(request, '未发起预期请求：' + endpoint); request.taken = true; return request;
    },
    async tick(delay) {
      for (const [id, timer] of [...timers]) if (timer.delay === delay) {
        if (!timer.repeat) timers.delete(id);
        timer.callback();
      }
      await settle();
    },
    async visible(visible) {
      await window.emit('message', { source: window.parent, origin: 'https://panel.example.test', data: { type: 'xlp:remote-visibility', visible } });
      await settle();
    },
    async hidden(hidden) { document.hidden = hidden; await document.emit('visibilitychange'); await settle(); },
    async status(value) {
      const pending = app.run('loadStatus()'); app.take('/status').respond(value); await pending; return pending;
    },
  };
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'static/app.js'), 'utf8'), context);
  if (!initialize) return app;
  app.take('/status').respond(initialStatus);
  await settle();
  if (!embedded && initialStatus.logged_in && initialStatus.target) { app.take('/tasks').respond(taskPage()); await settle(); }
  return app;
}

module.exports = { createApp, account, taskPage, settle };

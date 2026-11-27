'use strict';
// Tiny JSON persistence. Everything the hub keeps is small: sessions, push subscriptions, config.
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

const STATE_DIR = process.env.DEVBOX_STATE_DIR || path.join(os.homedir(), '.local/state/devbox');
const CONFIG_DIR = process.env.DEVBOX_CONFIG_DIR || path.join(os.homedir(), '.config/devbox');
fs.mkdirSync(STATE_DIR, { recursive: true });
fs.mkdirSync(CONFIG_DIR, { recursive: true, mode: 0o700 });

function readJson(file, def) {
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return def; }
}
function writeJson(file, value, mode) {
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(value, null, 2), mode ? { mode } : undefined);
  fs.renameSync(tmp, file);
}

class JsonFile {
  constructor(file, def, mode) {
    this.file = file; this.mode = mode;
    this.data = readJson(file, def);
    this.timer = null;
  }
  save() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.flush(), 150);
  }
  flush() { clearTimeout(this.timer); writeJson(this.file, this.data, this.mode); }
}

const EVENTS = path.join(STATE_DIR, 'events.jsonl');
function appendEvent(ev) {
  try {
    if (fs.existsSync(EVENTS) && fs.statSync(EVENTS).size > 20e6) fs.renameSync(EVENTS, `${EVENTS}.1`);
    fs.appendFileSync(EVENTS, `${JSON.stringify({ at: Date.now(), ...ev })}\n`);
  } catch { /* never let logging break the hub */ }
}

module.exports = { STATE_DIR, CONFIG_DIR, readJson, writeJson, JsonFile, appendEvent, EVENTS };

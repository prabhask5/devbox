'use strict';
// Web Push (VAPID). Works with iOS home-screen web apps (16.4+), Android and desktop browsers.
const webpush = require('web-push');

function init(config) {
  if (!config.data.vapid) {
    config.data.vapid = webpush.generateVAPIDKeys();
    config.flush();
  }
  webpush.setVapidDetails(config.data.vapidSubject || 'mailto:devbox@localhost', config.data.vapid.publicKey, config.data.vapid.privateKey);
}

async function sendAll(subs, payload) {
  const body = JSON.stringify(payload);
  const dead = [];
  await Promise.all(subs.data.list.map(async (s) => {
    try {
      await webpush.sendNotification(s.subscription, body, { TTL: 3600, urgency: payload.urgency || 'normal', topic: payload.topic });
      s.lastOk = Date.now();
    } catch (e) {
      if (e.statusCode === 404 || e.statusCode === 410) dead.push(s.subscription.endpoint);
      else console.error('push failed', e.statusCode || '', e.body || e.message);
    }
  }));
  if (dead.length) subs.data.list = subs.data.list.filter((s) => !dead.includes(s.subscription.endpoint));
  subs.save();
}

module.exports = { init, sendAll };

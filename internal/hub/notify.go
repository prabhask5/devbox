package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/webpush"
)

const (
	settingFinishedAfter = "finishedNotifyAfterSec" // a turn at least this long is announced when it ends
	defaultFinishedAfter = 30
	settingSessionEpoch  = "sessionEpoch" // part of what app session cookies sign; raising it signs everyone out
	pushTTL              = time.Hour
)

// setUpPush makes the hub able to send notifications: with the keys in its secrets, or with
// new ones on its first run.
func (h *Hub) setUpPush() error {
	keys := webpush.Keys{}
	if h.opt.Secrets.VapidPublicKey != nil && h.opt.Secrets.VapidPrivateKey != nil {
		keys = webpush.Keys{Public: *h.opt.Secrets.VapidPublicKey, Private: h.opt.Secrets.VapidPrivateKey.Expose()}
	} else {
		var err error
		if keys, err = webpush.GenerateKeys(); err != nil {
			return err
		}
		if h.opt.SaveVAPID != nil {
			if err := h.opt.SaveVAPID(keys); err != nil {
				return fmt.Errorf("saving the web push keys: %w", err)
			}
		}
	}
	client := h.opt.PushClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	h.push = &webpush.Sender{Keys: keys, Contact: "https://" + h.opt.Config.Hosts.Agents, Client: client}
	return nil
}

// badge is the number the app's icon shows: sessions that wait for the owner or have news.
// The caller holds mu.
func (h *Hub) badge() int {
	count := 0
	for _, s := range h.sessions {
		if s.State == proto.SessionStateNeedsInput || s.Unread {
			count++
		}
	}
	return count
}

func label(s *session) string {
	if s.Title != "" {
		return s.Name + " — " + clip(s.Title, 60)
	}
	return s.Name
}

// notify tells the owner's devices about a session, unless they are looking at it or were
// told the same kind of thing moments ago. The caller holds mu.
func (h *Hub) notify(s *session, kind, title, body string, urgent bool) {
	if h.viewers[s.ID] > 0 {
		return
	}
	key := s.ID + ":" + kind
	if last, told := h.notified[key]; told && h.now().Sub(last) < h.t.NotifyThrottle {
		return
	}
	h.notified[key] = h.now()
	s.Unread = true
	h.persist(s)
	h.deliver(proto.PushPayload{
		V: proto.PushPayloadVersion, Title: title, Body: clip(body, 180), URL: "/#/s/" + s.ID, Tag: "s-" + s.ID, Badge: h.badge(),
	}, urgent)
	h.record("notify", map[string]any{"sessionId": s.ID, "kind": kind, "title": title})
}

// deliver sends a notification to every registered device, without waiting for the push
// services. Devices whose subscription is gone are forgotten.
func (h *Hub) deliver(payload proto.PushPayload, urgent bool) {
	message, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.spawn(func() {
		ctx := context.Background()
		subscriptions, err := h.store.subscriptions(ctx)
		if err != nil {
			h.log.Warn("push subscriptions not read", "error", err)
			return
		}
		for _, subscription := range subscriptions {
			err := h.push.Send(ctx, subscription, webpush.Message{Payload: message, TTL: pushTTL, Urgent: urgent}, h.now())
			switch {
			case errors.Is(err, webpush.ErrGone):
				if err := h.store.unsubscribe(ctx, subscription.Endpoint); err == nil {
					h.changed()
				}
			case err != nil:
				h.log.Warn("notification not sent", "error", err)
			}
		}
	})
}

// notifyNeedsInput announces that a session waits for the owner — after a delay, and only if
// it still waits for the same thing: a question answered within seconds, or by someone sitting
// at an attached terminal, needs no notification. The caller holds mu.
func (h *Hub) notifyNeedsInput(s *session) {
	id, since, attached := s.ID, s.StateSince, s.Attached
	delay := h.t.NeedsInputDelay
	if attached {
		delay = h.t.NeedsInputDelayAttached
	}
	h.spawn(func() {
		select {
		case <-h.closing:
			return
		case <-time.After(delay):
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		current := h.sessions[id]
		if current == nil || current.State != proto.SessionStateNeedsInput || current.StateSince != since {
			return
		}
		what := "Needs your input"
		if current.WaitingFor != nil {
			what = *current.WaitingFor
		}
		h.notify(current, "needs_input", "🔐 "+label(current), what, true)
	})
}

// notifyFinished announces the end of a turn that took long enough to walk away from. The
// caller holds mu.
func (h *Hub) notifyFinished(s *session, seconds int, reply string) {
	if seconds < h.store.setting(context.Background(), settingFinishedAfter, defaultFinishedAfter) {
		return
	}
	if reply == "" {
		adapter, _ := AdapterByID(s.Agent)
		name := adapter.Label
		if name == "" {
			name = s.Agent
		}
		reply = fmt.Sprintf("%s finished after %ds", name, seconds)
	}
	h.notify(s, "done", "✅ "+label(s), reply, false)
}

// notifySync announces repositories a sync could not bring up to date.
func (h *Hub) notifySync(results []proto.SyncResult) {
	var problems []string
	for _, result := range results {
		if result.Status == proto.SyncStatusConflict || result.Status == proto.SyncStatusError {
			problems = append(problems, result.Name+": "+result.Detail)
		}
	}
	if len(problems) == 0 {
		return
	}
	title := fmt.Sprintf("⚠️ %d repos couldn't sync", len(problems))
	if len(problems) == 1 {
		title = "⚠️ 1 repo couldn't sync"
	}
	h.mu.Lock()
	badge := h.badge()
	h.mu.Unlock()
	h.deliver(proto.PushPayload{
		V: proto.PushPayloadVersion, Title: title, Body: clip(strings.Join(problems, "\n"), 180), URL: "/#/", Tag: "devbox", Badge: badge,
	}, false)
}

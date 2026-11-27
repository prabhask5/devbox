import { useEffect, useState } from "preact/hooks";
import type { Session } from "@devbox/shared/proto/types";
import { LETTER, STATE_LABEL, ago, groups, pill, sessionLine } from "../format.ts";
import { Gear, List, Plus, Spark } from "../icons.tsx";
import type { AppState } from "../store.ts";
import { NewSession } from "./NewSession.tsx";
import { Settings } from "./Settings.tsx";

const TITLE = { sessions: "Agents", assistant: "Assistant", settings: "Settings" };

function go(hash: string) {
  location.hash = hash;
}

function Row({ s, now }: { s: Session; now: number }) {
  return (
    <button
      class={`item ${s.state === "needs_input" ? "needs" : ""}`}
      onClick={() => {
        go(`#/s/${s.id}`);
      }}
    >
      <span class={`badge ${s.agent}`}>
        {LETTER[s.agent] ?? "?"}
        {(s.state === "working" || s.state === "starting") && <span class="spin" />}
      </span>
      <span class="main">
        <span class="row1">
          <span class="name">{s.name}</span>
          {s.unread && <span class="unread" aria-label="Unread" />}
          <span class="when">{ago(s.stateSince, now)}</span>
        </span>
        <span class="sub">{sessionLine(s)}</span>
        <span class="meta">
          <span class={`chip ${s.state}`}>{STATE_LABEL[s.state]}</span>
          {s.repo && <span class="chip">{s.repo}</span>}
          {s.kind === "external" && <span class="chip">outside tmux</span>}
          {s.attached && <span class="chip">attached</span>}
        </span>
      </span>
    </button>
  );
}

function Sessions({ sessions, now }: { sessions: Session[]; now: number }) {
  if (sessions.length === 0) {
    return (
      <div class="empty">
        <b>No sessions yet</b>Start Claude Code, Codex or opencode in one of your repos with the +
        button. Sessions keep running when you close the app.
      </div>
    );
  }
  return (
    <>
      {groups(sessions).map((group) => (
        <>
          <div class="section">
            <span>{group.name}</span>
            <span>{group.sessions.length}</span>
          </div>
          <div class="list">
            {group.sessions.map((s) => (
              <Row key={s.id} s={s} now={now} />
            ))}
          </div>
        </>
      ))}
    </>
  );
}

function Assistant() {
  return (
    <div class="soon">
      <div class="orb" />
      <h2>Your assistant lives here</h2>
      <p>
        This tab is reserved for a personal assistant running on your own model — a chat you control
        fully from your phone, alongside your coding agents.
      </p>
      <p style="color:var(--faint);font-size:13px">
        The hub already supports “chat” agents next to terminal ones; plugging a local model in is a
        new adapter, not a new app.
      </p>
    </div>
  );
}

/** The three tabs: the session list, the assistant placeholder and the settings. */
export function Main({ app, tab }: { app: AppState; tab: keyof typeof TITLE }) {
  const [sheet, setSheet] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    // Relative times move on even when nothing else changes.
    const timer = setInterval(() => {
      setNow(Date.now());
    }, 30000);
    return () => {
      clearInterval(timer);
    };
  }, []);
  const state = app.state;
  if (!state) return null;
  const status = pill(state.devbox, app.gateway);
  return (
    <>
      <div class="top">
        <h1>{TITLE[tab]}</h1>
        <span id="pill">
          <span class="pill">
            <span class={`dot ${status.dot}`} />
            {status.text}
          </span>
        </span>
      </div>
      <div class="scroll">
        {tab === "sessions" && (
          <Sessions sessions={state.sessions} now={Math.max(now, Date.now())} />
        )}
        {tab === "assistant" && <Assistant />}
        {tab === "settings" && <Settings state={state} gateway={app.gateway} />}
      </div>
      {tab === "sessions" && (
        <button
          class="fab"
          aria-label="New session"
          onClick={() => {
            setSheet(true);
          }}
        >
          <Plus />
        </button>
      )}
      <nav class="tabs">
        <button
          class={tab === "sessions" ? "on" : ""}
          onClick={() => {
            go("#/");
          }}
        >
          <List />
          Sessions
        </button>
        <button
          class={tab === "assistant" ? "on" : ""}
          onClick={() => {
            go("#/assistant");
          }}
        >
          <Spark />
          Assistant
        </button>
        <button
          class={tab === "settings" ? "on" : ""}
          onClick={() => {
            go("#/settings");
          }}
        >
          <Gear />
          Settings
        </button>
      </nav>
      {sheet && (
        <NewSession
          adapters={state.adapters}
          onClose={() => {
            setSheet(false);
          }}
        />
      )}
    </>
  );
}

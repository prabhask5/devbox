import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { ScreenFrame } from "@devbox/shared/proto/types";
import { ansiToHtml } from "../ansi.ts";
import { STATE_LABEL, tone } from "../format.ts";
import { Back, More, Send } from "../icons.tsx";
import { remember, remembered } from "../local.ts";
import { api, report, session, toast } from "../store.ts";
import type { AppState } from "../store.ts";

/** One session: what it asks, its live screen, quick keys and the composer. */
export function SessionView({ id, app }: { id: string; app: AppState }) {
  const s = session(app, id);
  const adapter = app.state?.adapters.find((a) => a.id === s?.agent);
  const [fontSize, setFontSize] = useState(() => remembered("fs", 12));
  const [menu, setMenu] = useState(false);
  const [frame, setFrame] = useState<ScreenFrame>();
  const [generation, setGeneration] = useState(0); // raised to open the screen stream anew
  const root = useRef<HTMLDivElement>(null);
  const screen = useRef<HTMLDivElement>(null);
  const composer = useRef<HTMLTextAreaElement>(null);
  const atBottom = useRef(true);

  // The screen stream, sized for this screen and reopened (with backoff) when it breaks.
  useEffect(() => {
    const area = screen.current;
    const host = root.current;
    if (!area || !host) return undefined;
    const probe = document.createElement("span");
    probe.style.cssText = `position:absolute;visibility:hidden;font-family:var(--mono);font-size:${String(fontSize)}px;white-space:pre`;
    probe.textContent = "M".repeat(40);
    host.appendChild(probe);
    const width = probe.getBoundingClientRect().width / 40 || fontSize * 0.6;
    probe.remove();
    const cols = Math.max(30, Math.floor((area.clientWidth - 20) / width));
    const rows = Math.max(12, Math.floor((area.clientHeight - 16) / (fontSize * 1.32)));

    let events: EventSource | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let delay = 1000;
    const open = () => {
      events = new EventSource(
        `/api/sessions/${id}/screen?cols=${String(cols)}&rows=${String(rows)}`,
      );
      events.onmessage = (event: MessageEvent<string>) => {
        delay = 1000;
        setFrame(JSON.parse(event.data) as ScreenFrame);
      };
      events.onerror = () => {
        events?.close();
        retry = setTimeout(open, delay);
        delay = Math.min(delay * 2, 15000);
      };
    };
    open();
    return () => {
      events?.close();
      clearTimeout(retry);
    };
  }, [id, fontSize, generation]);

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const reopen = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        setGeneration((n) => n + 1);
      }, 400);
    };
    const visible = () => {
      if (document.visibilityState === "visible") reopen();
    };
    const closeMenu = () => {
      setMenu(false);
    };
    window.addEventListener("resize", reopen);
    document.addEventListener("visibilitychange", visible);
    document.addEventListener("click", closeMenu);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("resize", reopen);
      document.removeEventListener("visibilitychange", visible);
      document.removeEventListener("click", closeMenu);
    };
  }, []);

  // Follow the output while the reader is at the bottom.
  useLayoutEffect(() => {
    const area = screen.current;
    if (area && atBottom.current) area.scrollTop = area.scrollHeight;
  }, [frame]);

  const send = (body: { keys: string[] } | { text: string }) =>
    api(`/api/sessions/${id}/input`, { method: "POST", body }).then(() => {
      atBottom.current = true;
    });
  const sendText = () => {
    const field = composer.current;
    if (!field) return;
    const text = field.value;
    if (!text.trim()) {
      send({ keys: ["Enter"] }).catch(report);
      return;
    }
    field.value = "";
    field.style.height = "auto";
    send({ text }).catch((failure: unknown) => {
      field.value = text;
      report(failure);
    });
  };
  const resize = (size: number) => {
    const next = Math.max(8, Math.min(20, size));
    remember("fs", next);
    setFontSize(next);
  };

  const terminal = s?.kind === "tmux" && s.state !== "gone";
  const subtitle = s
    ? STATE_LABEL[s.state] +
      (s.repo ? ` · ${s.repo}` : "") +
      (s.attached ? " · attached elsewhere" : "")
    : "";
  return (
    <div ref={root} class="sess" style={{ "--fs": `${String(fontSize)}px` }}>
      <div class="sess-top">
        <button
          class="icon-btn"
          aria-label="Back"
          onClick={() => {
            location.hash = "#/";
          }}
        >
          <Back />
        </button>
        <div class="sess-title">
          <b>{s ? s.name : "Session not found"}</b>
          <span>{subtitle}</span>
        </div>
        <button
          class="icon-btn"
          aria-label="More"
          onClick={(event) => {
            event.stopPropagation();
            setMenu(!menu);
          }}
        >
          <More />
        </button>
      </div>
      <div>
        {s?.state === "needs_input" && (
          <div class="ask">
            <p>🔐 {s.waitingFor ?? "Waiting for your input"}</p>
            <div class="row">
              {(adapter?.answerKeys ?? []).map((key, index) => (
                <button
                  key={key.label}
                  class={tone(key, index)}
                  onClick={() => {
                    send({ keys: [key.key] }).catch(report);
                  }}
                >
                  {key.label}
                </button>
              ))}
            </div>
          </div>
        )}
        {s?.state === "exited" && (
          <div class="ask" style="background:#ffffff08;border-color:var(--line2)">
            <p style="color:var(--dim)">The agent exited; the pane is a shell now.</p>
            <div class="row">
              <button
                class="ok"
                onClick={() => {
                  api(`/api/sessions/${id}/restart`, { method: "POST" }).catch(report);
                }}
              >
                Restart {adapter?.label ?? s.agent}
              </button>
            </div>
          </div>
        )}
      </div>
      <div
        ref={screen}
        class="screen"
        onScroll={(event) => {
          const area = event.currentTarget;
          atBottom.current = area.scrollTop + area.clientHeight >= area.scrollHeight - 40;
        }}
      >
        {frame?.text !== undefined ? (
          <pre dangerouslySetInnerHTML={{ __html: ansiToHtml(frame.text) }} />
        ) : (
          <pre>
            {frame && (
              <div class="msg">
                {frame.reason === "external" ? (
                  <>
                    This {adapter?.label ?? "agent"} session runs outside tmux (e.g. a VS Code
                    terminal). You still get its notifications, but answer it where it runs.
                    <br />
                    <br />
                    Tip: start agents from this app or with <code>agent new</code> to control them
                    from anywhere.
                  </>
                ) : (
                  "This tmux session has ended."
                )}
              </div>
            )}
          </pre>
        )}
      </div>
      <div class={`keys ${terminal ? "" : "hidden"}`}>
        {(app.state?.commonKeys ?? []).map((key) => (
          <button
            key={key.label}
            onClick={() => {
              send({ keys: [key.key] }).catch(report);
            }}
          >
            {key.label}
          </button>
        ))}
      </div>
      <div class={`composer ${terminal ? "" : "hidden"}`}>
        <textarea
          ref={composer}
          rows={1}
          placeholder="Message"
          aria-label="Message"
          enterkeyhint="send"
          autocapitalize="sentences"
          onInput={(event) => {
            const field = event.currentTarget;
            field.style.height = "auto";
            field.style.height = `${String(Math.min(140, field.scrollHeight))}px`;
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
              event.preventDefault();
              sendText();
            }
          }}
        />
        <button class="send" aria-label="Send" onClick={sendText}>
          <Send />
        </button>
      </div>
      {menu && s && (
        <div class="menu" role="menu">
          <button
            onClick={() => {
              const name = prompt("Name", s.name);
              if (name) {
                api(`/api/sessions/${id}/rename`, { method: "POST", body: { name } }).catch(report);
              }
            }}
          >
            Rename
          </button>
          <button
            onClick={() => {
              resize(fontSize + 1);
            }}
          >
            Bigger text
          </button>
          <button
            onClick={() => {
              resize(fontSize - 1);
            }}
          >
            Smaller text
          </button>
          {s.cwd && (
            <button
              onClick={() => {
                const host = location.hostname.replace(/^agents\./, "code.");
                window.open(`https://${host}/?folder=${encodeURIComponent(s.cwd)}`, "_blank");
              }}
            >
              Open folder in VS Code
            </button>
          )}
          {s.kind === "tmux" && s.tmux && (
            <button
              onClick={() => {
                void navigator.clipboard.writeText(`tmux attach -t ${s.tmux ?? ""}`).then(() => {
                  toast("Copied");
                }, report);
              }}
            >
              Copy attach command
            </button>
          )}
          <button
            class="bad"
            onClick={() => {
              const question = terminal
                ? `Kill "${s.name}"? The agent and its tmux session will be stopped.`
                : `Remove "${s.name}" from the list?`;
              if (!confirm(question)) return;
              api(`/api/sessions/${id}`, { method: "DELETE" })
                .then(() => {
                  location.hash = "#/";
                })
                .catch(report);
            }}
          >
            {terminal ? "Kill session" : "Remove"}
          </button>
        </div>
      )}
    </div>
  );
}

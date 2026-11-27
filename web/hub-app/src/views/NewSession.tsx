import { useEffect, useRef, useState } from "preact/hooks";
import type { AdapterInfo, Session } from "@devbox/shared/proto/types";
import { remember, remembered } from "../local.ts";
import { api, created } from "../store.ts";
import type { Repo } from "../store.ts";

let draft = ""; // a prompt typed into the sheet survives closing it

function detail(repo: Repo): string {
  if (!repo.git) return "";
  return (
    (repo.branch ?? "") +
    (repo.dirty ? ` · ${String(repo.dirty)}✎` : "") +
    (repo.ahead ? ` · ↑${String(repo.ahead)}` : "")
  );
}

/** The sheet that starts a session: which agent, where, and optionally what to do first. */
export function NewSession({
  adapters,
  onClose,
}: {
  adapters: AdapterInfo[];
  onClose: () => void;
}) {
  const terminals = adapters.filter((a) => a.kind === "tmux");
  const [agent, setAgent] = useState(() => {
    const last = remembered("lastAgent", "claude");
    if (terminals.find((a) => a.id === last)?.available) return last;
    return terminals.find((a) => a.available)?.id ?? "shell";
  });
  const [repos, setRepos] = useState<Repo[]>();
  const [repo, setRepo] = useState<string>(); // "" is the home directory
  const [filter, setFilter] = useState("");
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
  const prompt = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    api<{ repos: Repo[] }>("/api/repos")
      .then((listing) => {
        setRepos(listing.repos);
        setRepo((chosen) => chosen ?? listing.repos[0]?.name ?? "");
      })
      .catch(() => {
        setRepos([]);
        setRepo("");
      });
  }, []);

  const start = () => {
    setStarting(true);
    const text = prompt.current?.value.trim();
    api<Session>("/api/sessions", {
      method: "POST",
      body: { agent, ...(repo ? { cwd: repo } : {}), ...(text ? { prompt: text } : {}) },
    })
      .then((session) => {
        remember("lastAgent", agent);
        draft = "";
        created(session);
        onClose();
        location.hash = `#/s/${session.id}`;
      })
      .catch((failure: unknown) => {
        setError(failure instanceof Error ? failure.message : String(failure));
        setStarting(false);
      });
  };

  const shown = (repos ?? []).filter((r) => r.name.toLowerCase().includes(filter.toLowerCase()));
  return (
    <>
      <div class="sheet-bg" onClick={onClose} />
      <div class="sheet" role="dialog" aria-label="New session">
        <div class="grab" />
        <h2>New session</h2>
        <span class="label">Agent</span>
        <div class="seg">
          {terminals.map((a) => (
            <button
              key={a.id}
              class={a.id === agent ? "on" : ""}
              disabled={!a.available}
              onClick={() => {
                setAgent(a.id);
              }}
            >
              {a.label}
            </button>
          ))}
        </div>
        <span class="label">Where</span>
        {(repos ?? []).length > 7 && (
          <input
            class="field"
            placeholder="Filter repos"
            aria-label="Filter repos"
            value={filter}
            style="margin-bottom:8px"
            onInput={(event) => {
              setFilter(event.currentTarget.value);
            }}
          />
        )}
        <div class="repos">
          {repos === undefined && <div class="repo">Loading…</div>}
          {shown.map((r) => (
            <button
              key={r.name}
              class={`repo ${repo === r.name ? "on" : ""}`}
              onClick={() => {
                setRepo(r.name);
              }}
            >
              <span>{r.name}</span>
              <small>{detail(r)}</small>
            </button>
          ))}
          <button
            class={`repo ${repo === "" ? "on" : ""}`}
            onClick={() => {
              setRepo("");
            }}
          >
            <span>Home (~)</span>
            <small />
          </button>
        </div>
        <span class="label">First prompt (optional)</span>
        <textarea
          ref={prompt}
          class="field"
          placeholder="What should it do?"
          aria-label="First prompt"
          enterkeyhint="go"
          defaultValue={draft}
          onInput={(event) => {
            draft = event.currentTarget.value;
          }}
        />
        <div style="height:14px" />
        <button class="btn block" disabled={starting} onClick={start}>
          Start
        </button>
        <div class="err" role="alert">
          {error}
        </div>
      </div>
    </>
  );
}

import { useAppState } from "./store.ts";
import { Asleep, Login, Offline } from "./views/Gate.tsx";
import { Main } from "./views/Main.tsx";
import { SessionView } from "./views/SessionView.tsx";

function Screen() {
  const app = useAppState();
  switch (app.mode) {
    case "loading":
      return (
        <div class="center">
          <p>Connecting…</p>
        </div>
      );
    case "login":
      return <Login />;
    case "asleep":
      return <Asleep info={app.asleep} />;
    case "offline":
      return <Offline />;
    case "app":
      return app.route.name === "session" ? (
        <SessionView key={app.route.id} id={app.route.id} app={app} />
      ) : (
        <Main app={app} tab={app.route.name} />
      );
  }
}

export function App() {
  const app = useAppState();
  return (
    <>
      <Screen />
      {app.toast !== undefined && (
        <div class="toast" role="status">
          {app.toast}
        </div>
      )}
      {app.updated && app.toast === undefined && (
        <button
          class="toast update"
          onClick={() => {
            location.reload();
          }}
        >
          Updated — tap to reload
        </button>
      )}
    </>
  );
}

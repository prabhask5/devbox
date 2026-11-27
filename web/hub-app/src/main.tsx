import { render } from "preact";
import { App } from "./App.tsx";
import { start } from "./store.ts";
import "./styles.css";

const root = document.getElementById("app");
if (root) render(<App />, root);
start();

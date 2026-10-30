// The Gauntlet roll page. The server picks the game; the wheel only spins to
// that pick. Controls: keyboard, mouse, or either gamepad (A = roll/play,
// B = reroll).
import { Wheel } from "/static/spin-wheel-esm.js";

const WHEEL_SLOTS = 16;
const SPIN_MS = 6500;
const SPIN_TURNS = 6;
const AUTO_LAUNCH_SECONDS = 6;
const MATCHUP_KEY = "gauntlet.matchup";
const COLORS = ["#ff4d6d", "#3ec9ff", "#ffd23f", "#9b5de5", "#00f5a0", "#ff8c42"];
const LABEL_COLORS = ["#0d0b14", "#0d0b14", "#0d0b14", "#f4f1ff", "#0d0b14", "#0d0b14"];

const $ = (id) => document.getElementById(id);
const el = {
  p1: $("p1"),
  p2: $("p2"),
  status: $("status"),
  result: $("result"),
  name: $("game-name"),
  platform: $("game-platform"),
  countdown: $("countdown"),
  roll: $("roll"),
  rollLabel: $("roll-label"),
  reroll: $("reroll"),
  poolSize: $("pool-size"),
};

let state = "loading"; // loading | idle | spinning | landed | launching | error
let current = null; // the game the wheel is spinning to, or landed on
let rolled = []; // ids rolled for this matchup, skipped by rerolls
let countdownTimer = null;

const wheel = new Wheel($("wheel"), {
  items: [{ label: "" }],
  isInteractive: false,
  itemBackgroundColors: COLORS,
  itemLabelColors: LABEL_COLORS,
  itemLabelFont: '"Avenir Next", "Segoe UI", system-ui, sans-serif',
  itemLabelFontSizeMax: 34,
  itemLabelRadius: 0.92,
  itemLabelRadiusMax: 0.28,
  lineColor: "#0d0b14",
  lineWidth: 3,
  borderColor: "#0d0b14",
  borderWidth: 6,
  rotationResistance: 0,
  onRest: (event) => {
    if (event.method === "spintoitem" && state === "spinning") land();
  },
});

// --- Matchup ----------------------------------------------------------------

function loadMatchup() {
  try {
    const saved = JSON.parse(localStorage.getItem(MATCHUP_KEY) || "{}");
    el.p1.value = saved.p1 || "";
    el.p2.value = saved.p2 || "";
  } catch {
    // Storage unavailable; the inputs just start empty.
  }
}

function saveMatchup() {
  rolled = [];
  try {
    localStorage.setItem(MATCHUP_KEY, JSON.stringify({ p1: el.p1.value, p2: el.p2.value }));
  } catch {
    // Not fatal: the matchup only lives in the inputs.
  }
}

for (const input of [el.p1, el.p2]) {
  input.addEventListener("change", saveMatchup);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === "Escape") input.blur();
    e.stopPropagation();
  });
}

// --- Wheel ------------------------------------------------------------------

function shuffle(list) {
  const a = [...list];
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
}

function label(game) {
  const name = String(game.name);
  return name.length > 22 ? `${name.slice(0, 21)}…` : name;
}

// Fills the wheel from the pool. With a pick, it gets a slot at a random
// position and its index is returned.
function setWheel(games, pick = null) {
  let slots = shuffle(games.filter((g) => !pick || g.id !== pick.id)).slice(
    0,
    pick ? WHEEL_SLOTS - 1 : WHEEL_SLOTS,
  );
  let index = -1;
  if (pick) {
    index = Math.floor(Math.random() * (slots.length + 1));
    slots.splice(index, 0, pick);
  }
  if (slots.length === 0) slots = [{ name: "No games" }];
  wheel.items = slots.map((g) => ({ label: label(g), value: g.id }));
  return index;
}

function idleSpin() {
  wheel.stop();
  wheel.spin(12);
}

// --- Flow -------------------------------------------------------------------

function setState(next) {
  state = next;
  document.body.dataset.state = next;
  el.roll.disabled = !["idle", "landed"].includes(next);
  el.rollLabel.textContent = next === "landed" ? "Play now" : "Roll";
  el.reroll.hidden = next !== "landed";
}

function showError(message) {
  el.status.textContent = message;
  el.status.hidden = false;
  el.result.hidden = true;
  setState("error");
  // A broken token or missing collection gets fixed in RomM; retry quietly.
  setTimeout(loadPool, 10000);
}

async function api(path, options) {
  const response = await fetch(path, options);
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

async function loadPool() {
  if (!["loading", "error"].includes(state)) return;
  try {
    const { collection, games } = await api("/api/pool");
    setWheel(games);
    idleSpin();
    el.poolSize.textContent = `${games.length} games in ${collection}`;
    el.status.textContent = games.length ? "Ready when you are." : `Add games to ${collection} in RomM.`;
    setState(games.length ? "idle" : "error");
  } catch (e) {
    showError(e.message);
  }
}

async function roll() {
  stopCountdown();
  setState("spinning");
  el.result.hidden = true;
  el.status.hidden = false;
  el.status.textContent = "Spinning…";
  try {
    const { game, games } = await api("/api/roll", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ exclude: rolled }),
    });
    current = game;
    rolled.push(game.id);
    el.poolSize.textContent = `${games.length} games`;
    const index = setWheel(games, game);
    wheel.stop();
    wheel.spinToItem(index, SPIN_MS, false, SPIN_TURNS, 1, (n) => 1 - Math.pow(1 - n, 4));
  } catch (e) {
    showError(e.message);
  }
}

function land() {
  el.name.textContent = current.name;
  el.platform.textContent = current.platform;
  el.status.hidden = true;
  el.result.hidden = false;
  setState("landed");
  startCountdown();
}

function startCountdown() {
  let left = AUTO_LAUNCH_SECONDS;
  const tick = () => {
    if (left <= 0) return launch();
    el.countdown.textContent = `Launching in ${left}…`;
    left--;
  };
  tick();
  countdownTimer = setInterval(tick, 1000);
}

function stopCountdown(message = "") {
  clearInterval(countdownTimer);
  countdownTimer = null;
  el.countdown.textContent = message;
}

function launch() {
  if (!current) return;
  stopCountdown("Loading game…");
  setState("launching");
  window.location.assign(current.playUrl);
}

function primary() {
  if (state === "idle") roll();
  else if (state === "landed") launch();
}

function reroll() {
  if (state === "landed") roll();
}

el.roll.addEventListener("click", primary);
el.reroll.addEventListener("click", reroll);

document.addEventListener("keydown", (e) => {
  if (e.repeat) return;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    primary();
  } else if (e.key === "r" || e.key === "R") {
    reroll();
  } else if (e.key === "Escape" && state === "landed") {
    stopCountdown("Countdown stopped.");
  }
});

// --- Gamepads ---------------------------------------------------------------

const GAMEPAD_A = 0;
const GAMEPAD_B = 1;
const GAMEPAD_START = 9;
const held = new Map();

function pollGamepads() {
  for (const pad of navigator.getGamepads?.() ?? []) {
    if (!pad) continue;
    for (const [button, action] of [
      [GAMEPAD_A, primary],
      [GAMEPAD_START, primary],
      [GAMEPAD_B, reroll],
    ]) {
      const key = `${pad.index}:${button}`;
      const pressed = Boolean(pad.buttons[button]?.pressed);
      if (pressed && !held.get(key) && document.activeElement?.tagName !== "INPUT") action();
      held.set(key, pressed);
    }
  }
  requestAnimationFrame(pollGamepads);
}

// Coming back from RomM with the browser's back button restores this page
// from the bfcache mid-"launching"; start fresh instead.
window.addEventListener("pageshow", (e) => {
  if (e.persisted && state === "launching") {
    stopCountdown();
    el.result.hidden = true;
    el.status.hidden = false;
    el.status.textContent = "Ready when you are.";
    setState("idle");
    idleSpin();
  }
});

loadMatchup();
requestAnimationFrame(pollGamepads);
loadPool();

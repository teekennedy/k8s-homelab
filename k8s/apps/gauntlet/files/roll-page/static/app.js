// The Gauntlet TV page. The server picks the game; the wheel only spins to
// that pick. With a tournament running, the matchup comes from the bracket the
// referee manages at /ref; without one, the names are typed in.
//
// Controls: keyboard, mouse, or any gamepad. A = roll / start, B = reroll,
// X / Y = veto for the left / right player.
import { Wheel } from "/static/spin-wheel-esm.js";
import { rebuild } from "/static/bracket.js";
import { createManager, mutate, subscribe, tournament, watch } from "/static/store.js";

const WHEEL_SLOTS = 16;
const SPIN_MS = 6500;
const SPIN_TURNS = 6;
const MATCHUP_KEY = "gauntlet.matchup";
const COLORS = ["#ff4d6d", "#3ec9ff", "#ffd23f", "#9b5de5", "#00f5a0", "#ff8c42"];
const LABEL_COLORS = ["#0d0b14", "#0d0b14", "#0d0b14", "#f4f1ff", "#0d0b14", "#0d0b14"];

const $ = (id) => document.getElementById(id);
const el = {
  p1: $("p1"),
  p2: $("p2"),
  stage: $("stage"),
  status: $("status"),
  result: $("result"),
  name: $("game-name"),
  platform: $("game-platform"),
  roll: $("roll"),
  rollLabel: $("roll-label"),
  reroll: $("reroll"),
  vetoes: $("vetoes"),
  veto: [$("veto1"), $("veto2")],
  vetoName: [$("veto1-name"), $("veto2-name")],
  vetoHint: $("veto-hint"),
  poolSize: $("pool-size"),
};

let state = "loading"; // loading | idle | spinning | landed | launching | error
let current = null; // the game the wheel is spinning to, or landed on
let rolled = []; // ids rolled for this matchup, skipped by later rolls
// The bracket match on the TV: { id, players: [index, index] }, or null when
// there's no tournament (manual names) or nothing to play.
let match = null;
let rebuildSeq = 0;

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
  // Only the spin to the pick ever rests: the idle spin has no resistance,
  // and stop() raises no event.
  onRest: () => {
    if (state === "spinning") land();
  },
});

// --- Matchup ----------------------------------------------------------------

function loadManualMatchup() {
  try {
    const saved = JSON.parse(localStorage.getItem(MATCHUP_KEY) || "{}");
    el.p1.value = saved.p1 || "";
    el.p2.value = saved.p2 || "";
  } catch {
    // Storage unavailable; the inputs just start empty.
  }
}

function saveManualMatchup() {
  rolled = [];
  try {
    localStorage.setItem(MATCHUP_KEY, JSON.stringify({ p1: el.p1.value, p2: el.p2.value }));
  } catch {
    // Not fatal: the matchup only lives in the inputs.
  }
}

for (const input of [el.p1, el.p2]) {
  input.addEventListener("change", saveManualMatchup);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === "Escape") input.blur();
    e.stopPropagation();
  });
}

function showMatchup(names, label, editable, solo = false) {
  document.body.classList.toggle("solo", solo);
  el.p1.value = names[0];
  el.p2.value = names[1];
  el.p1.readOnly = el.p2.readOnly = !editable;
  el.stage.textContent = label;
  el.stage.hidden = !label;
}

// Follows the tournament record: fills in the names, and resets the roll when
// the referee moves the TV on to another match.
async function onTournament(t) {
  const seq = ++rebuildSeq;
  if (!t) {
    if (match !== null || state === "loading") loadManualMatchup();
    match = null;
    showMatchup([el.p1.value, el.p2.value], "", true);
    renderVetoes();
    return;
  }

  let bracket;
  try {
    bracket = await rebuild(t, createManager);
  } catch (e) {
    showMatchup(["", ""], `Bracket error: ${e.message}`, false);
    return;
  }
  if (seq !== rebuildSeq) return;

  if (bracket.champion != null) {
    setMatch(null);
    showMatchup([t.players[bracket.champion], ""], "Champion of the Gauntlet", false, true);
  } else if (bracket.tv) {
    const players = [bracket.tv.opponent1.id, bracket.tv.opponent2.id];
    setMatch({ id: bracket.tv.id, players });
    showMatchup(
      players.map((p) => t.players[p]),
      bracket.labels.get(bracket.tv.id),
      false,
    );
  } else {
    setMatch(null);
    showMatchup(["", ""], "Waiting for the referee", false);
  }
  renderVetoes();
}

function setMatch(next) {
  const changed = match?.id !== next?.id;
  match = next;
  if (!changed) return;
  rolled = [];
  if (["landed", "spinning"].includes(state)) {
    // The referee moved on; drop the game rolled for the previous match.
    wheel.stop();
    showReady();
  }
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
  el.rollLabel.textContent = next === "landed" ? "Start" : "Roll";
  el.reroll.hidden = next !== "landed";
  renderVetoes();
}

function renderVetoes() {
  const t = tournament();
  const show = Boolean(t && match);
  el.vetoHint.hidden = !show;
  el.vetoes.hidden = !show || state !== "landed";
  if (!show) return;
  match.players.forEach((player, side) => {
    const used = t.vetoes.includes(player);
    el.vetoName[side].textContent = used ? `${t.players[player]} (used)` : t.players[player];
    el.veto[side].disabled = used || state !== "landed";
  });
}

function showReady() {
  el.result.hidden = true;
  el.status.hidden = false;
  el.status.textContent = "Ready when you are.";
  setState("idle");
  idleSpin();
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
    el.poolSize.textContent = `${games.length} games in ${collection}`;
    if (!games.length) return showError(`Add games to ${collection} in RomM.`);
    showReady();
  } catch (e) {
    showError(e.message);
  }
}

async function roll(message = "Spinning…") {
  setState("spinning");
  el.result.hidden = true;
  el.status.hidden = false;
  el.status.textContent = message;
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
}

function start() {
  if (!current) return;
  setState("launching");
  el.status.hidden = false;
  el.status.textContent = "Loading game…";
  window.location.assign(current.playUrl);
}

function primary() {
  if (state === "idle") roll();
  else if (state === "landed") start();
}

function reroll() {
  if (state === "landed") roll();
}

let vetoing = false;

async function veto(side) {
  const t = tournament();
  if (state !== "landed" || !t || !match || vetoing) return;
  const player = match.players[side];
  if (t.vetoes.includes(player)) return;
  vetoing = true;
  try {
    await mutate((latest) =>
      latest.vetoes.includes(player) ? latest : { ...latest, vetoes: [...latest.vetoes, player] },
    );
    roll(`${t.players[player]} vetoed it. Spinning again…`);
  } catch (e) {
    el.status.hidden = false;
    el.status.textContent = e.message;
  } finally {
    vetoing = false;
  }
}

el.roll.addEventListener("click", primary);
el.reroll.addEventListener("click", () => reroll());
el.veto.forEach((button, side) => button.addEventListener("click", () => veto(side)));

document.addEventListener("keydown", (e) => {
  if (e.repeat) return;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    primary();
  } else if (e.key === "r" || e.key === "R") {
    reroll();
  } else if (e.key === "1" || e.key === "2") {
    veto(Number(e.key) - 1);
  }
});

// --- Gamepads ---------------------------------------------------------------

// Standard-mapping button indexes. Every pad does everything: which pad is
// "player 1" isn't reliable (the Steam Deck's own controls count as one), so
// the veto buttons are per side instead.
const BUTTONS = [
  [0, primary], // A
  [9, primary], // Start
  [1, reroll], // B
  [2, () => veto(0)], // X
  [3, () => veto(1)], // Y
];
const held = new Map();

function pollGamepads() {
  for (const pad of navigator.getGamepads?.() ?? []) {
    if (!pad) continue;
    for (const [button, action] of BUTTONS) {
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
  if (e.persisted && state === "launching") showReady();
});

subscribe(onTournament);
watch();
requestAnimationFrame(pollGamepads);
loadPool();

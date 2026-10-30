// The tournament record on the server, shared by the TV and referee pages.

let current = { version: 0, tournament: null };
let loaded = false;
const listeners = new Set();

function publish(snapshot) {
  if (loaded && snapshot.version === current.version) return;
  loaded = true;
  current = snapshot;
  for (const listener of listeners) listener(current.tournament);
}

export function tournament() {
  return current.tournament;
}

// Calls listener on every change, starting with the first load.
export function subscribe(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// Long-polls the server for changes, forever.
export async function watch() {
  publish(await (await fetch("/api/tournament")).json());
  for (;;) {
    try {
      const response = await fetch(`/api/tournament?since=${current.version}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      publish(await response.json());
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 3000));
    }
  }
}

// Applies change (record -> new record, sync or async) and saves it. If
// someone else saved first, retries against their version, so neither write
// is lost.
export async function mutate(change) {
  for (let attempt = 0; attempt < 5; attempt++) {
    const next = await change(current.tournament);
    const response = await fetch("/api/tournament", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ version: current.version, tournament: next }),
    });
    const body = await response.json();
    if (response.status === 409) {
      publish({ version: body.version, tournament: body.tournament });
      continue;
    }
    if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
    publish(body);
    return body.tournament;
  }
  throw new Error("Couldn't save: the tournament keeps changing. Try again.");
}

// A BracketsManager on a fresh in-memory database, from the globals the
// brackets-viewer bundle defines.
export function createManager() {
  const Manager = window.bracketsManager.constructor;
  const Database = window.inMemoryDatabase.constructor;
  return new Manager(new Database());
}

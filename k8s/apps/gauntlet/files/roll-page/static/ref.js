// The Gauntlet referee page, for a phone. Starts the tournament, records
// winners, and fixes mistakes. Every change goes through mutate(), and the
// bracket is rebuilt from the record each time; see bracket.js.
import { newTournament, prune, rebuild, withResult } from "/static/bracket.js";
import { createManager, mutate, subscribe, tournament, watch } from "/static/store.js";

const $ = (id) => document.getElementById(id);
const el = {
  error: $("error"),
  setup: $("setup"),
  players: $("players"),
  shuffle: $("shuffle"),
  create: $("create"),
  running: $("running"),
  champion: $("champion"),
  onTv: $("on-tv"),
  tvLabel: $("tv-label"),
  win: [$("win1"), $("win2")],
  tvSelect: $("tv-select"),
  undo: $("undo"),
  bracket: $("bracket"),
  vetoList: $("veto-list"),
  names: $("names"),
  saveNames: $("save-names"),
  end: $("end"),
  dialog: $("match-dialog"),
  dialogLabel: $("dialog-label"),
  dialogHint: $("dialog-hint"),
  dialogWin: [$("dialog-win1"), $("dialog-win2")],
  dialogClear: $("dialog-clear"),
  dialogTv: $("dialog-tv"),
};

let bracket = null; // the latest rebuild
let rebuildSeq = 0;

function showError(message) {
  el.error.textContent = message;
  el.error.hidden = !message;
}

async function change(fn) {
  showError("");
  try {
    await mutate(fn);
  } catch (e) {
    showError(e.message);
  }
}

// --- Setup ------------------------------------------------------------------

function shuffle(list) {
  const a = [...list];
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
}

el.create.addEventListener("click", async () => {
  let players = el.players.value
    .split("\n")
    .map((p) => p.trim().slice(0, 40))
    .filter(Boolean);
  if (new Set(players.map((p) => p.toLowerCase())).size !== players.length) {
    return showError("Each player needs a different name.");
  }
  if (players.length < 2) return showError("Add at least two players.");
  if (el.shuffle.checked) players = shuffle(players);
  const format = document.querySelector('input[name="format"]:checked').value;
  // Build it once here, so a bracket the library rejects never gets saved.
  try {
    await rebuild(newTournament(players, format), createManager);
  } catch (e) {
    return showError(e.message);
  }
  change((t) => t ?? newTournament(players, format));
});

// --- Running ----------------------------------------------------------------

function sides(match) {
  return [match.opponent1?.id ?? null, match.opponent2?.id ?? null];
}

// Records a match result (winner null clears it).
function setWinner(match, winner) {
  const loser = sides(match).find((p) => p !== winner);
  return change((t) => prune(withResult(t, match.id, winner, loser), createManager));
}

async function render(t) {
  const seq = ++rebuildSeq;
  el.setup.hidden = Boolean(t);
  el.running.hidden = !t;
  if (!t) {
    bracket = null;
    return;
  }

  let built;
  try {
    built = await rebuild(t, createManager);
  } catch (e) {
    showError(`Bracket error: ${e.message}`);
    return;
  }
  if (seq !== rebuildSeq) return;
  bracket = built;
  const name = (id) => (id == null ? "TBD" : t.players[id]);

  // Champion
  el.champion.hidden = built.champion == null;
  if (built.champion != null) el.champion.textContent = `🏆 ${name(built.champion)} wins the Gauntlet`;

  // On the TV
  el.onTv.hidden = !built.tv;
  if (built.tv) {
    el.tvLabel.textContent = built.labels.get(built.tv.id);
    sides(built.tv).forEach((player, side) => {
      el.win[side].textContent = `${name(player)} won`;
      el.win[side].onclick = () => setWinner(built.tv, player);
    });
    el.tvSelect.replaceChildren(
      ...built.playable.map((m) => {
        const [a, b] = sides(m);
        return new Option(`${built.labels.get(m.id)}: ${name(a)} vs ${name(b)}`, m.id, false, m.id === built.tv.id);
      }),
    );
  }

  el.undo.disabled = t.results.length === 0;

  // Bracket
  window.bracketsViewer.render(
    {
      stages: built.data.stage,
      matches: built.data.match,
      matchGames: built.data.match_game,
      participants: built.data.participant,
    },
    {
      selector: "#bracket",
      clear: true,
      onMatchClick: (match) => openMatch(match.id),
    },
  );

  // Vetoes
  el.vetoList.replaceChildren(
    ...t.players.map((player, i) => {
      const used = t.vetoes.includes(i);
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className = `chip${used ? " used" : ""}`;
      chip.textContent = `${player}: ${used ? "used" : "available"}`;
      chip.onclick = () =>
        change((latest) => ({
          ...latest,
          vetoes: latest.vetoes.includes(i)
            ? latest.vetoes.filter((v) => v !== i)
            : [...latest.vetoes, i],
        }));
      return chip;
    }),
  );

  // Names (left alone while someone is typing in them)
  if (!el.names.contains(document.activeElement)) {
    el.names.replaceChildren(
      ...t.players.map((player) => {
        const input = document.createElement("input");
        input.value = player;
        input.maxLength = 40;
        return input;
      }),
    );
  }
}

function openMatch(matchId) {
  if (!bracket) return;
  const match = bracket.data.match.find((m) => m.id === matchId);
  if (!match) return;
  const t = tournament();
  const players = sides(match);
  const ready = players.every((p) => p != null);
  const decided = ["opponent1", "opponent2"].some((s) => match[s]?.result === "win");
  const playable = bracket.playable.some((m) => m.id === matchId);

  el.dialogLabel.textContent = bracket.labels.get(matchId);
  el.dialogHint.textContent = !ready
    ? "Waiting on an earlier match."
    : decided
      ? "Pick a different winner, or clear the result to replay it."
      : "Tap the winner.";
  players.forEach((player, side) => {
    const button = el.dialogWin[side];
    button.hidden = !ready;
    button.textContent = player == null ? "TBD" : `${t.players[player]} won`;
    button.classList.toggle("chosen", match[side ? "opponent2" : "opponent1"]?.result === "win");
    button.onclick = () => {
      el.dialog.close();
      setWinner(match, player);
    };
  });
  el.dialogClear.hidden = !decided;
  el.dialogClear.onclick = () => {
    el.dialog.close();
    setWinner(match, null);
  };
  el.dialogTv.hidden = !playable;
  el.dialogTv.onclick = () => {
    el.dialog.close();
    change((latest) => ({ ...latest, tv: matchId }));
  };
  el.dialog.showModal();
}

el.tvSelect.addEventListener("change", () => {
  const tv = Number(el.tvSelect.value);
  change((t) => ({ ...t, tv }));
});

el.undo.addEventListener("click", () =>
  change((t) => ({ ...t, results: t.results.slice(0, -1) })),
);

el.saveNames.addEventListener("click", () => {
  const names = [...el.names.querySelectorAll("input")].map((i) => i.value.trim().slice(0, 40));
  if (names.some((n) => !n)) return showError("Names can't be empty.");
  if (new Set(names.map((n) => n.toLowerCase())).size !== names.length) {
    return showError("Each player needs a different name.");
  }
  document.activeElement?.blur();
  change((t) => ({ ...t, players: names }));
});

// Two taps, so a stray one doesn't throw the night away.
let endArmed = null;
el.end.addEventListener("click", () => {
  if (!endArmed) {
    el.end.textContent = "Tap again to end the tournament";
    endArmed = setTimeout(() => {
      endArmed = null;
      el.end.textContent = "End tournament";
    }, 4000);
    return;
  }
  clearTimeout(endArmed);
  endArmed = null;
  el.end.textContent = "End tournament";
  change(() => null);
});

subscribe(render);
watch();

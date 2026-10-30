// Tournament logic shared by the TV and referee pages.
//
// The server stores only a small record: players in seed order, the format,
// and each match's winner and loser in the order they were entered. Every page
// rebuilds the bracket by replaying that record through brackets-manager.
// Editing any result, even one that later matches depend on, is then just
// editing the record. On replay, a result whose match no longer has those two
// players (because an earlier result changed) no longer applies.

// brackets-manager match statuses.
const STATUS_READY = 2;
const STATUS_RUNNING = 3;
const STATUS_COMPLETED = 4;
const STATUS_ARCHIVED = 5;

export const FORMATS = {
  single: "single_elimination",
  double: "double_elimination",
};

// A fresh tournament record.
export function newTournament(players, format) {
  return { format, players, results: [], vetoes: [], tv: null };
}

function seeding(players) {
  let size = 2;
  while (size < players.length) size *= 2;
  return [...players, ...Array(size - players.length).fill(null)];
}

function isPlayable(match) {
  return (
    [STATUS_READY, STATUS_RUNNING].includes(match.status) &&
    match.opponent1?.id != null &&
    match.opponent2?.id != null
  );
}

function isDone(match) {
  return [STATUS_COMPLETED, STATUS_ARCHIVED].includes(match.status);
}

// Rebuilds the bracket from a tournament record.
//
// createManager returns a new BracketsManager on an empty in-memory
// database; it's injected so tests can use the npm package and pages the
// bundled one.
//
// Returns the exported bracket data, the results that still apply, a label
// for every match, the playable matches in play order, the match on the TV,
// and the champion once the final is decided.
export async function rebuild(tournament, createManager) {
  const manager = createManager();
  const type = FORMATS[tournament.format];
  if (!type) throw new Error(`Unknown format "${tournament.format}"`);
  if (tournament.players.length < 2) throw new Error("Add at least two players.");

  await manager.create.stage({
    tournamentId: 0,
    name: "Gauntlet",
    type,
    seeding: seeding(tournament.players),
    settings: {
      balanceByes: true,
      ...(type === FORMATS.double ? { grandFinal: "simple" } : {}),
    },
  });

  const applied = [];
  for (const result of tournament.results) {
    const data = await manager.export();
    const match = data.match.find((m) => m.id === result.match);
    if (!match || !isPlayable(match)) continue;
    const pair = [match.opponent1.id, match.opponent2.id];
    if (!pair.includes(result.winner) || !pair.includes(result.loser)) continue;
    const side = match.opponent1.id === result.winner ? "opponent1" : "opponent2";
    await manager.update.match({ id: match.id, [side]: { result: "win" } });
    applied.push(result);
  }

  const data = await manager.export();
  const rounds = new Map(data.round.map((r) => [r.id, r]));
  const groups = new Map(data.group.map((g) => [g.id, g]));
  const groupCount = data.group.length;

  const labels = new Map();
  for (const match of data.match) {
    labels.set(match.id, matchLabel(match, rounds, groups, groupCount, type));
  }

  // Play order: earlier rounds first, then winners before losers. In double
  // elimination this interleaves the two brackets the usual way.
  const order = (m) => [rounds.get(m.round_id).number, groups.get(m.group_id).number, m.number];
  const playable = data.match.filter(isPlayable).sort((a, b) => {
    const [x, y] = [order(a), order(b)];
    return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
  });

  const tv = playable.find((m) => m.id === tournament.tv) ?? playable[0] ?? null;

  // The last group holds the final: the grand final in double elimination,
  // the final round otherwise.
  const lastGroup = data.group.reduce((a, b) => (b.number > a.number ? b : a));
  const finalMatch = data.match
    .filter((m) => m.group_id === lastGroup.id)
    .reduce((a, b) => (rounds.get(b.round_id).number > rounds.get(a.round_id).number ? b : a));
  let champion = null;
  if (isDone(finalMatch)) {
    const winner = ["opponent1", "opponent2"].find((s) => finalMatch[s]?.result === "win");
    if (winner) champion = finalMatch[winner].id;
  }

  return { data, applied, labels, playable, tv, champion };
}

function matchLabel(match, rounds, groups, groupCount, type) {
  const round = rounds.get(match.round_id).number;
  const group = groups.get(match.group_id).number;
  if (type === FORMATS.single) {
    const roundCount = Math.max(
      ...[...rounds.values()].filter((r) => r.group_id === match.group_id).map((r) => r.number),
    );
    if (round === roundCount) return "Final";
    if (round === roundCount - 1) return `Semifinal ${match.number}`;
    return `Round ${round} · Match ${match.number}`;
  }
  if (group === groupCount) return "Grand final";
  const bracket = group === 1 ? "Winners" : "Losers";
  return `${bracket} R${round} · Match ${match.number}`;
}

// The record with one match's result set, or cleared when winner is null.
// A changed result keeps its place in the order, so replaying still visits
// matches in the order they were played.
export function withResult(tournament, matchId, winner, loser) {
  if (winner == null) {
    return { ...tournament, results: tournament.results.filter((r) => r.match !== matchId) };
  }
  const result = { match: matchId, winner, loser };
  const exists = tournament.results.some((r) => r.match === matchId);
  return {
    ...tournament,
    results: exists
      ? tournament.results.map((r) => (r.match === matchId ? result : r))
      : [...tournament.results, result],
  };
}

// The record without results that no longer apply, so one can't come back
// later if a replay happens to pair the same players in that match again.
export async function prune(tournament, createManager) {
  const { applied } = await rebuild(tournament, createManager);
  return { ...tournament, results: applied };
}

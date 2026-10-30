# The Gauntlet

Elimination tournament night: two players go head to head on randomly chosen
retro games. Everything is on the internal gateway (LAN only).

| Address | What | Used by |
| --- | --- | --- |
| `gauntlet.msng.to` | TV page: matchup, wheel, vetoes, launches the game | TV (Steam Deck browser) |
| `gauntlet.msng.to/ref` | Referee page: start the tournament, record winners, fix mistakes | Referee's phone |
| `romm.msng.to` | RomM (`k8s/apps/romm`): library and EmulatorJS | TV, after Start |

## How it works

- The server picks a random game from RomM's **Gauntlet** collection. The
  wheel then spins to that pick; it only animates.
- **Start** opens RomM's console-mode player, which starts the game
  immediately. Use the browser's back button to return.
- The referee page keeps the tournament: players, format (single or double
  elimination), each match's winner, and vetoes. The TV follows it live and
  shows the current match's names.
- The server stores that record in `/data/tournament.json` on a Longhorn
  volume. The pages rebuild the bracket from it with
  [brackets-manager](https://github.com/Drarig29/brackets-manager.js), vendored
  in `static/brackets-viewer.min.js` with its viewer. Because the bracket is
  always rebuilt from the list of results, the referee can change or clear
  any result, even one that later matches depend on. Later results that no
  longer fit are dropped, and those matches get replayed.

## Rules on the TV

| Action | Keyboard | Gamepad |
| --- | --- | --- |
| Roll, then Start the landed game | Enter / Space | A or Start |
| Reroll (free; for broken or unsuitable games) | R | B |
| Veto for the left / right player (one per player per tournament) | 1 / 2 | X / Y |

Every gamepad can do everything: which pad counts as "player 1" isn't reliable
(the Steam Deck's own controls are a gamepad too), so vetoes are per side.
Rolls skip games already rolled for the current match until the pool runs out.

Without a tournament, the TV takes typed-in names and has no vetoes.

## One-time setup

1. **RomM.** Finish the setup wizard, upload ROMs, and create a collection
   named `Gauntlet`.
2. **API token.** In RomM, Administration → Client API Tokens, create a token
   with `roms.read` and `collections.read`, then:

   ```sh
   kubectl -n gauntlet create secret generic romm-api-token \
     --from-literal=ROMM_API_TOKEN=rmm_...
   ```

   The TV says what's missing until the token and collection exist.

## Development

`files/roll-page/server.py` is standard library only. Run it locally with
`ROMM_URL`, `ROMM_PUBLIC_URL`, `ROMM_API_TOKEN` and (to keep the tournament)
`DATA_DIR` set, then `python server.py`. Tests: `uv run pytest` in
`files/roll-page/`.

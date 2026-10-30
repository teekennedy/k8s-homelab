# The Gauntlet

Bracket-style tournament night: two players go head to head on randomly
chosen retro games. Everything is on the internal gateway (LAN only).

| Host | What | Used by |
| --- | --- | --- |
| `gauntlet.msng.to` | Roll page: matchup, wheel, launches the game | TV (Steam Deck browser) |
| `bracket.msng.to` | Bracket: players, bracket, results | Referee's phone |
| `romm.msng.to` | RomM (`k8s/apps/romm`): library and EmulatorJS | TV, after a roll |

The roll page server picks a random game from RomM's **Gauntlet** collection.
The wheel then spins to that pick and only animates. The page stores no
results; Bracket is the only record.

## One-time setup

1. **RomM admin.** Open `romm.msng.to` and finish the setup wizard.
2. **ROMs.** Upload through RomM's UI, or copy into the PVC under
   `/romm/library/roms/<platform>/`, then run a scan.
3. **Collection.** Create a collection named `Gauntlet` and add the games.
4. **API token.** In RomM, Administration → Client API Tokens, create a token
   with `roms.read` and `collections.read`, then:

   ```sh
   kubectl -n gauntlet create secret generic romm-api-token \
     --from-literal=ROMM_API_TOKEN=rmm_...
   ```

   The roll page shows what's missing until the token and collection exist.
5. **Bracket login.** User `admin@msng.to`; the password is in the
   `bracket-secrets` Secret:

   ```sh
   kubectl -n gauntlet get secret bracket-secrets \
     -o jsonpath='{.data.ADMIN_PASSWORD}' | base64 -d
   ```

## Roll page controls

| Action | Keyboard | Gamepad |
| --- | --- | --- |
| Roll, or play the landed game now | Enter / Space | A or Start |
| Reroll | R | B |
| Stop the auto-launch countdown | Esc | — |

Rolls skip games already rolled for the current matchup until the pool runs
out. Changing a player name resets that.

## Development

`files/roll-page/server.py` is standard library only. Run it locally with
`ROMM_URL`, `ROMM_PUBLIC_URL` and `ROMM_API_TOKEN` set, then
`python server.py`. Tests: `uv run pytest` in `files/roll-page/`.

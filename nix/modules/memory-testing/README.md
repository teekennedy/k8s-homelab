# memory-testing

Support for investigating a host's memory subsystem (borg-0's memory-controller
errors). Test runs, analysis and history live in the separate `ms02-memory`
repo. This module only produces and stores the data.

## `services.memory-testing.enable`

`memory-testing-collect` runs at boot, 5 minutes after boot, then every 30
minutes. It writes `/var/lib/memory-testing/boots/<boot time UTC>/`, persisted
in `/cache`:

| File | Collected | Contents |
|---|---|---|
| `meta.txt` | once | boot time and id, kernel, BIOS, CPU, microcode, BDAT address |
| `bdat-acpi.bin`, `bdat-data.bin` | once | Intel BDAT table and the margin results it points to, if the BIOS publishes them |
| `dmidecode-memory.txt` | once | installed modules, including serial numbers |
| `bios-settings*.txt` | once | BIOS settings recorded by `bios-setup` before this boot, else the last recorded ones |
| `temps.txt` | appended | DIMM (spd5118) and CPU package temperatures |
| `mce-this-boot.txt` | refreshed | kernel machine-check lines. The next boot finishes the previous boot's file from the journal, so a crash doesn't lose them |
| `rasdaemon-mce.txt`, `reboots.txt` | refreshed | full MCE history and reboot history |

## `services.memory-testing.biosSetup.enable`

Changes BIOS setup variables without a keyboard or a USB stick. The bootloader
installs an EDK2 shell, `setup_var.efi` and a `startup.nsh` into
`/boot/efi/memory-testing/`, plus a "BIOS setup (memory testing)" boot entry.

```sh
sudo bios-setup rmt-per-bit                # preset
sudo bios-setup 'SaSetup:0x19A(2)=0xFA0'   # raw write: max frequency 4000
sudo bios-setup --dry-run rmt-off          # show what would be written
sudo bios-setup read                       # change nothing, record settings
```

`bios-setup` writes the queued `setup_var.efi` call to `/boot/memory-testing/action.nsh`
and reboots once into the shell entry (`systemctl reboot --boot-loader-entry=…`).
`startup.nsh` records the settings, applies the queued writes, records them
again and resets. The next boot then trains memory with the new settings, and
`memory-testing-collect` moves the recordings into that boot's directory.

The offsets are from the MS-02 Ultra BIOS 1.04 (board ARWSA) IFR. `bios-setup`
refuses to run on any other board or BIOS version unless given `--force`.
Selecting the boot entry by hand runs the same script: press ESC during the
5-second countdown to get an interactive shell instead.

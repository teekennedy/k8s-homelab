# Memory-subsystem testing support. See README.md in this directory.
{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.memory-testing;
  stateDir = "/var/lib/memory-testing";

  # The bootloader installs these into the ESP on every switch.
  espDir = "efi/memory-testing";
  # Runtime files written by bios-setup and the UEFI shell. Kept outside espDir
  # so the bootloader's extra-files cleanup never touches them.
  espStateDir = "memory-testing";
  bootEntry = "memory-testing-bios-setup.conf";

  setupVar = pkgs.fetchurl {
    url = "https://github.com/datasone/setup_var.efi/releases/download/0.3.1/setup_var.efi";
    hash = "sha256-y+V3e2EnbT81BqKLNIRTJukvbBcb/1kXfjkS/iD0mEA=";
  };

  # Setup variable offsets from the IFR of Minisforum MS-02 Ultra BIOS 1.04
  # (board ARWSA). Other boards or BIOS versions have different offsets.
  biosBoard = "ARWSA";
  biosVersion = "1.04";
  readVariables = [
    "SaSetup:0x1" # BDAT ACPI table support
    "SaSetup:0x1B4" # BDAT memory test type: 0 RMT per rank, 1 RMT per bit, 2 margin 2D
    "SaSetup:0x221" # Rank Margin Tool
    "SaSetup:0x233" # RMT per bit
    "SaSetup:0x1B5" # RMT loop count, 0 = auto
    "SaSetup:0x1A1" # MRC fast boot
    "SaSetup:0x1B6" # memory test on warm boot
    "SaSetup:0x224" # retrain margin check
    "SaSetup:0x19C" # memory profile: 0 default SPD, 1 custom
    "SaSetup:0x19A(2)" # max memory frequency, MT/s, 0 = auto
    "SaSetup:0x27(2)" # VDD mV, 0 = auto
    "SaSetup:0x29(2)" # VDDQ mV, 0 = auto
    "SaSetup:0x31(2)" # Rcomp WrDS
    "SaSetup:0x33(2)" # Rcomp WrDSCmd
    "SaSetup:0x35(2)" # Rcomp WrDSCtl
    "SaSetup:0x37(2)" # Rcomp WrDSClk
    "SaSetup:0x39(2)" # DIMM0 RttWr
    "SaSetup:0x3B(2)" # DIMM0 RttNomRd
    "SaSetup:0x3D(2)" # DIMM0 RttNomWr
    "SaSetup:0x3F(2)" # DIMM0 RttPark
    "CpuSetup:0x461(2)" # SA voltage override, 0 = auto
    "CpuSetup:0x487(2)" # VccSA mV
  ];
  presets = {
    read = [];
    rmt-on = ["SaSetup:0x1=0x1" "SaSetup:0x221=0x1" "SaSetup:0x1B4=0x0" "SaSetup:0x1A1=0x0"];
    rmt-off = ["SaSetup:0x1=0x0" "SaSetup:0x221=0x0" "SaSetup:0x1B4=0x0" "SaSetup:0x1A1=0x1"];
    rmt-per-bit = ["SaSetup:0x1B4=0x1" "SaSetup:0x233=0x1"];
    rmt-per-rank = ["SaSetup:0x1B4=0x0" "SaSetup:0x233=0x0"];
  };

  # The EDK2 shell runs startup.nsh from its own directory and sets
  # %homefilesystem% to the filesystem it was loaded from (the ESP).
  setupVarEfi = "\\${lib.replaceStrings ["/"] ["\\"] espDir}\\setup_var.efi";
  readArgs = lib.concatStringsSep " " readVariables;
  startupNsh = pkgs.writeText "startup.nsh" (lib.concatMapStrings (l: l + "\r\n") [
    "@echo -off"
    "# Run by the ${bootEntry} boot entry: apply queued settings, record them, reset."
    "# Press ESC during the countdown for an interactive shell instead."
    "%homefilesystem%"
    "cd \\${espStateDir}"
    "${setupVarEfi} ${readArgs} >a before.txt"
    "if exist action.nsh then"
    "  action.nsh"
    "  rm action.nsh"
    "endif"
    "${setupVarEfi} ${readArgs} >a after.txt"
    "reset"
  ]);

  collect = pkgs.writeShellApplication {
    name = "memory-testing-collect";
    runtimeInputs =
      [pkgs.coreutils pkgs.gawk pkgs.gnugrep pkgs.dmidecode pkgs.util-linux config.systemd.package]
      ++ lib.optional config.hardware.rasdaemon.enable pkgs.rasdaemon;
    text = ''
      state=${stateDir}
      btime=$(awk '/^btime/ {print $2}' /proc/stat)
      boot_id=$(tr -d - < /proc/sys/kernel/random/boot_id)
      dir=$state/boots/$(date -u -d "@$btime" +%Y-%m-%dT%H%MZ)
      if [ -e "$dir/meta.txt" ] && ! grep -qx "boot_id=$boot_id" "$dir/meta.txt"; then
        dir=$dir-''${boot_id:0:8}
      fi
      mkdir -p "$dir"
      cd "$dir"
      now=$(date -u +%FT%TZ)

      mce_log() {
        journalctl -k -b "$1" --no-pager -q -o short-iso \
          | grep -iE 'machine check|hardware error|mce: ' || true
      }

      # Everything fixed for the life of a boot is collected once.
      if [ ! -e meta.txt ]; then
        {
          echo "host=$(hostname)"
          echo "boot_utc=$(date -u -d "@$btime" +%FT%TZ)"
          echo "boot_id=$boot_id"
          echo "kernel=$(uname -r)"
          echo "bios_version=$(cat /sys/class/dmi/id/bios_version)"
          echo "bios_date=$(cat /sys/class/dmi/id/bios_date)"
          echo "board=$(cat /sys/class/dmi/id/board_name)"
          echo "product=$(cat /sys/class/dmi/id/product_name)"
          echo "cpu=$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
          echo "microcode=$(grep -m1 microcode /proc/cpuinfo | cut -d: -f2- | tr -d ' ')"
          echo "memtotal_kb=$(awk '/MemTotal/ {print $2}' /proc/meminfo)"
        } > meta.tmp

        # The BDAT ACPI table holds the physical address of the results.
        if [ -r /sys/firmware/acpi/tables/BDAT ]; then
          cat /sys/firmware/acpi/tables/BDAT > bdat-acpi.bin
          addr=$(od -An -tu8 -j40 -N8 bdat-acpi.bin | tr -d ' ')
          echo "bdat_addr=$(printf '%#x' "$addr")" >> meta.tmp
          dd if=/dev/mem of=bdat-data.bin bs=4096 count=16 skip="$addr" \
            iflag=skip_bytes status=none
        else
          echo "bdat_addr=none" >> meta.tmp
        fi

        dmidecode -t 17 > dmidecode-memory.txt

        # Settings recorded by the UEFI shell just before this boot, if bios-setup ran.
        esp=/boot/${espStateDir}
        if [ -e "$esp/after.txt" ]; then
          tr -d '\r' < "$esp/after.txt" > bios-settings.txt
          [ -e "$esp/before.txt" ] && tr -d '\r' < "$esp/before.txt" > bios-settings-before.txt
          [ -e "$esp/action.txt" ] && tr -d '\r' < "$esp/action.txt" > bios-setup-output.txt
          [ -e "$esp/queued.txt" ] && cp "$esp/queued.txt" bios-setup-queued.txt
          rm -f "$esp/after.txt" "$esp/before.txt" "$esp/action.txt" "$esp/queued.txt"
          cp bios-settings.txt "$state/bios-settings-current.txt"
          echo "bios_settings=recorded by the UEFI shell before this boot" >> meta.tmp
        elif [ -e "$state/bios-settings-current.txt" ]; then
          cp "$state/bios-settings-current.txt" bios-settings.txt
          echo "bios_settings=last recorded, no bios-setup before this boot" >> meta.tmp
        else
          echo "bios_settings=unknown" >> meta.tmp
        fi

        # The previous boot can't record its own end, so finish its MCE log here.
        prev=$(journalctl --list-boots -q --no-pager | awk '$1 == -1 {print $2}')
        if [ -n "$prev" ]; then
          prev_meta=$(grep -lx "boot_id=$prev" "$state"/boots/*/meta.txt 2>/dev/null | head -1 || true)
          if [ -n "$prev_meta" ]; then
            mce_log "$prev" > "$(dirname "$prev_meta")/mce-this-boot.txt"
          fi
        fi
        mv meta.tmp meta.txt
      fi

      # Refreshed on every run.
      echo "$now" >> collections.txt
      for d in /sys/bus/i2c/devices/*; do
        [ "$(cat "$d/name" 2>/dev/null)" = spd5118 ] || continue
        for h in "$d"/hwmon/hwmon*; do
          echo "$now spd5118 $(basename "$d") $(cat "$h/temp1_input")"
        done
      done >> temps.txt
      for h in /sys/class/hwmon/hwmon*; do
        if [ "$(cat "$h/name")" = coretemp ]; then
          echo "$now coretemp package $(cat "$h/temp1_input")" >> temps.txt
        fi
      done
      mce_log 0 > mce-this-boot.txt
      if command -v ras-mc-ctl > /dev/null; then
        ras-mc-ctl db --errors --table mce_record > rasdaemon-mce.txt 2>&1 || true
      fi
      last -x reboot shutdown 2> /dev/null | head -40 > reboots.txt || true
    '';
  };

  biosSetup = pkgs.writeShellApplication {
    name = "bios-setup";
    runtimeInputs = [pkgs.coreutils config.systemd.package];
    text = ''
      usage() {
        cat <<EOF
      Usage: bios-setup [--dry-run] [--no-reboot] [--force] PRESET|VAR:OFFSET=VALUE...

      Queues BIOS setup variable writes on the ESP, then reboots once into the
      UEFI shell entry, which applies them, records the settings and resets.
      The next boot's memory-testing-collect saves the recorded settings.

      Presets:
      ${lib.concatStrings (lib.mapAttrsToList (n: v: "  ${n}: ${
          if v == []
          then "(no change, only record settings)"
          else lib.concatStringsSep " " v
        }\n")
        presets)}
      Raw writes look like SaSetup:0x19A(2)=0xFA0. Offsets are for board
      ${biosBoard} BIOS ${biosVersion}; --force skips that check.
      EOF
      }

      dry_run=0 reboot=1 force=0
      writes=()
      [ $# -gt 0 ] || { usage; exit 2; }
      for arg in "$@"; do
        case $arg in
          -h | --help) usage; exit 0 ;;
          --dry-run) dry_run=1 ;;
          --no-reboot) reboot=0 ;;
          --force) force=1 ;;
      ${lib.concatStrings (lib.mapAttrsToList (n: v: "    ${n}) writes+=(${lib.escapeShellArgs v}) ;;\n") presets)}
          *)
            if [[ $arg =~ ^[A-Za-z]+:0x[0-9A-Fa-f]+(\([0-9]+\))?=0x[0-9A-Fa-f]+$ ]]; then
              writes+=("$arg")
            else
              echo "unknown preset or malformed write: $arg" >&2
              exit 2
            fi
            ;;
        esac
      done

      board=$(cat /sys/class/dmi/id/board_name)
      version=$(cat /sys/class/dmi/id/bios_version)
      if [ "$force" -eq 0 ] && { [ "$board" != ${biosBoard} ] || [ "$version" != ${biosVersion} ]; }; then
        echo "board $board BIOS $version: offsets are for ${biosBoard} ${biosVersion} (use --force)" >&2
        exit 1
      fi

      if [ ''${#writes[@]} -gt 0 ]; then
        echo "will write: ''${writes[*]}"
      else
        echo "no writes queued: the shell will only record the current settings"
      fi
      [ "$dry_run" -eq 1 ] && exit 0
      [ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

      esp=/boot/${espStateDir}
      mkdir -p "$esp"
      rm -f "$esp/action.nsh"
      if [ ''${#writes[@]} -gt 0 ]; then
        printf '${lib.replaceStrings ["\\"] ["\\\\"] setupVarEfi} --write_on_demand %s >a action.txt\r\n' \
          "''${writes[*]}" > "$esp/action.nsh"
      fi
      echo "''${writes[*]:-(none)}" > "$esp/queued.txt"
      sync

      if [ "$reboot" -eq 1 ]; then
        systemctl reboot --boot-loader-entry=${bootEntry}
      else
        echo "queued. Reboot with: systemctl reboot --boot-loader-entry=${bootEntry}"
      fi
    '';
  };
in {
  options.services.memory-testing = {
    enable = lib.mkEnableOption "per-boot collection of memory margin results, modules, temperatures and machine checks";
    biosSetup.enable = lib.mkEnableOption "the bios-setup command and its UEFI shell boot entry (MS-02 Ultra BIOS 1.04 only)";
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      environment.systemPackages = [collect];
      environment.persistence."/cache".directories = [stateDir];

      systemd.services.memory-testing-collect = {
        description = "Collect memory margin results and memory evidence for this boot";
        wantedBy = ["multi-user.target"];
        after = ["local-fs.target"];
        unitConfig.RequiresMountsFor = [stateDir "/boot"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe collect;
        };
      };
      # Refreshes temperatures and machine checks while the boot lasts.
      systemd.timers.memory-testing-collect = {
        wantedBy = ["timers.target"];
        timerConfig = {
          OnBootSec = "5min";
          OnUnitActiveSec = "30min";
        };
      };
    })
    (lib.mkIf cfg.biosSetup.enable {
      environment.systemPackages = [biosSetup];
      boot.loader.systemd-boot = {
        extraFiles = {
          "${espDir}/shell.efi" = "${pkgs.edk2-uefi-shell}/shell.efi";
          "${espDir}/startup.nsh" = startupNsh;
          "${espDir}/setup_var.efi" = setupVar;
        };
        extraEntries.${bootEntry} = ''
          title  BIOS setup (memory testing)
          efi    /${espDir}/shell.efi
          sort-key z_memory-testing
        '';
      };
    })
  ];
}

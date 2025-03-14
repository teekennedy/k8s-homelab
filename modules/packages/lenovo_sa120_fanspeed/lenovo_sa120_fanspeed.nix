{
  fetchFromGitHub,
  writers,
}: let
  src = fetchFromGitHub {
    owner = "AndrewX192";
    repo = "lenovo-sa120-fanspeed-utility";
    rev = "f0fcd405d400dcee14c02cd1a2cd59a099e98c75";
    hash = "sha256-XDgwLzr6dV0ylkdp2Zw5VubIoqfaUEcOcRjnjFK5AbY=";
  };
  script = builtins.readFile (src + "/fancontrol.py");
in
  writers.writePython3Bin "lenovo-sa120-fanspeed" {
    # don't run flake8 on source
    doCheck = false;
  }
  script

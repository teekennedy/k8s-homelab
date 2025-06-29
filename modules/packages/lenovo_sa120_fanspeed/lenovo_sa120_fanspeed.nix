{
  fetchFromGitHub,
  python3,
  makeWrapper,
  sg3_utils,
  pkgs,
}: let
  src = fetchFromGitHub {
    owner = "AndrewX192";
    repo = "lenovo-sa120-fanspeed-utility";
    rev = "f0fcd405d400dcee14c02cd1a2cd59a099e98c75";
    hash = "sha256-XDgwLzr6dV0ylkdp2Zw5VubIoqfaUEcOcRjnjFK5AbY=";
  };
in
  pkgs.stdenv.mkDerivation {
    pname = "lenovo-sa120-fanspeed";
    version = "unstable";

    src = src;
    dontUnpack = true;

    buildInputs = [makeWrapper];
    installPhase = ''
      mkdir -p $out/bin
      install -m755 ${src}/fancontrol.py $out/bin/lenovo-sa120-fanspeed
      wrapProgram $out/bin/lenovo-sa120-fanspeed \
        --prefix PATH : ${sg3_utils}/bin \
        --prefix PATH : ${python3}/bin
    '';
  }

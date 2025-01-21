{...}: {
  networking = {
    interfaces = let
      common = {
        wakeOnLan.enable = false;
      };
    in {
      # aka eno1
      enp0s29f1 =
        common
        // {
          ipv4.addresses = [
            {
              address = "10.69.80.10";
              prefixLength = 25;
            }
          ];
        };
      # aka eno2
      enp0s29f2 =
        common
        // {
          useDHCP = true;
        };
      enp2s0 = common;
      enp3s0 = common;
    };
    defaultGateway = {
      address = "10.69.80.1";
      interface = "enp0s29f1";
    };
  };
}

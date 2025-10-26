{...}: {
  # Don't suspend / powerdown when the laptop lid is closed.
  services.logind.settings.Login.HandleLidSwitch = "ignore";
}

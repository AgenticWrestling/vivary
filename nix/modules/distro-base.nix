{ lib, pkgs, modulesPath, ... }:

let
  maxAgents    = 20;
  uidBlockSize = 65536;
  subidCount   = maxAgents * uidBlockSize;
in {
  imports = [
    (modulesPath + "/virtualisation/lxc-container.nix")
  ];

  boot.isContainer = true;

  networking.hostName  = "vivary";
  networking.useDHCP   = lib.mkDefault true;

  time.timeZone        = "UTC";
  i18n.defaultLocale   = "C.UTF-8";

  # No interactive console in production; saves significant closure size.
  console.enable = false;

  # Strip every default package (nano, perl, …) and documentation.
  environment.defaultPackages  = lib.mkForce [ ];
  documentation.enable         = false;
  documentation.info.enable    = false;
  documentation.man.enable     = false;
  programs.command-not-found.enable = false;

  environment.systemPackages = with pkgs; [
    # bash is already in the NixOS base; bringbashInteractive would add
    # readline/completion — unnecessary on a headless server.
    btrfs-progs   # btrfs subvolume operations for agent workspaces
    cacert        # TLS CA bundle for keeperd → Claude API calls
    nftables      # per-agent network isolation rules
  ];
  # sqlite: keeperd uses modernc.org/sqlite (pure Go) — no system binary needed.

  services.openssh.enable  = false;
  services.udisks2.enable  = false;
  security.sudo.enable     = false;

  users.mutableUsers = false;
  users.users.root.initialHashedPassword = "!";  # root login disabled

  # Nix daemon is not needed in the runtime image; disable it to reduce
  # the attack surface.  keeperd manages its own software layout.
  nix.enable = false;

  # subuid/subgid allocation for nspawn user namespacing.
  # Each agent container gets a non-overlapping 65 536-entry UID range.
  environment.etc."subuid".text = "root:100000:${toString subidCount}\n";
  environment.etc."subgid".text = "root:100000:${toString subidCount}\n";

  systemd.extraConfig = ''
    DefaultTimeoutStopSec=15s
  '';

  system.stateVersion = "25.11";
}

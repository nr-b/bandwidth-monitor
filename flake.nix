{
  description = "Bandwidth Monitor — real-time network monitoring dashboard";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # GeoIP databases (fetched as fixed-output derivations)
        geolite2-country = pkgs.fetchurl {
          url = "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb";
          hash = "sha256-8rj7BTcc+MkfjDOFGEyRhIMr3J/zjblOVFFI1GvBDAM=";
        };

        geolite2-asn = pkgs.fetchurl {
          url = "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb";
          hash = "sha256-12uzwfuqh60oqs9eC5D4qTRiaMJbIEmN6kIYxYPPlk8=";
        };

        bandwidth-monitor = pkgs.buildGoModule {
          pname = "bandwidth-monitor";
          version = "0.0.17";
          src = ./.;

          vendorHash = null; # Uses go.sum for verification

          buildInputs = [ pkgs.libpcap ];
          nativeBuildInputs = [ pkgs.pkg-config ];

          # CGO is needed for libpcap (gopacket)
          CGO_ENABLED = 1;

          ldflags = [ "-s" "-w" ];

          postInstall = ''
            # Install GeoIP databases alongside the binary
            install -Dm644 ${geolite2-country} $out/share/bandwidth-monitor/GeoLite2-Country.mmdb
            install -Dm644 ${geolite2-asn} $out/share/bandwidth-monitor/GeoLite2-ASN.mmdb

            # Install env.example
            install -Dm644 env.example $out/share/bandwidth-monitor/env.example

            # Install systemd service
            install -Dm644 bandwidth-monitor.service $out/lib/systemd/system/bandwidth-monitor.service
          '';

          meta = with pkgs.lib; {
            description = "Real-time network monitoring dashboard for Linux";
            homepage = "https://github.com/awlx/bandwidth-monitor";
            license = licenses.agpl3Only;
            platforms = platforms.linux;
            mainProgram = "bandwidth-monitor";
          };
        };
      in
      {
        packages = {
          default = bandwidth-monitor;
          bandwidth-monitor = bandwidth-monitor;
        };

        apps.default = flake-utils.lib.mkApp {
          drv = bandwidth-monitor;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            libpcap
            pkg-config
          ];
        };
      }
    ) // {
      # NixOS module for easy integration
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.bandwidth-monitor;
        in
        {
          options.services.bandwidth-monitor = {
            enable = lib.mkEnableOption "Bandwidth Monitor";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              description = "The bandwidth-monitor package to use.";
            };

            listenAddress = lib.mkOption {
              type = lib.types.str;
              default = ":8080";
              description = "HTTP listen address.";
            };

            environmentFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              description = "Path to environment file with configuration.";
            };

            settings = lib.mkOption {
              type = lib.types.attrsOf lib.types.str;
              default = {};
              description = "Environment variables for bandwidth-monitor.";
              example = {
                ADGUARD_URL = "http://adguard.local";
                ADGUARD_USER = "admin";
              };
            };
          };

          config = lib.mkIf cfg.enable {
            systemd.services.bandwidth-monitor = {
              description = "Bandwidth Monitor";
              after = [ "network.target" ];
              wantedBy = [ "multi-user.target" ];

              environment = {
                LISTEN = cfg.listenAddress;
                GEO_COUNTRY = "${cfg.package}/share/bandwidth-monitor/GeoLite2-Country.mmdb";
                GEO_ASN = "${cfg.package}/share/bandwidth-monitor/GeoLite2-ASN.mmdb";
              } // cfg.settings;

              serviceConfig = {
                ExecStart = "${cfg.package}/bin/bandwidth-monitor";
                DynamicUser = true;
                AmbientCapabilities = [ "CAP_NET_RAW" "CAP_NET_ADMIN" ];
                CapabilityBoundingSet = [ "CAP_NET_RAW" "CAP_NET_ADMIN" ];
                ProtectSystem = "strict";
                ProtectHome = true;
                PrivateTmp = true;
                NoNewPrivileges = true;
                Restart = "on-failure";
                RestartSec = 5;
              } // lib.optionalAttrs (cfg.environmentFile != null) {
                EnvironmentFile = cfg.environmentFile;
              };
            };
          };
        };
    };
}

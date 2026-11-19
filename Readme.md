# PrusaConnectMoonrakerAdapter

PrusaConnectMoonrakerAdapter connects Klipper printers through Moonraker to Prusa Connect Local. It adds verified file uploads, prepared G-code executions, durable start tracking and a Moonraker setup form alongside the existing Prusa printer guide.

**Built on Prusa Connect Local by Prusa Research a.s.** Community modifications are maintained by [D3ADD3V3R](https://github.com/D3ADD3V3R). This is an independent derivative, not an official Prusa release.

- [Upstream Prusa Connect](https://github.com/prusa3d/prusa-connect)
- [Adapter configuration and limitations](adapter/README.md)
- [Raspberry Pi build and installation](deploy/rpi/README.md)
- [Klipper macro contract](klipper/adapter-contract.md)
- [License](LICENCE.md) and [attribution](NOTICE.md)

## Current capabilities

- Moonraker identity, live status, file browsing and file management
- Optional G-code uploads with size and SHA-256 verification; uploads never start printing
- G-code preparation with execution identity, material/colour metadata and an early PCMA hook
- Optional print starts with a persistent SQLite journal, profile checks and local bed confirmation
- Reconciliation after lost start acknowledgements and connection loss
- Material-change status and generation-bound bed confirmations
- A Prusa / Klipper-Moonraker choice in Add a Printer, with address entry and connection status
- A configuration profile generator and coordinate readings without motion controls
- Manual homing, relative jogging, mesh leveling and filament actions, with live state checks
- A separate Raspberry Pi build directory and an installer with backup, health checks and rollback

The real Klipper macros and LCD workflow are still required before enabling PCMA print starts. The simulator implements the macro contract for testing. The production queue, dedicated PCMA status UI and supervised hardware acceptance remain separate integration work.

## Build on a Raspberry Pi

The supplied toolchain targets Linux ARM64. Git, make, Python 3, curl, tar, xz and flock must already be installed. The first build downloads pinned Go, Node and pnpm toolchains into the build directory.

```sh
git clone https://github.com/D3ADD3V3R/PrusaConnectMoonrakerAdapter.git
cd PrusaConnectMoonrakerAdapter
python3 deploy/rpi/stage.py "$HOME/pcma-build"
cd "$HOME/pcma-build"
make
```

The build downloads Prusa Connect at commit `b194cb83b682822e95e839a3cecf4391f3929e87` (`v0.1.0-beta.1`), applies `upstream.patch` and embeds the web frontend. The adapter SDK dependency is resolved from that checkout.

| Command | Purpose |
| --- | --- |
| `make` | Build PC, the preparation tool, profile generator and simulator |
| `make test` | Run adapter tests and Go vet |
| `make smoke` | Test an isolated PC instance and simulator, including the embedded web assets |
| `make check-install` | Exercise installation and rollback with an isolated service model |
| `make install` | Run checks, back up the installed binary, replace it and restart the service |
| `make status` | Read the installed service state and version |

The default install target is `/home/administrator/prusa-connect/prusa-connect`, served by `prusa-connect.service`. Override `INSTALL_DIR`, `SERVICE` and `HEALTH_URL` when the existing installation differs. Sources and build outputs stay in the separate build directory. Installation does not copy production configuration or credentials into this repository.

## Connect a printer

The public build uses neutral PCMA identifiers. Its connector instance is `pcma`, its type is `pcma-moonraker`, and its printer macro contract uses `PCMA_*`. Existing installations must align their configuration, prepared files and macros with these identifiers before switching to this build. The installer does not rewrite an existing installation's configuration.

Enable a PCMA connector instance named `pcma` in the PC installation's `config/config.yaml`. If the file does not exist, create it. Preserve any existing settings and other connector declarations.

```yaml
core:
  connectors:
    xbuddy:
      type: xbuddy
    pcma:
      type: pcma-moonraker
      config:
        uploads: true
        starts: false
```

Restart PC once after changing its connector declarations. Open **Add a Printer**, select **Klipper / Moonraker**, and enter a unique printer name and the Moonraker address, such as `printer.local:7125`. The page uses the signed-in browser session; no copied API token is needed. Saving the address and establishing a live printer connection are reported separately.

Moonraker API-key authentication is not implemented in this adapter version. Print-start activation also needs an approved compatibility profile, a durable journal and the required Klipper macros; see the adapter documentation.

## Generate a capability profile

After building, read the printer's active configuration with the supplied tool:

```sh
./bin/pcma-profile -address http://printer.local:7125 -model Klipper -uploads -output /home/administrator/klipper-capabilities.json
```

The tool reads configuration, object availability and status. It sends no heater, motion or print commands. The new output file contains the profile registry; standard output contains selected configuration evidence and its SHA-256. Keep printer-specific files outside this repository. The model label must match the endpoint's model option.

Set `core.connectors.pcma.config.capability_profile_path` to the absolute output path, then restart PC. The patched host validates the registry with its existing profile loader. A matching profile removes `no_capability_profile`; live probing and connector policy still restrict the accepted commands. `uploads: false` and `starts: false` retain their effect.

Generation currently supports one extruder. Heater limits come from explicit configuration values. The firmware range begins at the observed version and ends before the next patch release; regenerate after firmware or configuration changes. This profile describes implemented adapter commands and does not approve a machine for unattended printing. Manual motion requires known axis limits and homed axes; the adapter reads the current printer state before each action.

## Source layout

The independent [PCMA UI module](pcma-ui/README.md) adds a PCMA tab to the printer page. It reads materials and spools through the existing Materials API, requests a physical change and records the selected assignment only after a matching local printer confirmation. The Materials extension is unchanged, including its existing Change button. The PCMA bundle is installed separately from the core binary.

| Path | Purpose |
| --- | --- |
| `adapter/` | Moonraker connector, G-code preparation, simulator and Go tests |
| `pcma-ui/` | Independent filament workflow using public PC and Materials APIs |
| `deploy/rpi/` | Pinned versions, frontend patch, build and installation scripts |
| `tests/` | Isolated PC integration test |
| `klipper/` | Interface contract for the printer macros |
| `dev.ps1` | Windows adapter test/build entry point |

The full upstream checkout, downloaded toolchains, dependencies, binaries, production configuration, databases and print files are excluded from Git.

## License and third-party assets

This derivative retains **OCL v1.1 + Micro v1 + NoSaaS v1 + SWAtt v1**, exactly as specified by the pinned Prusa source. Read [LICENCE.md](LICENCE.md) for the complete terms, including commercial-use and service restrictions. Original notices remain in [adapter/LICENSE](adapter/LICENSE).

Upstream printer illustrations have separate redistribution restrictions and are not supplied here. The repository publishes its own sources and textual changes to the pinned dependency, rather than distributing upstream web assets or compiled PC binaries. Other dependencies retain their own licenses.

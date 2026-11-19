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
- Generic single-extruder Klipper filament macros, with persistent requests and local confirmation
- Macro checks, download, and a guarded Moonraker installation offer in the PCMA tab
- A separate Raspberry Pi build directory and an installer with backup, health checks and rollback

The generic [filament macros](klipper/README.md) implement manual maintenance on a fresh Klipper printer. They have configurable extrusion settings and an optional LCD menu; no existing machine-specific filament macros are needed. Automatic PCMA print starts require the separate full print/bed-confirmation contract and remain disabled by this bundle. The production queue and supervised hardware acceptance remain separate integration work.

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
| `make install` | Build and check, migrate configuration, generate profiles, enable the PCMA tab and restart with rollback |
| `make status` | Read the installed service state and version |

The default install target is `/home/administrator/prusa-connect/prusa-connect`, served by `prusa-connect.service`. One `make install` also updates the existing operator configuration, generates capability profiles from the registered Moonraker endpoints and enables the independent PCMA tab. The assistant asks about legacy connector migration, missing printer addresses and sudo credentials when needed. Existing endpoint identities and unrelated settings are retained. Configuration, resource overlay and binary are backed up together. Override `INSTALL_DIR`, `SERVICE` and `HEALTH_URL` when the existing installation differs. Sources and build outputs stay in the separate build directory. Installation does not copy production configuration or credentials into this repository. Materials must already be enabled; printer macros and hardware acceptance remain separate.

## Connect a printer

The public build uses neutral PCMA identifiers. Its connector instance is `pcma`, its type is `pcma-moonraker`, and its printer macro contract uses `PCMA_*`. The installer configures this instance and offers migration of legacy connector declarations.

For a manual installation instead of `make install`, enable a PCMA connector instance named `pcma` in the PC installation's `config/config.yaml`. If the file does not exist, create it. Preserve any existing settings and other connector declarations.

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

`make install` generates and registers these profiles. For a manual installation, read the printer's active configuration with the supplied tool after building:

```sh
./bin/pcma-profile -address http://printer.local:7125 -model Klipper -uploads -output /home/administrator/klipper-capabilities.json
```

The tool reads configuration, object availability and status. It sends no heater, motion or print commands. The new output file contains the profile registry; standard output contains selected configuration evidence and its SHA-256. Keep printer-specific files outside this repository. The model label must match the endpoint's model option.

Set `core.connectors.pcma.config.capability_profile_path` to the absolute output path, then restart PC. The patched host validates the registry with its existing profile loader. A matching profile removes `no_capability_profile`; live probing and connector policy still restrict the accepted commands. `uploads: false` and `starts: false` retain their effect.

Generation currently supports one extruder. Heater limits come from explicit configuration values. The firmware range begins at the observed version and ends before the next patch release; regenerate after firmware or configuration changes. This profile describes implemented adapter commands and does not approve a machine for unattended printing. Manual motion requires known axis limits and homed axes; the adapter reads the current printer state before each action.

## Source layout

The independent [PCMA UI module](pcma-ui/README.md) adds a PCMA tab to the printer page. It reads materials and spools through the existing Materials API, requests a physical change and records the selected assignment only after a matching local printer confirmation. The Materials extension is unchanged, including its existing Change button. The Pi installer deploys the PCMA bundle together with the core binary.

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

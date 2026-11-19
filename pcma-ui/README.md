# PCMA filament workflow

This browser module adds a **PCMA** tab to the printer page. It uses the published Prusa Connect SDK and the typed Materials client. It does not change the Materials extension or intercept its existing Change button. The macro section checks the generic single-extruder Klipper bundle and offers download, installation and a separately confirmed restart through the PCMA setup service.

Select a material and optionally a concrete spool. PCMA reads its material type and RGB color again before requesting the change. A spool assigned elsewhere, archived stock, resin or unknown color is refused before sending a printer command.

The request is written to PC's module records before the physical command is sent. Its UUID is passed as both the PC command key and the macro's `REQUEST` argument. A page reload or lost response never resends the command. Records belong to the signed-in person; reopen the tab as that person to continue an interrupted workflow.

PCMA waits for `change_id` and `confirmed_change_id` to match the saved UUID, `filament_phase=CONFIRMED`, and matching `loaded_material` and `loaded_color`. Only then does it call the Materials assignment API for tool `0`. The selected spool ID is retained for the eventual Materials assignment. The unchanged Materials API refuses assignment during PRINTING and PAUSED. In those states the confirmed request remains pending; retry the metadata assignment once the printer is idle. Consumption cannot be attributed to the new spool through this assignment until Materials accepts it. Install the generic manual macros and complete local confirmation; see [Klipper setup](../klipper/README.md). The full automatic print-start contract is separate.

If assignment fails, **Check confirmation / assignment** retries the metadata step. It never resends a physical command. An independent assignment change is checked against the original material/spool identity. Its update time is retained for the journal, but a report-only timestamp refresh is not a different assignment. The exact printer request, type and color must still be confirmed before writing. Materials keeps authority over its own labels and aliases. An archive is checked again before continuing an observed pending request. The public APIs do not offer a transaction across PC records, printer state and Materials: a write already in flight may still finish. Archiving does not cancel printer activity. Check the printer before starting another request.

## Build

The repository's Pi `make` builds the shared UI library, the unchanged Materials API client and this bundle. The output is `pcma-ui/dist`, including `module.yaml`, `ui/index.js` and translations. For local development, build the shared library and Materials client first, then run these commands from `pcma-ui`:

```sh
pnpm install --frozen-lockfile
pnpm run check
pnpm run build
```

## Install

On the Pi, run `make install` in the prepared build directory. It generates and registers capability profiles, installs this asset and enables the PCMA tab in the existing configuration. Materials `>=0.2.5 <0.3.0` must already be enabled. Existing user roles still apply. No separate resource merge or service restart is needed.

For a manual installation elsewhere, use the PCMA adapter build and a regenerated capability profile that contains the new `request_id` argument. Copy the bundle and print a ready-to-merge resource declaration with:

```sh
python3 pcma-ui/prepare-install.py /home/administrator/prusa-connect/modules/pcma-tools-0.3.5
```

The destination must be new. The helper calculates the same directory digest as PC and prints a declaration under `resources.asset.pcma-tools`. For this manual path, merge it into the existing PC configuration, preserving other resources, then restart PC. Materials is not replaced.

The person using the workflow needs access to the PCMA asset and permission to configure its records, permission to operate the printer, and the Materials permissions needed to read the catalogue and assign a tool. A refused record write stops the workflow before a physical command. The browser uses its existing signed-in session and holds no copied API token.

The UI runs while the tab is open. Reopening it resumes confirmation and assignment checks. A separate server worker is not included in this browser module.

Built on Prusa Connect Local by Prusa Research a.s.; maintained by D3ADD3V3R. The repository's [license](../LICENCE.md) applies.

The setup service uses the host module protocol and the browser session. It accepts only configured connector/address pairs and checks configure permission on the selected printer before writes or restart. Installation backs up and verifies configuration, and does not restart Klipper. The second confirmation requests restart only while idle. Custom Resume or partial/conflicting macros require manual integration. Existing variables-file settings are preserved. The file API has no cross-client transaction: do not edit through other clients during installation. An uncertain write is never replayed automatically.

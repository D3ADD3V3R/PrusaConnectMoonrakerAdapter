# PCMA filament workflow

This browser module adds a **PCMA** tab to the printer page. It uses the published Prusa Connect SDK and the typed Materials client. It does not change the Materials extension or intercept its existing Change button.

Select a material and optionally a concrete spool. PCMA reads its material type and RGB color again before requesting the change. A spool assigned elsewhere, archived stock, resin or unknown color is refused before sending a printer command.

The request is written to PC's module records before the physical command is sent. Its UUID is passed as both the PC command key and the macro's `REQUEST` argument. A page reload or lost response never resends the command. Records belong to the signed-in person; reopen the tab as that person to continue an interrupted workflow.

PCMA waits for `change_id` and `confirmed_change_id` to match the saved UUID, `filament_phase=CONFIRMED`, and matching `loaded_material` and `loaded_color`. Only then does it call the Materials assignment API for tool `0`. The selected spool ID is retained for the eventual Materials assignment. The unchanged Materials API refuses assignment during PRINTING and PAUSED. In those states the confirmed request remains pending; retry the metadata assignment once the printer is idle. Consumption cannot be attributed to the new spool through this assignment until Materials accepts it. The printer macros and their local confirmation still need implementation; see [the contract](../klipper/adapter-contract.md).

If assignment fails, **Check confirmation / assignment** retries the metadata step. It never resends a physical command. An independent assignment change is checked against the original material/spool identity. Its update time is retained for the journal, but a report-only timestamp refresh is not a different assignment. The exact printer request, type and color must still be confirmed before writing. Materials keeps authority over its own labels and aliases. An archive is checked again before continuing an observed pending request. The public APIs do not offer a transaction across PC records, printer state and Materials: a write already in flight may still finish. Archiving does not cancel printer activity. Check the printer before starting another request.

## Build

The repository's Pi `make` builds the shared UI library, the unchanged Materials API client and this bundle. The output is `pcma-ui/dist`, including `module.yaml`, `ui/index.js` and translations. For local development, build the shared library and Materials client first, then run these commands from `pcma-ui`:

```sh
pnpm install --frozen-lockfile
pnpm run check
pnpm run build
```

## Install

Use the PCMA adapter build and a regenerated capability profile that contains the new `request_id` argument. Materials `0.2.5` must already be installed. Copy the bundle and print a ready-to-merge resource declaration with:

```sh
python3 pcma-ui/prepare-install.py /home/administrator/prusa-connect/modules/pcma-tools-0.3.4
```

The destination must be new. The helper calculates the same directory digest as PC and prints a declaration under `resources.asset.pcma-tools`. Merge it into the existing PC configuration, preserving other resources, then restart PC. The build's `make install` updates the core binary; it does not silently rewrite module declarations or replace Materials.

The person using the workflow needs access to the PCMA asset and permission to configure its records, permission to operate the printer, and the Materials permissions needed to read the catalogue and assign a tool. A refused record write stops the workflow before a physical command. The browser uses its existing signed-in session and holds no copied API token.

The UI runs while the tab is open. Reopening it resumes confirmation and assignment checks. A separate server worker is not included in this browser module.

Built on Prusa Connect Local by Prusa Research a.s.; maintained by D3ADD3V3R. The repository's [license](../LICENCE.md) applies.

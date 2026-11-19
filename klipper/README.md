# PCMA on a fresh Klipper printer

The generic [pcma-filament.cfg](../adapter/macroconfig/pcma-filament.cfg) adds a manual filament workflow for one extruder. It uses its own extrusion routines and does not require existing LOAD_FILAMENT or UNLOAD_FILAMENT macros. The filament routines never home or move X, Y or Z. An explicit Resume delegates to Klipper and can return to the saved print position. Automatic print starts are not implemented by this file: `contract_version` stays `0`; `filament_contract_version` is `1`.

Copy the file into Klipper's configuration directory and add this line to `printer.cfg`, before the generated SAVE_CONFIG block:

```ini
[include pcma-filament.cfg]
```

Restart Klipper while idle. The file supplies `[pause_resume]`, `[save_variables]` and a guarded `RESUME` for a fresh configuration. If any of these already has custom settings, or a custom `RESUME` exists, merge those explicitly first. Do not overwrite an existing variables-file setting or an existing resume routine by including a second definition.

Set the temperature map, load/unload distances, purge distance and extrusion speed under `PCMA_STATE` for the installed extruder and materials. Defaults are 40 mm load/unload, 5 mm purge and 2 mm/s; these are starting values, not a machine calibration. Unloading uses the old confirmed material's temperature. If that material is unknown, it uses the configurable `unload_temperature`, initially 220 °C. Loading uses the requested new material's temperature. Every extrusion checks the configured temperature and extrusion-only limits. The routine heats only for a requested action, turns the nozzle target off afterward and restores the G-code positioning modes without moving axes.

After the first boot, inspect the printer locally and call `PCMA_RESET_FILAMENT`. A change from PC carries material, `C_RRGGBB` color and request ID. It unloads, then waits for physical insertion. Call `PCMA_LOCAL_LOAD`, optionally `PCMA_PURGE_FILAMENT`, and then `PCMA_CONFIRM_FILAMENT` after checking the material and color. Confirmation never sends Resume. A paused print resumes only on an explicit `RESUME`; the wrapper refuses while a change or recovery is pending.

On a printer with an LCD menu, optionally also include [pcma-menu.cfg](pcma-menu.cfg). A printer without a display uses the main file alone. Confirmation commands can be invoked from a Klipper console: macros cannot prove that an authenticated G-code caller is physically at the printer. PCMA deliberately does not expose a remote confirmation button.

The request record is persisted before physical action. `AWAITING_INSERTION` permits one local load; `LOADING` means the physical load has started. A failed final record write therefore cannot become a second load. A separate unload ends in `UNLOADED`, which blocks Resume. A repeated current request does not start a second action. A restart never heats or resumes; it invalidates the material claim and requires local recovery. `PCMA_RESET_FILAMENT` discards the active claim after inspection and retains the last request ID to prevent replay. Do not retry an action automatically after a timeout or communication loss.

## Installation from PCMA

The PCMA tab checks the macro objects and manual contract. It offers this generic file for download. Its separate setup service can install the compiled bundle through Moonraker when the config root is writable, the main file is at that root and the printer is idle. A fresh built-in `RESUME` is supported. Existing custom Resume, partial PCMA definitions and modified PCMA files require manual review; the installer does not overwrite them. An existing `save_variables` filename and `pause_resume` settings are preserved.

The API first copies and verifies a backup of the main configuration. It uploads and reads back the bundle, checks the main configuration digest again and inserts the include before SAVE_CONFIG. This does not restart Klipper. A second confirmed action requests restart, after another idle/digest check. Lost acknowledgements are not retried; refresh the plan instead. Moonraker's upload API has no cross-client compare-and-swap transaction, so do not edit the configuration from another client during this operation. The backup stays available after an error.

The setup service accepts only addresses configured by the PC installer, bound to the host-reported connector of the selected device. Reads use the signed-in caller's device access. Writing and restart additionally require configure permission on that device. Neither route accepts arbitrary files or G-code from the browser.

The templates have offline render/replay tests using Klipper's Jinja delimiters. They still need supervised hardware acceptance for the printer they are installed on. See the [contract](adapter-contract.md) for the separate automatic-print workflow.

Run the offline tests in a separate environment:

```sh
python3 -m venv .tools/klipper-check
.tools/klipper-check/bin/pip install -r tests/requirements-macros.txt
.tools/klipper-check/bin/python tests/klipper_macros.py
```

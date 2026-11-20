# Moonraker connector 0.3.6

Der Connector erweitert das offizielle Moonraker-Beispiel aus Prusa Connect `v0.1.0-beta.1`, Commit `b194cb83b682822e95e839a3cecf4391f3929e87`. Die ursprüngliche Lizenz liegt in `LICENSE`; die Projektbedingungen stehen in `../LICENCE.md` und `../NOTICE.md`.

Der Adapter bietet Status, Heizung, Bewegung, Homing, Mesh, Filamentwechsel, Dateiverwaltung und geprüfte Uploads. Vorbereitete Druckstarts verwenden ein dauerhaftes SQLite-Journal und lokale Material-/Bettfreigaben. Binär-G-Code, mehrere Materialien pro Datei und Moonraker-API-Key-Authentifizierung sind nicht implementiert.

## Druckstarts konfigurieren

Upload und Start sind standardmäßig deaktiviert. Eine Instanz für einen Drucker mit passendem Makrovertrag benötigt zum Beispiel:

```yaml
core:
  connectors:
    klipper:
      type: pcma-moonraker
      config:
        uploads: true
        starts: true
        state_path: /var/lib/pcma/executions.db
        profile_id: klipper-single
        profile_version: r1
        materials: [PLA, PETG]
        capability_profile_path: /var/lib/pcma/capabilities.json
```

Das Capability-Profil wird mit `pcma-profile` aus dem Drucker erzeugt. Es beschreibt die verfügbaren Befehle und Maschinenlimits. Profil-ID und Revision binden vorbereitete Dateien an die konfigurierte Freigabe; sie ersetzen keine Prüfung des tatsächlichen G-Codes und keine Hardwareabnahme. Unterschiedliche Profile benötigen getrennte Connectorinstanzen. Der Endpunkt braucht eine stabile `unit_uid`.

Auf dem Drucker müssen `virtual_sdcard`, `pause_resume`, `save_variables` und der vollständige PCMA-Vertrag Version 1 vorhanden sein. Das generische Filamentpaket in diesem Repository stellt nur den manuellen Vertrag bereit; der vollständige Druckvertrag ist in `../klipper/adapter-contract.md` beschrieben und muss passend zum Drucker integriert und abgenommen werden.

## Normaler PC-Dateiablauf

Bei aktivierten Druckstarts bereitet PCMA eine normale `.gcode`-Datei automatisch vor, bevor PC den Transfer erfasst. Dazu dient eine optionale Vorbereitungsschnittstelle im gepflegten Hostpatch. Der Host verwendet anschließend die abgeleiteten Bytes, deren Größe und den tatsächlichen Zielpfad. Andere Treiber behalten ihren bisherigen Ablauf.

Die Quelldatei im PC-Speicher bleibt unverändert. Eine neue Übertragung einer Originaldatei erhält eine neue Ausführungs-ID. Auf dem Drucker landet sie unter `gcodes/pcma/<execution_id>.gcode`. Der normale PC-Druckdialog startet genau diesen Pfad; ein zusätzlicher Terminalbefehl ist nicht erforderlich.

Material und Farbe werden getrennt aufgelöst:

- Unterstützte vorhandene Slicerwerte bleiben maßgeblich.
- Nur ein fehlender Typ wird aus dem lokal bestätigten geladenen Typ ergänzt.
- Nur eine fehlende Farbe wird aus der lokal bestätigten geladenen RGB-Farbe ergänzt.
- Unbekannte oder widersprüchliche erforderliche Werte verhindern die Vorbereitung.

Eine Datei muss ein Material und eine RGB-Farbe tragen. Mehrdeutige Mehrmaterial-Kommentare, spätere Toolwechsel und `M600` werden abgewiesen. Der Vorbereitungsweg führt höchstens eine Vorbereitung pro Sitzung aus, begrenzt die Größe auf 512 MiB und verwendet temporäre Dateien statt eines Puffers in Dateigröße.

Bereits vorbereitete Dateien behalten ihre Ausführungs-ID und müssen zum Profil passen. Eine verifizierte oder bereits verwendete Ausführung wird durch einen erneuten Upload nicht ersetzt. Zum erneuten Drucken die Originaldatei neu senden; damit entsteht eine neue Ausführung. Für einen schon übertragenen, noch nicht gestarteten Auftrag kann die vorhandene Druckerdatei ausgewählt werden.

Das separate Werkzeug `pcma-prepare` bleibt für Integrationen verfügbar. Der normale PC-Upload benötigt es nicht.

## Journal und lokale Freigaben

Die Übertragung prüft Ziel, Länge und SHA-256 und liest die gespeicherten Bytes zurück. Erst danach wird der Artefaktbeleg als verifiziert markiert. Das Journal bindet Profil, Originalhash, abgeleiteten Hash, Ausführungs-ID und Zielpfad.

Vor einem Start speichert der Adapter die Reservierung und verbraucht genau eine passende lokale Bettbestätigung. Eine verlorene Startantwort löst keinen zweiten Start aus. Kommunikationsverlust und widersprüchliche Belege lassen einen Auftrag gesperrt. Eine lokale Wiederherstellung auf dem Drucker umgeht keine noch aktive Journalreservierung; deren Ende-zu-Ende-Abgleich bleibt Teil der Abnahme.

Nach Neustart werden Material und Bett nicht automatisch als geprüft übernommen. Materialabweichungen halten denselben Druck vor der normalen Slicer-Startsequenz an. Die konkrete lokale Bestätigung und Resume-Bedienung richten sich nach dem integrierten Druckerprofil.

## Build und Prüfung

`deploy/rpi` enthält den separaten Pi-Build und Installer. `make install` sichert und aktualisiert den PC-Dienst, Konfiguration, Capability-Profile, PCMA-UI und Setup-Service. Bestehende Startschalter und Journal-/Profilbindungen bleiben erhalten. Die Druckerkonfiguration wird dadurch nicht automatisch verändert.

Adaptertests prüfen Vorbereitung, unabhängige Metadatenrückfälle, Wiederholung, Abbruch, parallele Transfers, Journal, lokale Freigaben und unklare Startantworten. `tests/pc_smoke.py --automatic-preparation` prüft den normalen Rohdatei-Upload gegen einen isolierten PC und Moonraker-Simulator. Eine physische Druckabnahme ersetzt dieser Test nicht.

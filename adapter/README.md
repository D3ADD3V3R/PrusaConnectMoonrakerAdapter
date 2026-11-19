# PrusaConnectMoonrakerAdapter Moonraker-Connector 0.3.0

Der Connector erweitert das offizielle PC-Moonraker-Beispiel um Dateiverwaltung, vorbereitete Ausführungen, ein dauerhaftes Startjournal und den Abgleich lokaler Material-/Bettzustände. Der registrierte Typ lautet `pcma-moonraker`.

Die übernommenen Go-Dateien, Tests, der Simulator und die Grafik stammen aus `examples/adapters/moonraker` im Prusa-Commit `b194cb83b682822e95e839a3cecf4391f3929e87`, Release `v0.1.0-beta.1`. Der separate Checkout unter `../upstream/prusa-connect` liefert SDK und PC-Kern. Die ursprüngliche Lizenz liegt in `LICENSE`. Die Übernahme als Quellcode erlaubt die SDK-Conformance-Prüfung im eigenen Go-Modul, nachdem der Import des registrierenden Beispiels im Go-Testprogramm an fehlenden Modulmetadaten scheiterte.

## Implementierter Umfang

- PC-Registrierung, Geräteprobe, WebSocket-Status, Pause, Resume und Cancel auf Basis des Beispiels.
- SDK-Dateibaum für `gcodes`, Verzeichnis- und Einzeldateiabfragen, Ordner anlegen, Dateien und leere Ordner löschen. Schreibgeschützte Wurzeln begrenzen die Capabilities.
- Optionaler Upload mit Prüfung von Zielpfad, Größe und SHA-256. Der Adapter setzt `print=false`, prüft die Upload-Antwort und liest die Datei zur Inhaltsprüfung zurück. Ein temporärer Multipart-Spool hält den Speicherverbrauch unabhängig von der G-Code-Größe.
- Vorbereitung einer abgeleiteten Datei über das Paket `gcode` oder `pcma-prepare`. Material und Farbe werden aus unterstützten Slicer-Kommentaren gelesen oder explizit übergeben. Ein passender früher Hook bleibt erhalten. Originalbytes, Originalhash, abgeleiteter Hash, Profilrevision und Ausführungs-ID bleiben zugeordnet.
- Optionale native PC-Druckstarts mit dauerhaftem SQLite-Journal. Die verwendete SQLite-Bibliothek ist auf dieselbe Version wie im geprüften PC-Kern gepinnt. Pro Drucker erlaubt das Journal eine aktive Ausführung. Ausführungs-ID und Artefaktinhalt sind unveränderlich gebunden.
- Persistenz des Startversuchs vor dem Wire-Aufruf. Ein unklarer Start wird anhand von Dateiname, Ausführungs-ID und Druckerzustand abgeglichen. Ein erneuter Start derselben Ausführung sendet den Druckbefehl nicht noch einmal.
- Stabile PC-Job-ID `pcma-<execution_id>` für vorbereitete Dateien, auch beim Reconnect. Makrophasen werden unter `/pcma/*` gespiegelt. Eine Materialpause behält den Fachstatus `PRINT_ONGOING` und blockiert reguläres Resume.
- Generationen- und sequenzgebundene Übernahme lokaler Bettbestätigungen. Der Adapter speichert die Bestätigung vor der Quittierung an Klipper. Beim nächsten Start wird diese Freigabe atomar mit der Reservierung verbraucht.

## Konfiguration und Vorbereitung

Dateiabfragen werden anhand der Geräteprobe angeboten. Upload und Start sind standardmäßig deaktiviert. Ein Beispiel für eine spätere Connectorinstanz folgt. Die Werte müssen zu den tatsächlich freigegebenen Profilen passen.

```yaml
core:
  connectors:
    ender3:
      type: pcma-moonraker
      config:
        uploads: true
        starts: true
        state_path: /var/lib/pcma/executions.db
        profile_id: ender3
        profile_version: v1
        materials: [PLA, PETG]
```

Die Profilbindung gilt je Connectorinstanz. Unterschiedliche Profile brauchen getrennte Instanzen. Der Drucker-Endpunkt wird über die native PC-Endpunktverwaltung angelegt. `unit_uid` muss eine stabile physische Identität tragen, wenn sich die IP ändern kann.

Ein Konfigurationsprofil lässt sich mit `pcma-profile` aus dem verbundenen Drucker erzeugen. Die Anleitung im Haupt-README beschreibt den Aufruf und `capability_profile_path`. Das Profil begrenzt die angebotenen Adapterbefehle anhand der Konfiguration; es ersetzt keine technische Druckfreigabe. Die Laufzeitprüfung und die Schalter `uploads` und `starts` gelten weiterhin.

Start wird nur angeboten, wenn Upload, Cancel, ein einzelner Extruder, `save_variables` und der Makro-Vertrag Version 1 vorhanden sind. Der reale Drucker hat diese PCMA-Makros bisher noch nicht. Der genaue Status- und ACK-Vertrag steht in `../klipper/adapter-contract.md`.

Eine freigegebene Datei wird vor ihrem Upload vorbereitet. Unter Windows beispielsweise so aufrufen.

```powershell
./Moonraker-Adapter/bin/pcma-prepare.exe -input freigegeben.gcode -output job-001.gcode -execution job-001 -profile ender3 -profile-version v1 -color WHITE
```

Das Werkzeug erzeugt eine neue Datei und gibt ihren Artefaktbeleg als JSON aus. Vorhandene Ausgabedateien werden durch den exklusiven Erstellungsmodus geschützt. Der Zielpfad auf dem Drucker muss `gcodes/pcma/job-001.gcode` lauten. Ein nativer PC-Upload überträgt anschließend genau diese vorbereitete Datei.

Die Vorbereitung erfolgt vor `Transfers.Send`, damit PC beim Upload dieselben Bytes, dieselbe Größe und dieselben Hashes verfolgt wie der Adapter. Die PC-Extension muss den Paketaufruf noch in ihren Freigabe-/Artefaktablauf einbinden. Das CLI dient derzeit als überprüfbarer Einstieg. Die technische Freigabe prüft den tatsächlichen G-Code gegen die Maschine und das Profil.

## Prüfung

Benötigt werden Git, Python 3 und Go 1.26.11. `dev.ps1` nutzt die projektlokale oder eine installierte Go-Toolchain. Aus dem PrusaConnectMoonrakerAdapter-Projektverzeichnis ausführen.

```powershell
./Moonraker-Adapter/dev.ps1 test
./Moonraker-Adapter/dev.ps1 build
./Moonraker-Adapter/dev.ps1 smoke
```

`test` führt die übernommenen Tests, die SDK-Conformance-Suiten, Datei-, Artefakt- und Ausführungsprüfungen sowie `go vet` aus. Getestet werden unter anderem beschädigte Uploads, Pfadwechsel, unveränderliche Artefakte, gleichzeitige Reservierungen, verlorene Start-ACKs, Wiederöffnung des Journals, unbekannte Starts, Materialpausen und verspätete Bettfreigaben.

`build` erzeugt den Simulator, den Profilgenerator, das Vorbereitungswerkzeug und den Windows-PC-Build unter `bin`. `smoke` startet eine isolierte PC-Instanz mit eigener temporärer Datenbank auf Loopback. Er prüft über native PC-APIs Geräteanlage, Dateiansicht, Upload und Druckstart. Die simulierte Materialpause muss dabei dieselbe Ausführung und `PRINT_ONGOING` behalten. Reguläres Resume wird in dieser Phase abgewiesen. Testprozesse und temporäre Daten werden danach entfernt.

## Build und Installation auf PrinterFarmHost

Der native ARM64-Build mit eingebetteter Weboberfläche, `make smoke` mit Simulator sowie `make test` einschließlich `go vet` wurden auf PrinterFarmHost erfolgreich abgeschlossen. Die vier Binärdateien liegen unter `bin`; `SHA256SUMS`, `BUILDINFO.txt` und `VERSION` dokumentieren den Build.

Der Distributionsstand `0.1.0-beta.1+pcma.0.3.4` ergänzt unter „Add a Printer“ die Auswahl „Klipper / Moonraker“. Nach einmaliger Aktivierung einer Connectorinstanz namens `pcma` lassen sich Drucker dort mit Namen und Adresse hinzufügen. Die Einrichtung nutzt die Browser-Sitzung, unterscheidet gespeicherte und tatsächlich verbundene Geräte und bietet eine gegen die Serverversion geprüfte Adresskorrektur. Der Quellpatch liegt unter `../deploy/rpi/upstream.patch`.

Der separate Quellstand liegt auf dem Pi unter `/home/administrator/pcma-build`, die bisherige Installation unter `/home/administrator/prusa-connect`. Makefile und Skripte werden lokal unter `../deploy/rpi` gepflegt. Die dortige README beschreibt `make`, `make test`, `make smoke` und `make install` sowie Sicherung, Dienstneustart und automatische Rücksicherung bei fehlgeschlagener Gesundheitsprüfung. Das Installationsskript wurde mit einem isolierten Dienstmodell einschließlich Rücksicherung geprüft.

## Verbleibende Integration

Klipper-Makros, Original-LCD-Menüs, temperatur- und hardwareabhängige Lade-/Entladewege, Purge sowie lokale Wiederherstellung nach Stromausfall sind noch umzusetzen. Die vorhandenen Tests prüfen einen Simulator dieses Vertrags. Eine tatsächliche Hardwareabnahme steht aus.

Die Fachmodelle und Queue der PC-Extension müssen Reservierung, Artefaktvorbereitung, Unterphasen und Bettzustand noch in den gesamten Produktionsablauf einbinden. Eine eigene PCMA-Anzeige in der PC-Oberfläche ist offen. Der Windows-Compose-Build prüft die API-Anbindung. Der native Linux-ARM64-Build enthält zusätzlich das eingebettete PC-Webfrontend; dessen HTML und JavaScript sowie Geräteanlage, Upload, Start und Materialpause wurden in einer isolierten PC-Instanz auf PrinterFarmHost erfolgreich geprüft.

Die Übernahme in den produktiven PC-Dienst erfolgt über `make install`; sie wurde bislang nicht ausgeführt. Moonraker-Tokenauthentifizierung, weitere Slicerformate, Binär-G-Code und mehrere Materialien pro Datei sind noch offen. Die Metadatenprüfungen basieren zunächst auf PrusaSlicer-Schlüsseln, die auch in einer realen vorhandenen Datei geprüft wurden.

Ein ungeklärter Start bleibt gesperrt, wenn der Drucker keine passende Ausführungs-/Dateiidentität liefert. Die explizite lokale Wiederherstellung dafür muss gemeinsam mit den Makros ergänzt werden. Ein bekanntermaßen abgewiesener Start gibt seine Reservierung und unbenutzte Bettbestätigung wieder frei.

HTTP-Aufrufe sind zusätzlich zum Aufruferkontext auf fünf Minuten begrenzt. Der Upload meldet Fortschritt vor der Übertragung und nach bestätigtem Inhalt. Bei unklarem Uploadfehler kann bereits eine Datei am Ziel liegen. Änderungen durch Moonrakers G-Code-Nachverarbeitung führen zu einer abgelehnten Inhaltsprüfung und müssen vor dem Produktionseinsatz untersucht werden. Der Go-Race-Detector wurde auf diesem Windows-System bislang nicht ausgeführt.

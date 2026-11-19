# PrusaConnectMoonrakerAdapter-Build auf PrinterFarmHost

Der vorbereitete Quellstand liegt unter `/home/administrator/pcma-build-0.3.4-f0aba7d`. Das Installationsziel ist `/home/administrator/prusa-connect/prusa-connect`, der Dienst heißt `prusa-connect.service`.

Als `administrator` ausführen.

Für einen frischen Checkout zuerst `python3 deploy/rpi/stage.py "$HOME/pcma-build"` im Repository ausführen. Das kopiert die benötigten Quellen in den separaten Buildordner. Beim ersten `make` wird der gepinnte Prusa-Checkout geladen; `upstream.patch` ergänzt die Moonraker-Einrichtung in der Oberfläche.

```sh
cd /home/administrator/pcma-build-0.3.4-f0aba7d
make install
```

`make` richtet bei Bedarf Go 1.26.11, Node 24.22.3 und pnpm 11.3.0 unter `.tools` ein. Go- und Node-Archive werden mit den hinterlegten offiziellen SHA-256-Prüfsummen geprüft. Der Prusa-Kern ist auf Commit `b194cb83b682822e95e839a3cecf4391f3929e87` gepinnt. Der Build umfasst das eingebettete PC-Webfrontend, den PCMA-Connector, das Vorbereitungswerkzeug und den Profilgenerator. Die Ergebnisse liegen unter `bin`.

`make install` baut und prüft zuerst alle Komponenten. Danach erledigt der Installer die Einrichtung:

- Den aktiven Konfigurationspfad aus dem Dienst lesen; ohne `--config` gilt `config/config.yaml`. Ein ausdrücklich im Dienst eingetragenes `config.yml` wird ebenfalls verwendet.
- Vorhandene Moonraker-Endpunkte lesen und aktuelle Capability-Profile direkt aus den Druckern erzeugen. Dabei werden keine Bewegungs-, Heiz- oder Druckbefehle gesendet.
- Alte Moonraker-Connectortypen nach einer Rückfrage auf `pcma-moonraker` umstellen. Instanznamen, Endpunkte und vorhandene Druckerzuordnungen bleiben erhalten. Die Instanz `pcma` für „Add a Printer“ wird ergänzt.
- Das PCMA-UI-Paket in ein eigenes Verzeichnis unter der Installation kopieren und den Reiter über `resources.asset.pcma-tools` aktivieren. Materials bleibt unverändert.
- Einen fehlenden Datensatzspeicher als `resources.db.pcma-records` mit SQLite unter dem Konfigurationsordner einrichten. Ein vorhandener aktiver Speicher wird unverändert verwendet; mehrere aktive Speicher werden vor dem Austausch abgewiesen. Dieser Speicher hält die PCMA-Wechselaufträge dauerhaft.
- Binary, Konfiguration und Ressourcendatei sichern, die Änderungen übernehmen und den Dienst starten. Bei einem Fehler werden Binary, Konfiguration und Ressourcendatei zurückgesetzt; neu angelegte Profile und UI-Pakete werden entfernt.

Der Assistent fragt bei Bedarf nach der Moonraker-Adresse und nach dem sudo-Passwort. Verlangt PC eine Anmeldung, fragt er außerdem verdeckt nach einem PC-API-Token mit Leserechten auf die Ressourcen und das PCMA-Asset. Nach erfolgreicher Prüfung speichert der Installer den Token lokal in `INSTALL_DIR/.pcma-installer-token.json` mit Dateirechten `0600`, gebunden an die PC-API-Adresse. Updates verwenden ihn ohne erneute Eingabe; er gehört nicht in den Quell- oder Buildordner und wird nicht mit den Installationssicherungen kopiert. Ein widerrufener oder abgelaufener Token muss ersetzt werden. Frühere Installer haben den Token nicht gespeichert; ein damals verlorener Token lässt sich nicht wiederherstellen und muss einmal neu erstellt werden. Vor dem Austausch prüft der Installer den API-Zugriff; nach dem Start prüft er zusätzlich den Zustand des PCMA-Assets und dessen veröffentlichten UI-Einstieg. Für die vorhandene Installation braucht es keine zusätzliche manuelle YAML-Bearbeitung. Ein weiteres `make install` aktualisiert dieselbe Einrichtung. Bestehende Upload-/Startschalter und fremde Einstellungen bleiben erhalten; neue Connectorinstanzen erhalten `starts: false`.

Voraussetzung ist ein aktiviertes Materials-Modul in Version `>=0.2.5 <0.3.0`. Die Berechtigungen des angemeldeten PC-Benutzers bleiben maßgeblich; der Installer erweitert keine Benutzerrollen. Die Druckermakros und deren Hardwareprüfung sind ein separater Schritt.

Für automatisierte Aufrufe lässt sich die Migration mit `INSTALL_MIGRATE=1` bestätigen. `INSTALL_MOONRAKER_ADDRESS` liefert eine Adresse, wenn noch kein Endpunkt vorliegt. `INSTALL_PC_TOKEN` liefert bei Bedarf den PC-API-Token. `INSTALL_CONFIG` benennt eine abweichende Konfigurationsdatei; der Dienst muss diese Datei ebenfalls verwenden. Fehlende Angaben führen ohne Terminal vor dem Austausch zum Abbruch. Drucker desselben Modells mit unterschiedlichen Firmwarebereichen oder Capabilities brauchen getrennte Connectorinstanzen.

Der Build verwendet einen Go-Prozess und begrenzte Go-/Node-Speichereinstellungen für den Pi mit etwa 1 GB RAM. Temporäre Dateien und Caches liegen auf der SD-Karte im Build-Verzeichnis. Der erste Build lädt die gepinnten Abhängigkeiten und benötigt entsprechend Zeit.

Die Build-Version lautet `0.1.0-beta.1+pcma.0.3.5`. Der öffentliche Update-Prüfschlüssel wurde aus den Build-Metadaten der vorhandenen offiziellen Binary übernommen. Der Dokumentationslink verweist auf die zugrunde liegende PC-Beta-Version.

Wie die vorhandene Release-Binary verwendet der Build `CGO_ENABLED=0` und die Tags `full`, `diagnostics`, `observability` und `postgres`.

`make test` führt die Adapter- und SDK-Tests sowie `go vet` aus. `make smoke` baut und prüft eine separate PC-Instanz auf Loopback, einschließlich Webfrontend, Geräteanlage, Dateiansicht, Upload, Druckstart und simulierter Materialpause. Die Testinstanz verwendet temporäre eigene Daten und den Simulator.

Die Sicherungen liegen in einem datierten Unterordner von `/home/administrator/prusa-connect/backups`. Der Dienst wird für den Austausch gestoppt. Die Kontrolle vergleicht den Gesundheitsendpunkt mit der Build-Version und die laufende Prozessdatei mit der erzeugten SHA-256. Der Sicherungspfad wird ausgegeben.

Die Datenbank und Druckerhistorie werden nicht ersetzt. Der Installer schaltet automatische Druckstarts nicht zusätzlich frei. Deren Voraussetzungen stehen in `adapter/README.md` und `klipper/adapter-contract.md`.

Weitere Ziele lassen sich mit `make help` anzeigen. `make status` liest Dienstzustand und Version. Die Make-Variablen `INSTALL_DIR`, `SERVICE` und `HEALTH_URL` passen Ziel und Gesundheitsprüfung bei Bedarf an. Die Standardwerte entsprechen dem geprüften PrinterFarmHost.

Ab Version `pcma.0.3.5` installiert derselbe Aufruf auch den getrennten PCMA-Setup-Dienst. Er nutzt die bestehende Sitzung und vom Installer aus den Connector-Endpunkten übernommene Adressen; Schreibzugriffe verlangen zusätzlich Configure-Rechte auf den ausgewählten Drucker. Der PCMA-Reiter prüft Makros und bietet die generische Datei an. Erst ein bestätigter Klick schreibt sie über Moonraker; ein weiterer bestätigter Klick startet Klipper neu. `make install` selbst verändert keine Druckerkonfiguration und startet Klipper nicht neu. Der Dienst nutzt ein auf Linux/ARM64 geprüftes Prozessbudget von 32 MiB. Die Makrodateien stehen unter `adapter/macroconfig/pcma-filament.cfg` und optional `klipper/pcma-menu.cfg`; Einbindung und Hardwareparameter beschreibt `klipper/README.md`.

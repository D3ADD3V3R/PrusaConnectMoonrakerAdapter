# PrusaConnectMoonrakerAdapter-Build auf PrinterFarmHost

Die Quellen liegen unter `/home/administrator/pcma-build`. Das Installationsziel ist `/home/administrator/prusa-connect/prusa-connect`, der Dienst heißt `prusa-connect.service`.

Als `administrator` ausführen.

Für einen frischen Checkout zuerst `python3 deploy/rpi/stage.py "$HOME/pcma-build"` im Repository ausführen. Das kopiert die benötigten Quellen in den separaten Buildordner. Beim ersten `make` wird der gepinnte Prusa-Checkout geladen; `upstream.patch` ergänzt die Moonraker-Einrichtung in der Oberfläche.

```sh
cd /home/administrator/pcma-build
make
make install
```

`make` richtet bei Bedarf Go 1.26.11, Node 24.22.3 und pnpm 11.3.0 unter `.tools` ein. Go- und Node-Archive werden mit den hinterlegten offiziellen SHA-256-Prüfsummen geprüft. Der Prusa-Kern ist auf Commit `b194cb83b682822e95e839a3cecf4391f3929e87` gepinnt. Der Build umfasst das eingebettete PC-Webfrontend, den PCMA-Connector, das Vorbereitungswerkzeug und den Profilgenerator. Die Ergebnisse liegen unter `bin`.

Der Build verwendet einen Go-Prozess und begrenzte Go-/Node-Speichereinstellungen für den Pi mit etwa 1 GB RAM. Temporäre Dateien und Caches liegen auf der SD-Karte im Build-Verzeichnis. Der erste Build lädt die gepinnten Abhängigkeiten und benötigt entsprechend Zeit.

Die Build-Version lautet `0.1.0-beta.1+pcma.0.3.3`. Der öffentliche Update-Prüfschlüssel wurde aus den Build-Metadaten der vorhandenen offiziellen Binary übernommen. Der Dokumentationslink verweist auf die zugrunde liegende PC-Beta-Version.

Wie die vorhandene Release-Binary verwendet der Build `CGO_ENABLED=0` und die Tags `full`, `diagnostics`, `observability` und `postgres`.

`make test` führt die Adapter- und SDK-Tests sowie `go vet` aus. `make smoke` baut und prüft eine separate PC-Instanz auf Loopback, einschließlich Webfrontend, Geräteanlage, Dateiansicht, Upload, Druckstart und simulierter Materialpause. Die Testinstanz verwendet temporäre eigene Daten und den Simulator.

`make install` führt beide Prüfungen aus, legt eine datierte Sicherung der bisherigen Binary unter `/home/administrator/prusa-connect/backups` an und tauscht die Binary atomar aus. Danach startet es den vorhandenen Dienst über `sudo -n systemctl restart` neu. Die Kontrolle vergleicht den Gesundheitsendpunkt mit der Build-Version und die laufende Prozessdatei mit der erzeugten SHA-256. Bei einem Installationsfehler nach dem Austausch wird die Sicherung wiederhergestellt und der vorherige Dienstzustand wiederhergestellt. Der Sicherungspfad wird ausgegeben.

Der Installer verarbeitet die ausführbare Datei. Die bestehende PC-Konfiguration und ihre Daten werden vom gestarteten PC verwendet. Der PCMA-Typ ist im Build registriert. Seine Einrichtung als Connectorinstanz und das spätere Freischalten von Upload/Start richten sich nach `adapter/README.md` und `klipper/adapter-contract.md`.

Weitere Ziele lassen sich mit `make help` anzeigen. `make status` liest Dienstzustand und Version. Die Make-Variablen `INSTALL_DIR`, `SERVICE` und `HEALTH_URL` passen Ziel und Gesundheitsprüfung bei Bedarf an. Die Standardwerte entsprechen dem geprüften PrinterFarmHost.

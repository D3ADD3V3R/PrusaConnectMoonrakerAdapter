# Schnittstelle zwischen Adapter und Klipper-Makros

Stand 19.11.2026. Dies ist der im Adapter und Simulator implementierte Vertrag für die folgenden Makros. Das generische Paket `adapter/macroconfig/pcma-filament.cfg` implementiert den manuellen Filamentablauf für frisches Klipper mit einem Extruder. Es setzt `filament_contract_version=1`, aber ausdrücklich `contract_version=0`: Automatische Druckstarts, Bettfreigaben und deren Wiederherstellung sind damit nicht freigegeben. Der folgende vollständige Druckvertrag bleibt ein eigener Integrationsschritt. Die Hardwareabnahme erfolgt je Drucker; es gibt keine fest eingebaute Ender-Konfiguration.

## Statusobjekt

Klipper stellt `gcode_macro PCMA_STATE` bereit. Zusätzlich müssen `save_variables`, `virtual_sdcard` und `pause_resume` vorhanden sein. Die Statusfelder werden in PC unter `/pcma/<feldname>` gespiegelt.

| Feld | Typ und Bedeutung |
| --- | --- |
| `contract_version` | Integer `1` |
| `execution_id` | Aktuelle oder zuletzt gestartete Ausführungs-ID, anfangs leer |
| `phase` | `IDLE`, `PREPARING`, `MATERIAL_CHECK`, `MATERIAL_CHANGE`, `HEATING`, `PRINTING`, `PAUSED_OTHER`, `FINISHED`, `FAILED`, `CANCELLED` oder ein gesperrter Wiederherstellungszustand |
| `loaded_material` | Zuletzt lokal bestätigte Material-ID oder `UNKNOWN` |
| `loaded_color` | Zuletzt lokal bestätigte Farb-/Spulen-ID oder `UNKNOWN`. RGB-Farben haben die Form `C_FF8000`. |
| `bed_clear_confirmed` | Boolesche lokale Bestätigung, beim Start verbraucht |
| `bed_clear_for` | Genau die abgeschlossene Ausführung, deren Bett vor Ort geprüft wurde. Bei Erstinbetriebnahme `INITIAL`. |
| `bed_clear_sequence` | Persistenter positiver Integer, bei jeder neuen lokalen Bestätigung erhöht |
| `bed_clear_ack_for` | Generation, deren Bestätigung PC dauerhaft übernommen hat |
| `bed_clear_ack_sequence` | Zu dieser PC-Quittierung gehörende Sequenz |

Der Adapter leitet aus den aktiven Material-/Druckphasen zusätzlich `/pcma/execution_status=PRINT_ONGOING` ab. Der native PC-Druckerzustand kann dabei technisch `PAUSED` sein. Die PC-Extension verwendet die Ausführungs-ID und diese Fachphase für den Produktionsauftrag.

Die Bed-Sequenz darf durch Neustart oder Wiederholung eines Ereignisses nicht zurückgesetzt werden. Lokale Bestätigung und ihr Ausführungsbezug müssen dauerhaft gespeichert sein, bevor sie als gültig publiziert werden. Der reale Initialzustand verlangt eine lokale Bettprüfung. Der Simulator beginnt mit einer bereits vorgenommenen Initialprüfung als Testvoraussetzung.

## Früher Start

Die vorbereitete Datei enthält als ersten ausführbaren Befehl einen Aufruf dieser Form.

```gcode
PCMA_PRINT_START EXECUTION=job-1 MATERIAL=PLA COLOR=C_FF8000
```

Der Hook prüft die gültige Bettbestätigung samt PC-Quittierung erneut auf dem Drucker, verbraucht die Freigabe und persistiert die neue Ausführungs-ID vor weiteren Druckaktionen. Anschließend vergleicht er das angeforderte Material und die Farbe mit dem zuletzt lokal bestätigten Zustand. Bei Abweichung oder unbekanntem Zustand pausiert er denselben virtuellen SD-Druck vor der regulären Startsequenz. Die Temperaturwahl und Extruderwege stammen aus freigegebenen lokalen Hardware-/Materialprofilen.

`MATERIAL_CHANGE` bleibt bis zur lokalen Bestätigung aktiv. Die vorhandenen Wege für automatisches Laden, Filamentmenüs und reguläres Resume müssen diese Phase berücksichtigen. `Farbe OK` bestätigt den geladenen Zustand und setzt dieselbe Ausführung fort. Die wiederholbaren Purge-Aktionen gehören vollständig zur lokalen Routine.

## Manuelle Wartung aus PC

Das eigene PCMA-Modul stellt einen separaten Wechselablauf bereit. Materials und dessen vorhandener Change-Button bleiben unverändert. PCMA liest Material und optional Spule aus der Materials-API. Es speichert einen Auftrag vor dem Senden und übermittelt Materialtyp, RGB-Farbe und eine eindeutige Auftrags-ID, beispielsweise `PCMA_FILAMENT_CHANGE MATERIAL=PETG COLOR=C_FF8000 REQUEST=2da0809d-9143-4e42-8d80-80f76d4f0894`. Der native PC-Befehl lautet `core.command.load_filament` mit `mode=change`, `filament=PETG`, `color=#FF8000` und `request_id=<Auftrags-ID>`.

`PCMA_STATE` ergänzt dafür `change_id`, `confirmed_change_id` und `filament_phase`. Das Makro speichert `change_id` vor der ersten physischen Aktion. Eine Wiederholung derselben aktuellen Auftrags-ID startet keinen zweiten Wechsel. `AWAITING_INSERTION` wartet auf das Einführen, `LOADING` kennzeichnet den bereits begonnenen Ladeweg. Während `REQUESTED`, `UNLOADING`, `AWAITING_INSERTION`, `LOADING`, `AWAITING_CONFIRMATION` oder `RECOVERY_REQUIRED` nimmt der Adapter keinen weiteren Wechselauftrag an. `UNLOADED` verlangt einen neuen Auftrag oder eine ausdrücklich lokal geprüfte Rücksetzung vor Resume. Die generische Datei enthält eigene Lade-, Entlade- und Purge-Routinen mit konfigurierbaren Grenzen; bestehende Maschinenmakros werden nicht vorausgesetzt.

Erst nach lokaler Prüfung darf das Makro die angeforderte ID nach `confirmed_change_id` übernehmen, `loaded_material` und `loaded_color` aktualisieren und `filament_phase=CONFIRMED` setzen. Der Adapter spiegelt diese Werte unter `/pcma/*`; bestätigtes Material und RGB-Farbe werden außerdem im nativen Tool-Baum gemeldet. `UNKNOWN` entfernt dort eine bisherige Angabe. Der Auftrag gilt nur bei übereinstimmenden IDs, bestätigter Phase sowie passendem Material und passender Farbe als bestätigt. Nach einem Neustart darf kein Auftrag ohne Prüfung der physischen Lage als neu bestätigt erscheinen.

Erst danach schreibt PCMA die Material-/Spulenzuordnung über die bestehende Materials-API. Das Wechselprotokoll bleibt pro angemeldetem Benutzer im PC-Datenspeicher erhalten. Beim erneuten Öffnen des PCMA-Tabs wird es abgeglichen; ein Druckerbefehl wird dabei nie erneut gesendet. Fehlt die Bestätigung, bleibt die bisherige Zuordnung erhalten. Ein fehlgeschlagener API-Schreibvorgang lässt sich ohne weiteren Wechselbefehl erneut versuchen. Ein zwischenzeitlich von einer Person geänderter Materialeintrag wird nicht bewusst überschrieben.

`mode=load` mit Material und Farbe ruft `PCMA_LOAD_FILAMENT MATERIAL=... COLOR=...` auf. Ohne Metadaten bleibt die vorhandene Routine `LOAD_FILAMENT` nutzbar. Entladen verwendet `UNLOAD_FILAMENT`, solange kein PCMA-Zustandsvertrag aktiv ist; danach wird `PCMA_UNLOAD_FILAMENT` aufgerufen. Die PCMA-Makros müssen beim Entladen den bestätigten Material- und Farbzustand auf `UNKNOWN` setzen. Erst die lokale Bestätigung darf `loaded_material` und `loaded_color` aktualisieren. Kein dieser Aufrufe sendet Resume.

Die künftigen PCMA-Filamentmakros dürfen während des Entwicklungsübergangs bereits adressiert werden. Fehlt das Makro in der Objektliste, meldet der Adapter den Auftrag ausdrücklich als fehlgeschlagen, selbst wenn Klipper den unbekannten Befehl lediglich als Hinweis beantwortet. Es gibt keinen automatischen Wiederholungsversuch.

Homing verwendet `G28`. Mesh-Leveling verwendet `G28` und danach das vorhandene `G29`, ersatzweise `BED_MESH_CALIBRATE`. Mesh wird nur mit `bed_mesh` und `probe` angeboten. Relative XY-/Z-Bewegungen erfordern bekannte kartesische Achsgrenzen und referenzierte Zielachsen. Druck- und Bereitschaftszustand werden vor jeder manuellen Aktion frisch abgefragt. Während eines pausierten Drucks sind nur die Filamentaktionen erlaubt; die lokale Materialbestätigung bleibt erforderlich.

## Bettbestätigung und Quittierung

Nach Druckende, Abbruch oder Fehler bleibt `bed_clear_confirmed=false`. Das LCD bietet die Prüfung für die aktuelle beendete Ausführung an. Eine gültige lokale Bestätigung setzt `bed_clear_for` auf deren ID, erhöht die persistente Sequenz und publiziert die zusammengehörigen Werte.

Der Adapter gleicht `print_stats.filename`, die Ausführungs-ID und den Abschlusszustand mit seinem Journal ab. Er persistiert die gültige Bestätigung in PC und sendet anschließend diesen idempotenten Aufruf.

```gcode
PCMA_BED_ACK GENERATION=job-1 SEQUENCE=2
```

Das Makro nimmt die Quittierung nur an, wenn Generation und Sequenz noch exakt zur aktuellen lokalen Bestätigung gehören und kein Druck läuft. Es setzt ausschließlich die ACK-Felder. Der Folgedruck verlangt die passende lokale Bestätigung, die passende PC-Quittierung und eine atomare Reservierung im Journal. Eine Bestätigung wird bei einer angenommenen Reservierung genau einmal verbraucht.

Die feste Ender-3-Konfiguration muss ungeplante Starts über andere Moonraker-Clients und das vorhandene SD-Menü berücksichtigen. Die Makros können die Quelle eines beliebigen authentifizierten G-Code-Aufrufs allein anhand des Befehlsnamens nicht beweisen. Die lokale Bestätigung ist deshalb mit der tatsächlichen Menü- und Zugriffssteuerung zu testen.

## Wiederherstellung

Nach Kommunikationsverlust fragt der Adapter den vollständigen Zustand erneut ab. Ein verlorenes Start-ACK führt zu einem Abgleich mit der bereits gespeicherten Reservierung. Ein Start wird dabei nie erneut gesendet. Bei widersprüchlichen oder fehlenden Belegen bleibt die Ausführung gesperrt.

Für einen Neustart während Materialwechsel oder eines ungeklärten Starts ist die lokale Wiederherstellung noch auszuarbeiten. Sie muss den physischen Zustand prüfen, eine unterbrochene Ausführung explizit abschließen und eine neue Bettprüfung ermöglichen. Der Adapter besitzt dafür bisher keinen administrativen Freigabe-Bypass. Die Makros dürfen nach einem solchen Neustart keine Fortsetzung oder erneute Heizung selbst auslösen.

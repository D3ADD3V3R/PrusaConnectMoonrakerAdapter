# Schnittstelle zwischen Adapter und Klipper-Makros

Stand 18.11.2026. Dies ist der im Adapter und Simulator implementierte Vertrag für die folgenden Makros. Die Konfiguration für den realen Ender 3 muss diesen Vertrag noch umsetzen und im kontrollierten Hardwaretest bestehen.

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

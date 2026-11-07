# Zaptec · laddning och energi

Importera **[zaptec-charging.dynamic.json](zaptec-charging.dynamic.json)** i
Grafana via **Dashboards → New → Import → Upload dashboard JSON file**.
Den använder flikar och samma Canvas-upplägg som värmepumpsdashboarden.
Importformatet är verifierat i Grafana 13.3.

Systembilden använder en egen [SVG-illustration](assets/charging-scene.svg) med
villa, svart Zaptec-box och röd/svart Multivan. Bilden laddas via en offentlig
HTTPS-adress på GitHub, låst till en bestämd commit. Grafana behöver kunna ladda
bilder från `raw.githubusercontent.com`. Ingen egen bildserver eller extra
Grafana-plugin behövs. Effekt och energi är dynamiska Canvas-textfält ovanpå
bilden. Saknade fält visas som `—`; ett faktiskt rapporterat nollvärde visas som noll.

Kabel och ljusring är ett separat SVG-lager som färgas av Grafana:

- **Grönt / Laddar:** färskt laddläge är `3`, eller positiv laddareffekt när
  laddläge saknas.
- **Grått / Ingen laddning:** färskt laddläge anger annat än laddning, eller
  laddareffekten är noll när laddläge saknas.
- **Grått / Status saknas:** aktuellt laddläge och laddareffekt saknas, eller
  hämtning/skrapning har misslyckats. Inga gamla värden används som aktuell status.

Laddläget har företräde framför effekt när båda finns. Sense:s fastighetseffekt
styr aldrig laddindikeringen. Batterinivå och fysisk kabelanslutning antas inte.

Canvas tolkar base64-kodade `data:image/...`-adresser som relativa sökvägar och
lägger till sin `public/build/`-sökväg. Tidigare dashboardfiler med inbäddad SVG
kunde därför importeras men visade ingen bild. Importera den uppdaterade filen
över den befintliga dashboarden, eller byt bakgrundsadressen för elementet
`charging-scene` till HTTPS-adressen i `scripts/build-dashboard.py` (`SCENE_URL`).

Välj din Prometheus-datakälla i variabeln **Prometheus** och instansen
**homeassistant_zap**. Standardvärdet för datakällans UID är `grafanacloud-prom`.
Laddare och installation hämtas automatiskt från metric-labels; du behöver
inte fylla i UUID:er. Frågorna använder `job="zaptec"`.

För Grafana utan flikstödet finns **[zaptec-charging.json](zaptec-charging.json)**,
med samma paneler grupperade i rader. Importera endast den variant du vill använda.
Exporter-appen behöver inte uppdateras.

| Flik | Innehåll |
| --- | --- |
| Översikt | Systembild för fastighet, laddbox och bil; laddat idag/igår, senaste session och insamlingsstatus |
| Laddning | Effekt, aktiv sessionsenergi, laddläge, laddarens ström och spänning |
| Faser | Sense-ström och spänning per fas, laddström, tillgänglig ström och neutralström |
| Energi & sessioner | Fastighetens dygnsenergi, laddningens andel, arkiverad energi och senaste session |
| Datakvalitet | API-status, avläsningarnas ålder och energidatans täckning |

## Tolkning

- Energi idag/igår är Sense:s kalenderdygn enligt exporterns tidszon.
  Dashboardens tidszon är `Europe/Stockholm`. Delvis saknade timmar markeras
  i panelen för täckning; energisumman kan då vara ofullständig.
- Senaste sessionens energi kan avse laddning över midnatt. Dess varaktighet
  är ansluten tid inklusive pauser.
- Arkiverad energi är summan sedan exporterns konfigurerade historikstart,
  oberoende av Grafanas valda tidsintervall. Rättelser kan minska värdet.
- Exakt energi per månad finns ännu inte som metric. Använd sessionsarkivet
  för månadsberäkning; `rate()`/`increase()` på dessa gauges är olämpligt.
- Fas- och effektgrafer kräver färska observationer. I den första live-exporten
  saknades dessa, medan dygnsenergin fanns. Saknade värden visas som luckor/streck,
  aldrig som påhittade nollor. Ett rapporterat nollvärde visas däremot som noll.
- Fel i senaste API-hämtningen döljer aktuella mätvärden tills hämtningen fungerar
  igen. Status och tidsstämplar finns kvar för felsökning.
- Grafhistorik börjar när Prometheus började samla in. Tidigare exporterade
  JSON-filer ger ingen automatisk återfyllning av tidsserier.
- API-pollning varje minut för laddare/historik, var femte minut för Sense
  och skrapning/uppdatering varje minut fungerar
  tillsammans: dashboarden gör inga direkta Zaptec-anrop.

## Underhåll

JSON-filerna genereras från en gemensam definition utan extra Python-paket:

```sh
python3 scripts/build-dashboard.py
python3 scripts/check-dashboard.py
```

För att även testa PromQL med Prometheus `promtool`:

```sh
python3 scripts/check-dashboard.py --promtool-test /tmp/zaptec-dashboard.test.json
promtool test rules /tmp/zaptec-dashboard.test.json
```

Testfilen använder syntetiska laddar-ID:n och kontrollerar bland annat
noll, saknade mätvärden och fel i hämtning/skrapning. Ingen autentisering eller
privat laddhistorik finns i dashboardfilerna.

Vid ändringar i illustrationen: publicera SVG-filerna, uppdatera de fasta revisionerna
i `SCENE_URL` och `FLOW_URL` och generera om dashboardfilerna. Då hålls illustration och
mätvärdenas positioner tillsammans även när nästa bildversion publiceras.

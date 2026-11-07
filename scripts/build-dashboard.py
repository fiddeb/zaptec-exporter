"""Build Swedish Grafana dashboards from the exporter's public metric contract."""
from copy import deepcopy
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
UID = "zaptec-charging"
# Canvas resolves data: URLs as relative build assets. Use its supported HTTPS image source.
SCENE_URL = "https://raw.githubusercontent.com/fiddeb/zaptec-exporter/b7f9b78f6d5851863d3a6d4e3584f2571afd3046/dashboards/assets/charging-scene.svg"
FLOW_URL = "https://raw.githubusercontent.com/fiddeb/zaptec-exporter/b7f9b78f6d5851863d3a6d4e3584f2571afd3046/dashboards/assets/charging-flow.svg"
DS = {"type": "prometheus", "uid": "${datasource}"}
WHITE, MUTED, TEAL, BLUE, AMBER = "#F4F5F8", "#A8B3C7", "#73D9B1", "#80B6FF", "#EAC075"
PHASE_COLORS = {"Fas 1": AMBER, "Fas 2": TEAL, "Fas 3": BLUE}
MODE_MAP = {1: ("Ingen bil ansluten", "gray"), 2: ("Väntar på laddning", "orange"),
            3: ("Laddar", "green"), 5: ("Laddning avslutad", "blue")}
CHARGING_COLORS = {-1: ("Status saknas", "#66747D"), 0: ("Ingen laddning", "#84939C"),
                   1: ("Laddar", "#73D9B1")}


def selector(metric, scope=None, **labels):
    fields = ['job="zaptec"', 'instance=~"${instance:regex}"']
    if scope:
        key, variable = {"charger": ("charger_id", "charger"), "sense": ("installation_id", "installation")}[scope]
        fields.append(f'{key}=~"${{{variable}:regex}}"')
    fields.extend(f'{key}="{value}"' for key, value in labels.items())
    return metric + "{" + ",".join(fields) + "}"


def health(source):
    return selector({"charger": "zaptec_exporter_up", "history": "zaptec_exporter_history_up", "sense": "zaptec_exporter_sense_up"}[source])


def current(metric, source, scope=None, scale=None, **labels):
    expression = selector(metric, scope, **labels)
    if scale:
        expression += f" / {scale}"
    return f"({expression}) and on (job, instance) ({health(source)} == 1) and on (job, instance) ({selector('up')} == 1)"


def charging_state():
    """Use fresh charger status, then charger power; -1 explicitly means unknown."""
    mode = current("zaptec_charger_operation_mode", "charger", "charger")
    power = current("zaptec_charge_power_watts", "charger", "charger")
    return f"max(({mode}) == bool 3) or max(({power}) > bool 0) or vector(-1)"


def mapping(values):
    return [{"type": "value", "options": {str(key): {"text": text, "color": color, "index": i}
             for i, (key, (text, color)) in enumerate(values.items())}}]


def fields(unit="short", decimals=1, no_value="Ingen färsk data"):
    return {"defaults": {"unit": unit, "decimals": decimals, "noValue": no_value,
                         "color": {"mode": "palette-classic"},
                         "thresholds": {"mode": "absolute", "steps": [{"color": "green", "value": None}]}},
            "overrides": []}


def override(config, name, **properties):
    config["overrides"].append({"matcher": {"id": "byName", "options": name},
        "properties": [{"id": key, "value": value} for key, value in properties.items()]})


def query(expression, legend="", instant=False, ref="A"):
    return {"datasource": DS, "editorMode": "code", "expr": expression, "instant": instant,
            "range": not instant, "legendFormat": legend, "refId": ref}


class Builder:
    def __init__(self):
        self.panels = []
        self.tabs = []

    def tab(self, title):
        self.tabs.append({"title": title, "panels": []})

    def panel(self, title, kind, queries, x, y, w, h, config=None, options=None, description=""):
        panel = {"id": len(self.panels) + 1, "title": title, "type": kind, "description": description,
                 "datasource": DS, "gridPos": {"x": x, "y": y, "w": w, "h": h},
                 "targets": queries, "fieldConfig": config or fields(), "options": options or {},
                 "pluginVersion": "13.3.0", "links": []}
        self.panels.append(panel)
        self.tabs[-1]["panels"].append(panel)
        return panel

    def stat(self, title, expression, x, y, w=6, h=4, unit="short", decimals=2,
             mappings=None, thresholds=None, description="", no_value="Ingen färsk data"):
        config = fields(unit, decimals, no_value)
        if mappings:
            config["defaults"]["mappings"] = mapping(mappings)
        if thresholds:
            config["defaults"]["thresholds"]["steps"] = thresholds
            config["defaults"]["color"] = {"mode": "thresholds"}
        return self.panel(title, "stat", [query(expression, instant=True)], x,y,w,h, config,
            {"colorMode": "value" if mappings or thresholds else "none", "graphMode": "none",
             "justifyMode": "auto", "orientation": "auto", "textMode": "auto", "wideLayout": True,
             "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}, description)

    def chart(self, title, expressions, x, y, w=12, h=8, unit="short", colors=None, description=""):
        config = fields(unit, 2)
        config["defaults"]["custom"] = {"drawStyle": "line", "lineInterpolation": "stepAfter",
            "lineWidth": 2, "fillOpacity": 6, "gradientMode": "none", "showPoints": "never",
            "spanNulls": False, "insertNulls": False, "axisPlacement": "auto", "axisLabel": "",
            "scaleDistribution": {"type": "linear"}, "stacking": {"mode": "none", "group": "A"},
            "hideFrom": {"legend": False, "tooltip": False, "viz": False}, "thresholdsStyle": {"mode": "off"}}
        for name, color in (colors or {}).items():
            override(config, name, color={"mode": "fixed", "fixedColor": color})
        return self.panel(title,"timeseries",[query(expr,name,ref=chr(65+i)) for i,(name,expr) in enumerate(expressions)],
            x,y,w,h,config,{"legend":{"showLegend":True,"displayMode":"table","placement":"bottom","calcs":["lastNotNull","min","max"]},
                "tooltip":{"mode":"multi","sort":"none","hideZeros":False}},description)

    def note(self, title, text, x, y, w=24, h=5):
        return self.panel(title,"text",[],x,y,w,h,fields(),{"mode":"markdown","content":text})


def canvas_element(name, kind, x,y,w,h, text="", field=None, size=18, color=WHITE, background="transparent", border="transparent", radius=0):
    return {"name":name,"type":kind,"placement":{"left":x,"top":y,"width":w,"height":h},
        "constraint":{"horizontal":"left","vertical":"top"},
        "background":{"color":{"fixed":background}}, "border":{"color":{"fixed":border},"width":1 if border!="transparent" else 0,"radius":radius},
        "config":{"align":"center","valign":"middle","size":size,"color":{"fixed":color},
            "text":{"mode":"field" if field else "fixed","fixed":text,**({"field":field} if field else {})}}}


def add_canvas(b):
    expressions = [
        ("Installation kW",current("zaptec_sense_power_watts","sense","sense",1000)),
        ("Charge kW",current("zaptec_charge_power_watts","charger","charger",1000)),
        ("Today kWh",current("zaptec_sense_charge_energy_kwh","sense","sense",period="today")),
        ("Last session kWh",current("zaptec_charge_last_session_energy_kwh","history","charger")),
        ("Limit kW",current("zaptec_sense_power_limit_watts","sense","sense",1000)),
        ("Charging state", charging_state()),
    ]
    elements = []
    def add(*args,**kwargs): elements.append(canvas_element(*args,**kwargs))
    # SVG supplies the illustration; Grafana renders real values in native fields.
    # A full-size image element keeps zoom-to-content from cropping to the value fields.
    add("charging-scene", "rectangle", 0, 0, 1200, 650)
    elements[-1]["background"].update(image={"fixed": SCENE_URL}, size="fill")
    # The SVG inherits its fill from Canvas; missing/idle observations stay neutral.
    elements.append({"name": "charging-flow", "type": "icon",
        "placement": {"left": 0, "top": 0, "width": 1200, "height": 650},
        "constraint": {"horizontal": "left", "vertical": "top"},
        "background": {"color": {"fixed": "transparent"}},
        "config": {"path": {"mode": "fixed", "fixed": FLOW_URL},
                   "fill": {"field": "Charging state", "fixed": "#66747D"}}})
    for name, field, x, y, w, h, size in (
        ("house-power", "Installation kW", 61, 520, 310, 58, 42),
        ("charger-power", "Charge kW", 442, 520, 310, 58, 42),
        ("car-today", "Today kWh", 823, 520, 310, 58, 42),
        ("limit-value", "Limit kW", 76, 393, 210, 35, 23),
        ("session-value", "Last session kWh", 882, 393, 260, 35, 23),
    ):
        # The metric-value element overrides noValue with 'Field not found'.
        # Text elements use the same formatted field with an actual missing-field fallback.
        add(name, "text", x, y, w, h, text="—", field=field, size=size)
    add("charging-status", "text", 490, 132, 170, 22, text="Status saknas", field="Charging state", size=13)
    elements[-1]["config"]["color"] = {"field": "Charging state", "fixed": "#66747D"}
    config=fields("suffix:kW",2,"—")
    config["defaults"]["color"]={"mode":"fixed","fixedColor":WHITE}
    for name in ("Today kWh","Last session kWh"):override(config,name,unit="suffix:kWh")
    override(config, "Charging state", unit="short", decimals=0,
             color={"mode": "thresholds"}, mappings=mapping(CHARGING_COLORS))
    b.panel("Laddningen · fastighet, laddbox och bil","canvas",[query(expr,name,True,chr(65+i)) for i,(name,expr) in enumerate(expressions)],
        0,0,15,16,config,{"inlineEditing":False,"panZoom":False,"zoomToContent":True,"showAdvancedTypes":False,
            "tooltip":{"disableForOneClick":False},"root":{"type":"frame","name":"Zaptec charging",
             "placement":{"left":0,"top":0,"width":1200,"height":650},"constraint":{"horizontal":"left","vertical":"top"},
             "background":{"color":{"fixed":"transparent"}},"border":{"color":{"fixed":"transparent"}},"elements":elements,"connections":[]}},
        "Ögonblicksvärden och dygnsenergi har olika tidsbetydelse. Ingen färsk avläsning visas som —, aldrig som noll. Bilen är en illustration; batterinivå hämtas inte.")


def build():
    b=Builder()
    car=lambda metric,**kw:current(metric,"charger","charger",**kw)
    sense=lambda metric,**kw:current(metric,"sense","sense",**kw)
    archive=lambda metric:current(metric,"history","charger")
    age=f"time() - {selector('zaptec_sense_reading_timestamp_seconds','sense')}"
    ok={0:("Fel / inaktuell","red"),1:("OK","green")}
    b.tab("Översikt")
    add_canvas(b)
    b.stat("Laddat idag · Sense",sense("zaptec_sense_charge_energy_kwh",period="today"),15,0,9,4,"suffix:kWh",description="Löpande summa i svensk kalenderdag, till senaste hämtningen.")
    b.stat("Laddat igår · Sense",sense("zaptec_sense_charge_energy_kwh",period="yesterday"),15,4,9,4,"suffix:kWh")
    b.stat("Senaste sessionen · laddbox",archive("zaptec_charge_last_session_energy_kwh"),15,8,9,4,"suffix:kWh",description="Energin i senast arkiverade sessionen. Är inte samma mått som hela gårdagens laddning.")
    b.stat("Sense · avläsningens ålder",age,15,12,9,4,"s",0,thresholds=[{"color":"green","value":None},{"color":"orange","value":300},{"color":"red","value":600}],description="API:t kan svara OK trots att själva avläsningen är äldre. Standardgränsen för fas- och effektvärden är 10 minuter.")
    for i,(title,expr) in enumerate((("Alloy → exporter",selector("up")),("Laddarens API",health("charger")),("Sessionsarkiv",health("history")),("Sense API",health("sense")))):
        b.stat(title,expr,6*i,16,6,3,decimals=0,mappings=ok,no_value="Ej tillgänglig")
    b.chart("Laddenergi idag · förlopp",[("Laddning idag",sense("zaptec_sense_charge_energy_kwh",period="today"))],0,19,24,8,"suffix:kWh",{"Laddning idag":TEAL},"Gauge som nollställs vid lokal midnatt. Ingen rate/increase används. Historik börjar när Alloy började samla in.")

    b.tab("Laddning")
    b.stat("Laddareffekt",car("zaptec_charge_power_watts",scale=1000),0,0,6,4,"suffix:kW")
    b.stat("Aktiv sessionsenergi",car("zaptec_charge_session_energy_kwh"),6,0,6,4,"suffix:kWh")
    b.stat("Tilldelad laddström",car("zaptec_charge_current_limit_amperes"),12,0,6,4,"amp")
    b.stat("Laddläge",car("zaptec_charger_operation_mode"),18,0,6,4,decimals=0,mappings=MODE_MAP)
    b.chart("Effekt · laddare och fastighet",[("Laddare",car("zaptec_charge_power_watts",scale=1000)),("Fastighet",sense("zaptec_sense_power_watts",scale=1000)),("Effektgräns",sense("zaptec_sense_power_limit_watts",scale=1000))],0,4,24,8,"suffix:kW",{"Laddare":TEAL,"Fastighet":BLUE,"Effektgräns":MUTED})
    b.chart("Laddarens utström",[("Fas {{phase}}",car("zaptec_charge_current_amperes"))],0,12,12,8,"amp",PHASE_COLORS)
    b.chart("Laddarens utspänning",[("Fas {{phase}}",car("zaptec_charge_voltage_volts"))],12,12,12,8,"volt",PHASE_COLORS)
    config=fields(no_value="Ingen aktuell observation")
    config["defaults"].update(mappings=mapping(MODE_MAP),custom={"spanNulls":False,"lineWidth":0,"fillOpacity":85})
    b.panel("Laddläge över tid","state-timeline",[query(car("zaptec_charger_operation_mode"),"Laddläge")],0,20,24,5,config,
        {"mergeValues":True,"showValue":"auto","rowHeight":0.8,"legend":{"showLegend":False},"tooltip":{"mode":"single"}},"Saknade eller gamla observationer visas som luckor; avsaknad av laddning antas inte betyda att bilen är frånkopplad.")

    b.tab("Faser")
    for title,metric,x,y,unit in (("Fastighetens fasströmmar","zaptec_sense_current_amperes",0,0,"amp"),("Fastighetens fasspänningar","zaptec_sense_voltage_volts",12,0,"volt"),("Laddström enligt Sense","zaptec_sense_charge_current_amperes",0,8,"amp"),("Tillgänglig laddström per fas","zaptec_sense_available_current_amperes",12,8,"amp")):
        b.chart(title,[("Fas {{phase}}",sense(metric))],x,y,12,8,unit,PHASE_COLORS)
    b.chart("Neutralström",[("Neutral",sense("zaptec_sense_neutral_current_amperes"))],0,16,12,7,"amp",{"Neutral":AMBER})
    b.note("Så ska faserna läsas","Sense visar fastighetens ström och spänning. **Laddström** och **tillgänglig ström** är separata värden.\n\n- Historik byggs av Prometheus från insamlingsstart.\n- Saknade fasvärden är inte noll.\n- Färg: **fas 1 gul**, **fas 2 grön**, **fas 3 blå**.\n- Fasström × spänning är inte automatiskt aktiv effekt; dashboarden hittar inte på kWh per fas.",12,16,12,7)

    b.tab("Energi & sessioner")
    b.stat("Fastighet · idag",sense("zaptec_sense_total_energy_kwh",period="today"),0,0,6,4,"suffix:kWh")
    b.stat("Fastighet · igår",sense("zaptec_sense_total_energy_kwh",period="yesterday"),6,0,6,4,"suffix:kWh")
    share=f'100 * ({sense("zaptec_sense_charge_energy_kwh",period="today")}) / ({sense("zaptec_sense_total_energy_kwh",period="today")} > 0)'
    b.stat("Bilens andel idag · Sense",share,12,0,6,4,"percent",1,description="Laddenergi / rapporterad totalenergi. Visas inte när totalenergin är noll eller saknas.")
    b.stat("Arkiverad laddenergi · sedan start",archive("zaptec_charge_completed_energy_kwh"),18,0,6,4,"suffix:kWh",description="Summa för det konfigurerade arkivintervallet. Kan minska vid rättelser. Inte förbrukning under vald Grafana-period.")
    b.chart("Dagens energi · löpande dygnssummor",[("Fastighet",sense("zaptec_sense_total_energy_kwh",period="today")),("Laddning",sense("zaptec_sense_charge_energy_kwh",period="today"))],0,4,24,8,"suffix:kWh",{"Fastighet":BLUE,"Laddning":TEAL},"Summorna startar om vid lokal midnatt. De adderas inte över skrapningar.")
    b.stat("Arkiverade sessioner",archive("zaptec_charge_completed_sessions"),0,12,8,4,decimals=0)
    b.stat("Senaste session · ansluten tid",archive("zaptec_charge_last_session_duration_seconds"),8,12,8,4,"s",0,description="Tid från sessionsstart till slut inklusive pauser. Är inte aktiv laddtid.")
    b.stat("Senaste session · avslutad",f'({archive("zaptec_charge_last_session_end_timestamp_seconds")}) * 1000',16,12,8,4,"dateTimeAsLocal",0)
    b.note("Dygn, session och månad är olika mått","**Sense idag/igår** är energi per kalenderdygn. **Senaste sessionen** är laddboxens avslutade session och kan sträcka sig över midnatt. **Arkiverad laddenergi** är en korrigerbar summa sedan det inställda startdatumet.\n\nExakta **månads-kWh finns ännu inte som metric** i den här exporter-versionen. De kan beräknas ur sessionsarkivet separat; `increase()` eller `rate()` på dessa gauges ger inte en tillförlitlig månadssumma. Tidigare hämtade JSON-filer importeras inte automatiskt till Prometheus.",0,16,24,6)

    b.tab("Datakvalitet")
    b.chart("Hämtningar · ålder sedan senaste lyckade anrop",[(title,f'time() - ({selector(metric)} > 0)') for title,metric in (("Laddare","zaptec_exporter_last_success_timestamp_seconds"),("Arkiv","zaptec_exporter_history_last_success_timestamp_seconds"),("Sense","zaptec_exporter_sense_last_success_timestamp_seconds"))],0,0,12,8,"s",{"Laddare":TEAL,"Arkiv":AMBER,"Sense":BLUE})
    b.chart("Sense · mätvärdets ålder",[("Fasavläsning",age)],12,0,12,8,"s",{"Fasavläsning":AMBER},"Åldern gäller själva avläsningen. En lyckad hämtning föryngrar inte gammal mätdata.")
    config=fields(no_value="Ej tillgänglig");config["defaults"].update(mappings=mapping(ok),custom={"spanNulls":False,"lineWidth":0,"fillOpacity":85})
    b.panel("Insamlingsstatus över tid","state-timeline",[query(expr,title,ref=chr(65+i)) for i,(title,expr) in enumerate((("Exporter",selector("up")),("Laddare",health("charger")),("Arkiv",health("history")),("Sense",health("sense"))))],0,8,24,6,config,{"mergeValues":True,"showValue":"auto","rowHeight":0.8,"legend":{"showLegend":False},"tooltip":{"mode":"single"}})
    coverage=b.stat("Täckning · energins timintervall",selector("zaptec_sense_energy_complete","sense"),0,14,24,4,decimals=0,mappings={0:("Saknade timmar","red"),1:("Komplett","green")},description="Idag betyder komplett fram till senaste hämtningen, inte att hela kalenderdagen är avslutad.")
    coverage["targets"][0]["legendFormat"]="{{kind}} · {{period}}"
    for kind,label in (("total","Fastighet"),("charge","Laddning")):
        for period,day in (("today","idag"),("yesterday","igår")):override(coverage["fieldConfig"],f"{kind} · {period}",displayName=f"{label} · {day}")
    b.note("Ingen data är inte noll","- **Alloy → exporter** visar om skrapningen fungerar; de tre API-statusarna visar hämtningarna var för sig.\n- API-pollning är normalt **5 minuter** och Alloy-skrapning **1 minut**. Tätare skrapning orsakar inga extra Zaptec-anrop.\n- Din startbild kan visa dygnsenergi trots att effekt- och fasvärden saknas: de har olika tidsstämplar och färskhetsregler.\n- Grafer visar stegvisa avläsningar utan påhittade mellanvärden.\n- Kostnad i kronor och bilens batteriprocent visas inte: sådant underlag finns inte i dessa metrics.",0,18,24,6)
    return b


def variables_v1():
    variables=[{"type":"datasource","name":"datasource","label":"Prometheus","query":"prometheus","current":{"text":"Grafana Cloud Prometheus","value":"grafanacloud-prom"},"refresh":1,"hide":0}]
    definitions={"instance":'label_values(up{job="zaptec"}, instance)',
        "charger":'label_values({job="zaptec",instance=~"${instance:regex}",charger_id!=""}, charger_id)',
        "installation":'label_values(zaptec_sense_reading_timestamp_seconds{job="zaptec",instance=~"${instance:regex}"}, installation_id)'}
    for name,definition in definitions.items():
        variables.append({"type":"query","name":name,"label":{"instance":"Instans","charger":"Laddare","installation":"Installation"}[name],
            "datasource":DS,"definition":definition,"query":{"query":definition,"refId":name},"refresh":1,"sort":1,"regex":"","multi":False,"includeAll":True,"allValue":".*","options":[],
            "current":{"text":"homeassistant_zap","value":"homeassistant_zap"} if name=="instance" else {"text":"All","value":"$__all"}})
    return variables


def classic(builder):
    panels=[];offset=0
    for i,tab in enumerate(builder.tabs):
        panels.append({"id":1000+i,"type":"row","title":tab["title"],"collapsed":False,"panels":[],"gridPos":{"x":0,"y":offset,"w":24,"h":1}})
        height=max(p["gridPos"]["y"]+p["gridPos"]["h"] for p in tab["panels"])
        for source in tab["panels"]:
            panel=deepcopy(source);panel["gridPos"]["y"]+=offset+1;panels.append(panel)
        offset+=height+1
    return {"id":None,"uid":UID+"-classic","title":"Zaptec · laddning och energi (klassisk)","description":"Laddning, Sense, dygnsenergi och datakvalitet. Ingen data är inte noll.",
        "schemaVersion":42,"version":1,"editable":True,"tags":["zaptec","charging","sense"],"timezone":"Europe/Stockholm","graphTooltip":1,
        "refresh":"1m","time":{"from":"now-24h","to":"now"},"timepicker":{},"links":[],"annotations":{"list":[]},
        "templating":{"list":variables_v1()},"panels":panels}


def dynamic(builder):
    elements={}
    for panel in builder.panels:
        queries=[{"kind":"PanelQuery","spec":{"refId":q["refId"],"hidden":False,"query":{
            "kind":"DataQuery","group":"prometheus","version":"v0","datasource":{"name":"${datasource}"},
            "spec":{key:value for key,value in q.items() if key not in ("datasource","refId")}}}} for q in panel["targets"]]
        elements[f'panel-{panel["id"]}']={"kind":"Panel","spec":{"id":panel["id"],"title":panel["title"],"description":panel["description"],"links":[],
            "data":{"kind":"QueryGroup","spec":{"queries":queries,"transformations":[],"queryOptions":{}}},
            "vizConfig":{"kind":"VizConfig","group":panel["type"],"version":"13.3.0","spec":{"fieldConfig":panel["fieldConfig"],"options":panel["options"]}}}}
    tabs=[]
    for tab in builder.tabs:
        items=[{"kind":"GridLayoutItem","spec":{"element":{"kind":"ElementReference","name":f'panel-{p["id"]}'},
            "x":p["gridPos"]["x"],"y":p["gridPos"]["y"],"width":p["gridPos"]["w"],"height":p["gridPos"]["h"]}} for p in tab["panels"]]
        tabs.append({"kind":"TabsLayoutTab","spec":{"title":tab["title"],"layout":{"kind":"GridLayout","spec":{"items":items}}}})
    variables=[]
    for item in variables_v1():
        spec={"name":item["name"],"label":item["label"],"current":item["current"],"hide":"dontHide","includeAll":item.get("includeAll",False),
              "multi":False,"allowCustomValue":True,"options":[],"refresh":"onDashboardLoad","skipUrlSync":False,"regex":""}
        if item["type"]=="datasource":spec["pluginId"]="prometheus";kind="DatasourceVariable"
        else:
            kind="QueryVariable";spec.update(definition=item["definition"],allValue=item["allValue"],sort="alphabeticalAsc",regexApplyTo="value",
                query={"kind":"DataQuery","group":"prometheus","version":"v0","datasource":{"name":"${datasource}"},"spec":item["query"]})
        variables.append({"kind":kind,"spec":spec})
    return {"apiVersion":"dashboard.grafana.app/v2","kind":"Dashboard","metadata":{"name":UID},"spec":{
        "title":"Zaptec · laddning och energi","description":"Översikt med systembild, laddning, faser, dygnsenergi och datakvalitet.","editable":True,"annotations":[],
        "elements":elements,"layout":{"kind":"TabsLayout","spec":{"tabs":tabs}},"variables":variables,"links":[],"tags":["zaptec","charging","sense"],
        "cursorSync":"Crosshair","liveNow":False,"preload":False,"preferences":{},
        "timeSettings":{"from":"now-24h","to":"now","timezone":"Europe/Stockholm","autoRefresh":"1m","autoRefreshIntervals":["1m","5m","15m","30m","1h"],"hideTimepicker":False,"fiscalYearStartMonth":0}}}


def main():
    builder=build();destination=ROOT/"dashboards";destination.mkdir(exist_ok=True)
    for name,document in (("zaptec-charging.dynamic.json",dynamic(builder)),("zaptec-charging.json",classic(builder))):
        (destination/name).write_text(json.dumps(document,ensure_ascii=False,indent=2)+"\n")
    print(f"Generated {len(builder.panels)} panels across {len(builder.tabs)} tabs in {destination}")


if __name__=="__main__":main()

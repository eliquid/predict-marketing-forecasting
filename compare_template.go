package main

// The comparison page. Same rules as the single-model report: one file, no
// network, no build step.

import "html/template"

// Dark and monospaced on purpose. This is a dense numeric page that people read
// next to a terminal, and a tabular font keeps columns of money aligned in a way
// a proportional one does not. The palette is fixed rather than following the
// system theme: a report gets screenshotted and pasted into messages, and it
// should look the same to everyone who sees it.
//
// Every (entity, metric) pane is rendered into the page and all but one hidden.
// The dropdowns swap them with a few lines of plain JavaScript -- not htmx,
// which needs a server it will not have from a file:// page, and not a chart
// library, which would be a download the page cannot make either.
//
// Without JavaScript nothing is hidden, so the page degrades into every chart
// stacked vertically: longer, but complete and readable.
var compareTmpl = template.Must(template.New("compare").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.SeriesID}} — model comparison</title>
<style>
  :root {
    --bg:#11141c; --panel:#171b26; --panel2:#1c2130;
    --fg:#e6e9ef; --muted:#8b93a7; --faint:#5d6578;
    --line:#262c3b; --grid:#20263349;
    --hist:#8b93a7; --accent:#e8a33d;
    --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  }
  * { box-sizing:border-box; }
  body { margin:0; padding:28px 20px 60px; background:var(--bg); color:var(--fg);
         font:13.5px/1.6 var(--mono); -webkit-font-smoothing:antialiased; }
  main { max-width:1180px; margin:0 auto; }

  .topline { display:flex; align-items:center; gap:14px; flex-wrap:wrap;
             margin-bottom:18px; }
  .topline h1 { font-size:13px; font-weight:700; letter-spacing:.14em;
                text-transform:uppercase; margin:0; }
  .dot { color:var(--faint); }
  select { font:inherit; font-size:12px; letter-spacing:.1em; text-transform:uppercase;
           padding:6px 28px 6px 10px; color:var(--fg); background:var(--panel2);
           border:1px solid var(--accent); border-radius:4px; cursor:pointer;
           appearance:none;
           background-image:linear-gradient(45deg,transparent 50%,var(--accent) 50%),
                            linear-gradient(135deg,var(--accent) 50%,transparent 50%);
           background-position:calc(100% - 15px) 52%, calc(100% - 10px) 52%;
           background-size:5px 5px, 5px 5px; background-repeat:no-repeat; }
  select:focus { outline:2px solid var(--accent); outline-offset:1px; }

  .lede { color:var(--fg); margin:0 0 20px; max-width:none; }
  .lede b { color:var(--accent); font-weight:700; }

  .cards { display:flex; gap:14px; flex-wrap:wrap; margin-bottom:18px; }
  .card { border:1px solid var(--line); border-radius:5px; padding:11px 16px 12px;
          background:var(--panel); min-width:210px; }
  .card.lead { border-color:var(--accent); }
  .card .cap { font-size:10.5px; letter-spacing:.13em; text-transform:uppercase;
               color:var(--muted); margin-bottom:7px; }
  .card .big { font-size:25px; font-weight:700; letter-spacing:-.01em; line-height:1.1; }
  .card .sub { font-size:11px; color:var(--faint); margin-top:5px; }

  .callout { border-left:3px solid var(--accent); background:var(--panel);
             padding:12px 16px; margin:0 0 20px; }
  .callout .cap { font-size:10.5px; letter-spacing:.13em; text-transform:uppercase;
                  color:var(--muted); margin-bottom:6px; }

  .plot { background:var(--panel); border:1px solid var(--line); border-radius:5px;
          padding:16px 16px 10px; margin-bottom:16px; }
  .plot .cap { font-size:10.5px; letter-spacing:.13em; text-transform:uppercase;
               color:var(--muted); margin-bottom:6px; }
  .chart { width:100%; height:auto; display:block; }
  .grid  { stroke:var(--line); stroke-width:1; }
  .hist  { fill:none; stroke:var(--hist); stroke-width:2; stroke-linejoin:round; }
  .split { stroke:var(--faint); stroke-width:1; stroke-dasharray:3 4; }
  .ylab  { fill:var(--muted); font:11.5px var(--mono); }
  .xlab  { fill:var(--faint); font:11px var(--mono); text-anchor:middle; }
  .inline-label { font:12.5px var(--mono); text-anchor:end; }
  .hist-label { fill:var(--muted); text-anchor:middle; }
  .split-label { fill:var(--muted); font:11.5px var(--mono); }

  .legend { display:flex; gap:10px; flex-wrap:wrap; margin:4px 0 22px; }
  .legend span { display:inline-flex; align-items:center; gap:8px; font-size:11.5px;
                 color:var(--muted); background:var(--panel); padding:6px 11px;
                 border:1px solid var(--line); border-radius:4px; }
  .swatch { width:20px; height:0; border-top-width:3px; border-top-style:solid;
            display:inline-block; }

  table { border-collapse:collapse; width:100%; font-variant-numeric:tabular-nums; }
  th, td { text-align:right; padding:7px 12px; border-bottom:1px solid var(--line); }
  th:first-child, td:first-child { text-align:left; color:var(--muted); }
  th { color:var(--muted); font-weight:600; font-size:10.5px; letter-spacing:.11em;
       text-transform:uppercase; }

  footer { margin-top:34px; padding-top:18px; border-top:1px solid var(--line);
           color:var(--faint); font-size:11.5px; }
  footer td, footer th { text-align:left; }
  footer .swatch { vertical-align:middle; margin-right:7px; }
</style>
</head>
<body>
<main>
  <div class="topline">
    <h1>{{.SeriesID}}</h1>
    <span class="dot">·</span>
    <select id="pick-entity" aria-label="campaign">
      {{range .Entities}}<option value="{{.}}">{{.}}</option>{{end}}
    </select>
    <span class="dot">·</span>
    <select id="pick-metric" aria-label="metric">
      {{range .Metrics}}<option value="{{.}}">{{.}}</option>{{end}}
    </select>
  </div>

  <p class="lede">
    {{len .Models}} models forecast the same {{.Horizon}} days from the same
    history. The last day of real data is <b>{{.AsOf}}</b>; everything to the
    right of the divider is predicted, not observed. Where the lines separate,
    the models disagree — that gap is the honest measure of how sure any of this
    is.
  </p>

  {{if .Excluded}}
  <div class="callout">
    <div class="cap">Not forecast</div>
    <strong>{{range $i, $e := .Excluded}}{{if $i}}, {{end}}{{$e}}{{end}}</strong>
    — nothing moved during this period (paused for the whole of it, or no
    activity recorded). Their rows are still stored; there was nothing to predict.
  </div>
  {{end}}

  <div class="legend">
    <span><i class="swatch" style="border-top-color:var(--hist)"></i>actual</span>
    {{range .Models}}
    <span><i class="swatch" style="border-top-color:{{.Colour}};border-top-style:{{.Stroke}}"></i>{{.Name}}{{if .IsFineTuned}} · fine-tuned{{end}}</span>
    {{end}}
  </div>

  {{range .Panes}}
  <section class="pane" data-entity="{{.Entity}}" data-metric="{{.Metric}}">
    <div class="cards">
      {{range .Totals}}
      <div class="card{{if .Lead}} lead{{end}}">
        <div class="cap">{{.Caption}}</div>
        <div class="big">{{.Value}}</div>
        <div class="sub">{{.Note}}</div>
      </div>
      {{end}}
    </div>
    <div class="plot">
      <div class="cap">{{.Entity}} · {{.Metric}}</div>
      {{.Chart}}
    </div>
    <table>
      <thead><tr>{{range .Header}}<th>{{.}}</th>{{end}}</tr></thead>
      <tbody>
        {{range .Rows}}
        <tr><td>{{.Day}}</td>{{range .Values}}<td>{{.}}</td>{{end}}</tr>
        {{end}}
      </tbody>
    </table>
  </section>
  {{end}}

  <footer>
    Generated {{.Generated}}. Every line above is a model's median. What produced them:
    <table>
      <thead><tr><th>model</th><th>weights</th><th>revision</th><th>sha256</th><th>notes</th></tr></thead>
      <tbody>
      {{range .Models}}
        <tr>
          <td><i class="swatch" style="border-top-color:{{.Colour}};border-top-style:{{.Stroke}}"></i>{{.Name}}</td>
          <td>{{.Repo}}</td><td>{{.Revision}}</td><td>{{.Weights}}</td>
          <td>{{if .IsFineTuned}}trained on your data through {{.TrainedThrough}}{{else}}pretrained, unmodified{{end}}</td>
        </tr>
      {{end}}
      </tbody>
    </table>
    A fine-tuned model has already seen the days it was trained on, so judge it
    on days after that cutoff.
  </footer>
</main>
<script>{{.HTMX}}</script>
<script>
(function () {
  var panes  = Array.prototype.slice.call(document.querySelectorAll('.pane'));
  var entity = document.getElementById('pick-entity');
  var metric = document.getElementById('pick-metric');
  if (!panes.length || !entity || !metric) return;

  // Which metrics exist for the chosen campaign. A campaign that was not
  // forecast for some metric must not leave the page blank with the dropdown
  // still offering it.
  function metricsFor(e) {
    var seen = {}, out = [];
    panes.forEach(function (p) {
      if (p.dataset.entity !== e) return;
      if (!seen[p.dataset.metric]) { seen[p.dataset.metric] = true; out.push(p.dataset.metric); }
    });
    return out;
  }

  function show() {
    var e = entity.value, m = metric.value, shown = false;
    panes.forEach(function (p) {
      var on = p.dataset.entity === e && p.dataset.metric === m;
      p.style.display = on ? '' : 'none';
      if (on) shown = true;
    });
    if (!shown && metric.options.length) {   // fall back to the first that exists
      metric.selectedIndex = 0;
      show();
    }
  }

  function refreshMetrics() {
    var wanted = metric.value;
    var list = metricsFor(entity.value);
    metric.innerHTML = '';
    list.forEach(function (m) {
      var o = document.createElement('option');
      o.value = m; o.textContent = m;
      metric.appendChild(o);
    });
    if (list.indexOf(wanted) >= 0) metric.value = wanted;
    show();
  }

  entity.addEventListener('change', refreshMetrics);
  metric.addEventListener('change', show);
  refreshMetrics();
})();
</script>
</body>
</html>
`))

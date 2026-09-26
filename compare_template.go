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
  .scroller { overflow-x:auto; overflow-y:hidden; position:relative;
              scrollbar-color:var(--faint) var(--panel2); }
  .scroller::-webkit-scrollbar { height:12px; }
  .scroller::-webkit-scrollbar-track { background:var(--panel2); border-radius:6px; }
  .scroller::-webkit-scrollbar-thumb { background:var(--faint); border-radius:6px; }
  .chart { display:block; }
  .fcband { fill:#ffffff06; }
  .cross-v { stroke:var(--accent); stroke-width:1.5; stroke-dasharray:4 3; }
  .cross-dots circle { stroke:var(--bg); stroke-width:2; }
  /* Sits above the chart, not inside the scroller: anything absolutely
     positioned in a scrolling box scrolls away with the content. */
  .readout { display:flex; gap:20px; flex-wrap:wrap; align-items:baseline;
             background:var(--panel2); border:1px solid var(--line);
             border-radius:5px; padding:10px 14px; margin:0 0 10px;
             min-height:42px; font-size:12.5px; }
  .readout .rday { color:var(--accent); font-weight:700; letter-spacing:.08em; }
  .readout .rrow { display:inline-flex; align-items:center; gap:8px; }
  .readout .rval { font-weight:700; font-variant-numeric:tabular-nums; }
  .readout .rmuted { color:var(--faint); }
  .hint { color:var(--faint); font-size:11px; margin:6px 0 0; }
  .grid  { stroke:var(--line); stroke-width:1; }
  .hist  { fill:none; stroke:var(--hist); stroke-width:2; stroke-linejoin:round; }
  .split { stroke:var(--faint); stroke-width:1; stroke-dasharray:3 4; }
  .ylab  { fill:var(--muted); font:11.5px var(--mono); }
  .xlab  { fill:var(--faint); font:11px var(--mono); text-anchor:middle; }
  /* Anchored at the start because the label is drawn to the right of the line's
     last point, in the right-hand margin the value labels also live in. */
  .inline-label { font:12.5px var(--mono); text-anchor:start; }
  .hist-label { fill:var(--muted); text-anchor:middle; }
  .split-label { fill:var(--muted); font:11.5px var(--mono); }

  .legend { display:flex; gap:10px; flex-wrap:wrap; margin:4px 0 22px; }
  .legend .key { display:inline-flex; align-items:center; gap:8px; font:inherit;
                 font-size:11.5px; color:var(--fg); background:var(--panel2);
                 padding:6px 11px; border:1px solid var(--line); border-radius:4px;
                 cursor:pointer; }
  .legend .key:hover { border-color:var(--accent); }
  .legend .key[aria-pressed="false"] { color:var(--faint); opacity:.5;
                                       text-decoration:line-through; }
  .legend { margin:0 0 10px; }
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
    {{if gt (len .Models) 1}}{{len .Models}} models forecast{{else}}One line
    forecasts{{end}} the next {{.Horizon}} days. The last day of real data is
    <b>{{.AsOf}}</b>; everything to the right of the divider is predicted, not
    observed. The shaded band is the q10&ndash;q90 range &mdash; an 80% interval by
    construction, not 90% &mdash; and the line through the middle is the median.
    Treat the band as the models&rsquo; own optimism about their spread rather than
    a measured error bar: in backtests on real exports the actual fell inside it
    far less than 80% of the time. Which way it misses is not fixed &mdash; two
    backtests on different accounts ran in opposite directions &mdash; so do not
    read the band as a ceiling or a floor. <code>accuracy</code> measures the
    direction for <em>your</em> account; nothing on this page can.
    {{if gt (len .Models) 1}}Where the lines separate, they disagree — that gap
    is the honest measure of how sure any of this is.{{else}}The wider the band,
    the less sure the forecast.{{end}}
  </p>

  {{if .Excluded}}
  <div class="callout">
    <div class="cap">Not forecast</div>
    <strong>{{range $i, $e := .Excluded}}{{if $i}}, {{end}}{{$e}}{{end}}</strong>
    — switched off in the export, or nothing they record moved during this
    period. A campaign that is paused will spend nothing until someone turns it
    back on, which is a decision rather than something to predict. That is also
    why they are not in the dropdown above.
    <br><br>
    <strong>Read the (account) line with this in mind.</strong> The account series
    is the sum of every campaign, including these, so its history contains their
    spend — and the forecast of that series therefore carries on as though they
    were still running. Where a large campaign has just been switched off, the
    account forecast will be high by roughly its share. The per-campaign figures
    below do not have this problem: they cover only the campaigns that are
    actually running.
  </div>
  {{end}}


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

      <div class="legend" role="group" aria-label="show or hide a series">
        <button type="button" class="key" data-model="__actual" aria-pressed="true">
          <i class="swatch" style="border-top-color:var(--hist)"></i>actual
        </button>
        {{range $.Models}}
        <button type="button" class="key" data-model="{{.Name}}" aria-pressed="true">
          <i class="swatch" style="border-top-color:{{.Colour}};border-top-style:{{.Stroke}}"></i>{{.Name}}{{if .IsFineTuned}} · fine-tuned{{end}}
        </button>
        {{end}}
      </div>

      <div class="readout" aria-live="polite">
        <span class="rday">move across the chart to read a day</span>
      </div>

      <div class="scroller">{{.Chart}}</div>
      <p class="hint">Scroll left for older days · click a name above to hide that line</p>
      <script type="application/json" class="pane-data">{{.Data}}</script>
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
          <td>{{if .IsFineTuned}}trained on your data through {{.TrainedThrough}}{{else if .Derived}}{{.Derived}}{{else}}pretrained, unmodified{{end}}</td>
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

  // Crosshair and legend.
  //
  // The chart is drawn at a fixed pixel width, so a pointer position inside the
  // svg is already a chart coordinate -- that is why every day's x is handed over
  // in the JSON rather than re-derived here.
  //
  // The readout lives above the chart, not floating inside it: anything
  // absolutely positioned inside a scrolling box scrolls away with the content,
  // which is exactly how the first version managed to show nothing.
  function money(v) {
    var a = Math.abs(v);
    if (a >= 1000) return v.toLocaleString(undefined, {maximumFractionDigits: 0});
    if (a >= 1)    return v.toFixed(2);
    return v.toPrecision(3);
  }

  panes.forEach(function (pane) {
    var holder = pane.querySelector('.pane-data');
    var svg    = pane.querySelector('svg.chart');
    var box    = pane.querySelector('.readout');
    var scr    = pane.querySelector('.scroller');
    var keys   = Array.prototype.slice.call(pane.querySelectorAll('.key'));
    if (!holder || !svg || !box || !scr) return;

    var d;
    try { d = JSON.parse(holder.textContent); } catch (e) { return; }
    if (!d.xs || !d.xs.length) return;

    var cross = svg.querySelector('.cross');
    var vline = svg.querySelector('.cross-v');
    var dots  = svg.querySelector('.cross-dots');
    var hidden = {};
    var pinned = null;          // a clicked day stays put until clicked again

    function visible(model) { return !hidden[model]; }

    function nearest(x) {
      var best = 0, gap = Infinity;
      for (var i = 0; i < d.xs.length; i++) {
        var g = Math.abs(d.xs[i] - x);
        if (g < gap) { gap = g; best = i; }
      }
      return best;
    }

    // y for a value, taken from the chart's own scale so the markers land on the
    // lines rather than near them.
    var yTop = d.y0, ySpan = d.y1 - d.y0;
    function yOf(v) { return d.top + (d.bottom - d.top) * (1 - (v - yTop) / ySpan); }

    function readAt(i) {
      var parts = ['<span class="rday">' + d.days[i] + '</span>'];
      var marks = '';

      if (i < d.cut) {
        if (visible('__actual')) {
          parts.push('<span class="rrow"><i class="swatch" style="border-top-color:var(--hist)"></i>' +
                     'actual <span class="rval">' + money(d.actual[i]) + '</span></span>');
          marks += '<circle cx="' + d.xs[i] + '" cy="' + yOf(d.actual[i]) +
                   '" r="5" fill="var(--hist)"/>';
        }
        parts.push('<span class="rmuted">observed</span>');
      } else {
        var k = i - d.cut;
        d.lines.forEach(function (ln) {
          if (k >= ln.values.length || !visible(ln.model)) return;
          parts.push('<span class="rrow"><i class="swatch" style="border-top-color:' + ln.colour +
                     '"></i>' + ln.model + ' <span class="rval">' + money(ln.values[k]) + '</span></span>');
          marks += '<circle cx="' + d.xs[i] + '" cy="' + yOf(ln.values[k]) +
                   '" r="5" fill="' + ln.colour + '"/>';
        });
        parts.push('<span class="rmuted">forecast</span>');
      }

      box.innerHTML = parts.join('');
      dots.innerHTML = marks;
      vline.setAttribute('x1', d.xs[i]);
      vline.setAttribute('x2', d.xs[i]);
      cross.style.display = '';
    }

    function fromEvent(ev) {
      var r = svg.getBoundingClientRect();
      var x = (ev.touches ? ev.touches[0].clientX : ev.clientX) - r.left;
      return nearest(x * (svg.viewBox.baseVal.width / r.width));
    }

    svg.addEventListener('mousemove', function (ev) {
      if (pinned !== null) return;
      readAt(fromEvent(ev));
    });
    svg.addEventListener('touchmove', function (ev) { readAt(fromEvent(ev)); }, {passive: true});
    svg.addEventListener('click', function (ev) {
      var i = fromEvent(ev);
      pinned = (pinned === i) ? null : i;
      readAt(i);
    });
    svg.addEventListener('mouseleave', function () {
      if (pinned !== null) return;
      box.innerHTML = '<span class="rday">move across the chart to read a day</span>';
      dots.innerHTML = '';
      cross.style.display = 'none';
    });

    // Legend: click a name to take that line off the chart, the readout and the
    // end labels together.
    keys.forEach(function (key) {
      key.addEventListener('click', function () {
        var model = key.dataset.model;
        hidden[model] = !hidden[model];
        key.setAttribute('aria-pressed', hidden[model] ? 'false' : 'true');
        pane.querySelectorAll('[data-model="' + model + '"]').forEach(function (el) {
          if (el.classList.contains('key')) return;
          el.style.display = hidden[model] ? 'none' : '';
        });
        if (pinned !== null) readAt(pinned);
      });
    });

    // Open on the forecast; the history is one scroll to the left.
    requestAnimationFrame(function () { scr.scrollLeft = scr.scrollWidth; });
  });
})();
</script>
</body>
</html>
`))

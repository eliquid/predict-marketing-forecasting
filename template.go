package main

// The report page. One file, no network, no build step.

import (
	_ "embed"
	"html/template"
)

// htmx is embedded in the binary and inlined into every report, so the page is a
// single file that still works when it is moved, copied or emailed. A <script
// src="htmx.min.js"> tag would dangle the moment the report left this folder.
//
//go:embed assets/htmx.min.js
var htmxJS string

var reportTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Run.SeriesID}} — forecast</title>
<style>
  :root {
    --bg:#ffffff; --fg:#1a1a1a; --muted:#6b7280; --line:#e5e7eb;
    --hist:#2563eb; --fc:#7c3aed; --band:#7c3aed22; --accent:#7c3aed;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
      --bg:#0f1115; --fg:#e8e8ea; --muted:#9aa0aa; --line:#272b33;
      --hist:#60a5fa; --fc:#c4b5fd; --band:#c4b5fd22; --accent:#c4b5fd;
    }
  }
  * { box-sizing:border-box; }
  body { margin:0; padding:32px 16px; background:var(--bg); color:var(--fg);
         font:15px/1.55 ui-sans-serif,-apple-system,Segoe UI,Roboto,sans-serif; }
  main { max-width:940px; margin:0 auto; }
  h1 { font-size:22px; margin:0 0 4px; }
  h2 { font-size:16px; margin:0 0 6px; font-weight:600; }
  h2.entity { font-size:18px; margin:36px 0 14px; padding-bottom:6px;
              border-bottom:2px solid var(--line); }
  h3 { font-size:14px; margin:0 0 6px; font-weight:600; color:var(--muted); }
  details { margin:0 0 10px; border:1px solid var(--line); border-radius:6px; padding:10px 14px; }
  details[open] { padding-bottom:18px; }
  summary { cursor:pointer; font-weight:600; color:var(--fg); }
  table.summary td:first-child { max-width:340px; overflow:hidden; text-overflow:ellipsis; }
  tr.total td { font-weight:700; border-top:2px solid var(--line); }
  .note { color:var(--muted); font-size:12px; margin:6px 0 24px; }
  section { margin:0 0 40px; }
  section + section { padding-top:8px; border-top:1px solid var(--line); }
  .sub { color:var(--muted); font-size:13px; margin-bottom:24px; }
  .chart { width:100%; height:auto; display:block; margin:8px 0 4px; }
  .grid  { stroke:var(--line); stroke-width:1; }
  .band  { fill:var(--band); stroke:none; }
  .hist  { fill:none; stroke:var(--hist); stroke-width:2; stroke-linejoin:round; }
  .fc    { fill:none; stroke:var(--fc); stroke-width:2; stroke-dasharray:5 3; }
  .split { stroke:var(--line); stroke-width:1; stroke-dasharray:3 3; }
  .ylab  { fill:var(--muted); font-size:11px; text-anchor:end; }
  .xlab  { fill:var(--muted); font-size:11px; }
  .xlab.end { text-anchor:end; }
  .key { display:flex; gap:18px; flex-wrap:wrap; color:var(--muted); font-size:12px; margin-bottom:24px; }
  .key i { display:inline-block; width:14px; height:3px; vertical-align:middle; margin-right:6px; }
  table { border-collapse:collapse; width:100%; font-variant-numeric:tabular-nums; }
  th,td { padding:7px 10px; border-bottom:1px solid var(--line); text-align:right; }
  th:first-child, td:first-child { text-align:left; }
  th { color:var(--muted); font-weight:600; font-size:12px; text-transform:uppercase; letter-spacing:.04em; }
  details { margin-top:28px; color:var(--muted); font-size:13px; }
  summary { cursor:pointer; }
  pre { background:var(--line); padding:12px; border-radius:6px; overflow:auto; font-size:12px; color:var(--fg); }
  @media (max-width:640px) { body { padding:20px 16px; } th,td { padding:6px; } }
</style>
</head>
<body>
<main>
  <h1>{{.Run.SeriesID}}</h1>
  <div class="sub">
    {{.Run.Horizon}} days ahead &middot; {{.Run.Model}} &middot; generated {{.Generated}}
    {{if .Run.GroupBy}}&middot; split by {{.Run.GroupBy}}{{end}}
  </div>

  {{if .Excluded}}
  <p class="note">
    Not forecast, because the export says they are switched off, or because
    nothing they record moved during this period:
    <strong>{{range $i, $e := .Excluded}}{{if $i}}, {{end}}{{$e}}{{end}}</strong>.
    Their full history is still stored, and still counted in the account total.
  </p>
  {{end}}

  {{with .Account}}
  <h2 class="entity">{{.Name}} &mdash; all campaigns</h2>
  {{range .Metrics}}
  <section>
    <h3>{{.Name}}</h3>
    {{.Chart}}
    <div class="key">
      <span><i style="background:var(--hist)"></i>observed</span>
      <span><i style="background:var(--fc)"></i>forecast (median)</span>
      <span><i style="background:var(--accent);opacity:.25"></i>80% range (q10&ndash;q90)</span>
    </div>
    <table>
      <thead><tr><th>Day</th><th>Low (q10)</th><th>Median (q50)</th><th>High (q90)</th></tr></thead>
      <tbody>
      {{range .Rows}}
        <tr><td>{{.Day}}</td><td>{{.Low}}</td><td>{{.Median}}</td><td>{{.High}}</td></tr>
      {{end}}
      </tbody>
    </table>
  </section>
  {{end}}
  {{end}}

  {{if .Campaigns}}
  <h2 class="entity">Campaigns</h2>
  <table class="summary">
    <thead><tr><th>Campaign</th>{{range .MetricNames}}<th>{{.}}</th>{{end}}</tr></thead>
    <tbody>
    {{range .Campaigns}}
      <tr><td>{{.Name}}</td>{{range .Totals}}<td>{{.Value}}</td>{{end}}</tr>
    {{end}}
    {{with .Account}}
      <tr class="total"><td>{{.Name}}</td>{{range .Totals}}<td>{{.Value}}</td>{{end}}</tr>
    {{end}}
    </tbody>
  </table>
  <p class="note">Totals over the next {{.Run.Horizon}} days, median.</p>

  {{range .Campaigns}}
  <details>
    <summary>{{.Name}}</summary>
    {{range .Metrics}}
    <section>
      <h3>{{.Name}}</h3>
      {{.Chart}}
      <table>
        <thead><tr><th>Day</th><th>Low (q10)</th><th>Median (q50)</th><th>High (q90)</th></tr></thead>
        <tbody>
        {{range .Rows}}
          <tr><td>{{.Day}}</td><td>{{.Low}}</td><td>{{.Median}}</td><td>{{.High}}</td></tr>
        {{end}}
        </tbody>
      </table>
    </section>
    {{end}}
  </details>
  {{end}}
  {{end}}

  <details>
    <summary>What produced this</summary>
    <p>
      Model <strong>{{.Model.Repo}}</strong> at revision <code>{{.Model.Revision}}</code>,
      weights sha256 <code>{{.Model.WeightsSHA256}}</code>.
      Run <code>{{.Run.ID}}</code>, input fingerprint <code>{{.Run.InputHash}}</code>.
    </p>
    <pre>{{range $k, $v := .Model.Versions}}{{$k}} {{$v}}
{{end}}</pre>
  </details>
</main>
<script>{{.HTMX}}</script>
</body>
</html>
`))

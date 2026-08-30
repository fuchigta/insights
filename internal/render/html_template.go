package render

import "html/template"

// htmlTemplate は RenderHTML が使う唯一のテンプレート。
// html/template を通すことで、日本語・<script>・Windows パス（バックスラッシュ）などの
// 自由記述テキストが必ず正しくエスケープされる（文字列連結で HTML を組み立てない）。
//
// JS は使わない。ホバーのツールチップは SVG の <title> 要素（ブラウザ標準機能）で代替する。
var htmlTemplate = template.Must(template.New("root").Parse(htmlTemplateSource))

const htmlTemplateSource = `
{{define "page"}}<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{
  color-scheme: light;
  --surface:#fcfcfb; --page:#f9f9f7; --text:#0b0b0b; --text2:#52514e; --muted:#898781;
  --grid:#e1e0d9; --axis:#c3c2b7; --border:rgba(11,11,11,0.10);
  --good:#0ca30c; --warn:#fab219; --serious:#ec835a; --critical:#d03b3b;
  --s1:#2a78d6; --s2:#eb6834; --s3:#1baf7a; --s4:#eda100; --s5:#e87ba4; --s6:#008300; --s7:#4a3aa7; --s8:#e34948;
}
@media (prefers-color-scheme: dark){
  :root{
    color-scheme: dark;
    --surface:#1a1a19; --page:#0d0d0d; --text:#ffffff; --text2:#c3c2b7; --muted:#898781;
    --grid:#2c2c2a; --axis:#383835; --border:rgba(255,255,255,0.10);
    --good:#0ca30c; --warn:#fab219; --serious:#ec835a; --critical:#d03b3b;
    --s1:#3987e5; --s2:#d95926; --s3:#199e70; --s4:#c98500; --s5:#d55181; --s6:#008300; --s7:#9085e9; --s8:#e66767;
  }
}
*{ box-sizing:border-box; }
body{ margin:0; background:var(--page); color:var(--text); font-family:system-ui,-apple-system,"Segoe UI",sans-serif; line-height:1.6; }
.wrap{ max-width:960px; margin:0 auto; padding:24px 16px 64px; }
h1{ font-size:22px; margin:0 0 4px; word-break:break-word; }
h2{ font-size:17px; margin:32px 0 8px; padding-bottom:6px; border-bottom:1px solid var(--grid); }
h3{ font-size:14px; margin:20px 0 6px; color:var(--text2); }
p.lead{ color:var(--text2); margin:0 0 24px; font-size:13px; }
.card{ background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:16px; margin-bottom:16px; overflow-x:auto; }
.stat-row{ display:flex; flex-wrap:wrap; gap:12px; margin-bottom:16px; }
.stat-tile{ flex:1 1 160px; background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:12px 14px; }
.stat-tile .label{ font-size:12px; color:var(--text2); }
.stat-tile .value{ font-size:20px; font-weight:600; margin-top:2px; word-break:break-word; }
.chart-svg{ width:100%; height:auto; display:block; }
.grid-line{ stroke:var(--grid); stroke-width:1; }
.axis-line{ stroke:var(--axis); stroke-width:1; }
.divider, .divider-v{ stroke:var(--surface); stroke-width:2; }
.line{ fill:none; stroke-width:2; stroke-linecap:round; stroke-linejoin:round; }
.axis-label{ font-size:10px; fill:var(--muted); }
.value-label-inv{ font-size:10px; font-weight:600; fill:#ffffff; paint-order:stroke; stroke:rgba(0,0,0,0.35); stroke-width:2px; stroke-linejoin:round; }
.legend{ display:flex; flex-wrap:wrap; gap:10px 16px; margin-top:10px; font-size:12px; color:var(--text2); }
.legend .item{ display:flex; align-items:center; gap:6px; }
.swatch{ width:10px; height:10px; border-radius:2px; display:inline-block; flex:none; }
table{ width:100%; border-collapse:collapse; font-size:12px; }
th,td{ text-align:left; padding:6px 8px; border-bottom:1px solid var(--grid); vertical-align:top; }
th{ color:var(--text2); font-weight:600; }
caption{ text-align:left; font-size:12px; color:var(--text2); margin-bottom:4px; }
details.table-wrap{ margin-top:10px; }
details.table-wrap summary{ cursor:pointer; color:var(--text2); font-size:12px; }
.note{ color:var(--muted); font-size:12px; margin:4px 0; }
.desc{ color:var(--text2); font-size:12px; margin:0 0 10px; }
ul.caveats{ font-size:12px; color:var(--text2); padding-left:18px; }
ul.caveats li{ margin-bottom:4px; }
</style>
</head>
<body>
<div class="wrap">

<h1>{{.Title}}</h1>
<p class="lead">{{.PeriodLabel}}</p>

<h2>期間サマリ</h2>
<div class="stat-row">
  <div class="stat-tile"><div class="label">データのある日数</div><div class="value">{{.PeriodDays}} 日間</div></div>
  <div class="stat-tile"><div class="label">セッション数</div><div class="value">{{.SessionsLabel}}</div></div>
  <div class="stat-tile"><div class="label">総コスト</div><div class="value">{{.CostLabel}}</div></div>
  <div class="stat-tile"><div class="label">総時間</div><div class="value">{{.DurationLabel}}</div></div>
  <div class="stat-tile"><div class="label">評価済み / 総セッション</div><div class="value">{{.EvaluatedLabel}}</div></div>
</div>

<h2>コストの推移</h2>
{{template "chart" .CostTrendChart}}
<h3>コスト内訳</h3>
{{template "chart" .CostBreakdownChart}}

<h2>成果の推移</h2>
{{template "chart" .AchievedRatioChart}}
<h3>成果の日次構成比</h3>
{{template "chart" .OutcomeChart}}

<h2>やり方の推移</h2>
<h3>モデル適合 (model_fit)</h3>
{{template "chart" .ModelFitChart}}
<h3>主体性 (ownership)</h3>
{{template "chart" .OwnershipChart}}
<h3>前半 / 後半の比較</h3>
{{template "halfcompare" .HalfCompare}}

<h2>改善アクションの消化状況</h2>
{{template "chart" .ActionStatusChart}}
<h3>提案累計 vs 完了累計</h3>
{{template "chart" .ActionCumulativeChart}}
<h3>アクション一覧</h3>
<div class="card">
{{template "table" .ActionsTable}}
</div>

{{if .EvalHealth}}
<h2>評価の健全性</h2>
<div class="stat-row">
  <div class="stat-tile"><div class="label">評価実行回数</div><div class="value">{{.EvalHealth.TotalLabel}}</div></div>
  <div class="stat-tile"><div class="label">成功</div><div class="value">{{.EvalHealth.SucceededLabel}}</div></div>
  <div class="stat-tile"><div class="label">失敗</div><div class="value">{{.EvalHealth.FailedLabel}}</div></div>
  <div class="stat-tile"><div class="label">評価コスト（失敗分を含む）</div><div class="value">{{.EvalHealth.CostLabel}}</div></div>
</div>
<h3>失敗の内訳</h3>
<div class="card">
{{template "table" .EvalHealth.FailuresTable}}
</div>
{{end}}

{{if .Caveats}}
<h2>但し書き</h2>
<ul class="caveats">
{{range .Caveats}}<li>{{.}}</li>{{end}}
</ul>
{{end}}

</div>
</body>
</html>
{{end}}

{{define "chart"}}<div class="card">
  {{if .Title}}<h3 style="margin-top:0">{{.Title}}</h3>{{end}}
  {{if .Note}}
    <p class="note">{{.Note}}</p>
  {{else}}
    {{if .Desc}}<p class="desc">{{.Desc}}</p>{{end}}
    <svg viewBox="{{.SVG.ViewBox}}" class="chart-svg" role="img" aria-label="{{.Title}}">
{{range .SVG.Lines}}      <line x1="{{.X1}}" y1="{{.Y1}}" x2="{{.X2}}" y2="{{.Y2}}" class="{{.Class}}" />
{{end}}{{range .SVG.Rects}}      <rect x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="{{.H}}" fill="{{.Fill}}">{{if .Title}}<title>{{.Title}}</title>{{end}}</rect>
{{end}}{{range .SVG.Dividers}}      <line x1="{{.X1}}" y1="{{.Y1}}" x2="{{.X2}}" y2="{{.Y2}}" class="{{.Class}}" />
{{end}}{{range .SVG.Paths}}      <path d="{{.D}}" stroke="{{.Stroke}}" class="{{.Class}}" />
{{end}}{{range .SVG.Circles}}      <circle cx="{{.Cx}}" cy="{{.Cy}}" r="{{.R}}" fill="{{.Fill}}">{{if .Title}}<title>{{.Title}}</title>{{end}}</circle>
{{end}}{{range .SVG.Texts}}      <text x="{{.X}}" y="{{.Y}}" text-anchor="{{.Anchor}}" class="{{.Class}}">{{.Text}}</text>
{{end}}    </svg>
    {{if .Legend}}<div class="legend">
    {{range .Legend}}<span class="item"><span class="swatch" style="background:{{.Color}}"></span>{{.Label}}</span>{{end}}
    </div>{{end}}
  {{end}}
  {{if .Table}}<details class="table-wrap"><summary>データ表を表示</summary>
{{template "table" .Table}}
  </details>{{end}}
</div>
{{end}}

{{define "table"}}{{if .}}{{if .Rows}}<table>
  <caption>{{.Caption}}</caption>
  <thead><tr>{{range .Headers}}<th>{{.}}</th>{{end}}</tr></thead>
  <tbody>
  {{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>
  {{end}}</tbody>
</table>{{else}}<p class="note">{{.Caption}}: データがありません。</p>{{end}}{{end}}{{end}}

{{define "halfcompare"}}<div class="card">
{{if .Applicable}}<table>
<thead><tr><th>指標</th><th>前半</th><th>後半</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Label}}</td><td>{{.FirstPct}}（{{.FirstN}}）</td><td>{{.SecondPct}}（{{.SecondN}}）</td></tr>
{{end}}</tbody>
</table>{{else}}<p class="note">{{.Note}}</p>{{end}}
</div>
{{end}}
`

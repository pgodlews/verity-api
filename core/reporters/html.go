package reporters

import (
	"html/template"

	"github.com/pgodlews/verity-api/core/spi"
)

// HTMLReporter renders a self-contained HTML report (inline CSS, no external
// assets) suitable for CI artifact upload. It uses html/template, so all
// dynamic values are auto-escaped; values are also pre-redacted by the runner.
type HTMLReporter struct{}

func (HTMLReporter) Type() string { return "html" }

func (r *HTMLReporter) Report(result *spi.RunResult, out string) (err error) {
	w, closeFn, err := openOut(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeFn(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return htmlTemplate.Execute(w, result)
}

var htmlTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"statusClass": func(s spi.Status) string { return string(s) },
}).Parse(htmlSource))

const htmlSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Verity Report — {{.Project}}</title>
<style>
  body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 2rem; color: #1a1a1a; }
  h1 { font-size: 1.4rem; }
  .summary { padding: .75rem 1rem; border-radius: 6px; margin: 1rem 0; }
  .passed { background: #e6f4ea; color: #137333; }
  .failed { background: #fce8e6; color: #c5221f; }
  .error  { background: #fef7e0; color: #b06000; }
  .skipped{ background: #f1f3f4; color: #5f6368; }
  .aborted{ background: #e8eaf6; color: #3949ab; }
  .suite { border: 1px solid #e0e0e0; border-radius: 6px; margin: 1rem 0; }
  .suite > .hd { padding: .5rem 1rem; font-weight: 600; background: #fafafa; }
  .case { padding: .25rem 1rem .25rem 1.5rem; }
  .step { padding: .15rem 1rem .15rem 2.5rem; }
  .assert { padding: .1rem 1rem .1rem 3.5rem; font-size: .9rem; color: #444; }
  .badge { display: inline-block; min-width: 3.5rem; text-align: center; padding: 0 .4rem; border-radius: 4px; font-size: .75rem; margin-right: .5rem; }
  pre { background: #f6f8fa; padding: .5rem; border-radius: 4px; overflow-x: auto; font-size: .8rem; }
</style>
</head>
<body>
<h1>Verity Report — {{.Project}}{{if .Environment}} <small>[{{.Environment}}]</small>{{end}}</h1>
<div class="summary {{statusClass .Summary.Status}}">
  <strong>{{.Summary.Status}}</strong>{{if .Summary.Bailed}} (bailed){{end}}{{if .Summary.Aborted}} (aborted){{end}} —
  suites {{.Summary.Suites}}, cases {{.Summary.Cases}}, steps {{.Summary.Steps}} (failed {{.Summary.StepsFailed}}, errors {{.Summary.Errors}}, skipped {{.Summary.Skipped}}, aborted {{.Summary.StepsAborted}}),
  assertions {{.Summary.Assertions}} (passed {{.Summary.Passed}}, failed {{.Summary.Failed}}) — {{.DurationMS}}ms
</div>
{{range .Suites}}
<div class="suite">
  <div class="hd"><span class="badge {{statusClass .Status}}">{{.Status}}</span>{{.Name}} <small>({{.DurationMS}}ms)</small></div>
  {{range .Cases}}
  <div class="case"><span class="badge {{statusClass .Status}}">{{.Status}}</span>{{.Name}}{{if ge .Iteration 0}} [row {{.Iteration}}]{{end}}</div>
    {{range .Steps}}
    <div class="step"><span class="badge {{statusClass .Status}}">{{.Status}}</span>{{.Name}} <small>[{{.Protocol}}] {{.DurationMS}}ms{{if gt .Attempts 1}} · {{.Attempts}} attempts{{end}}</small></div>
      {{if .Error}}<div class="assert">⚠ {{.Error.Code}}: {{.Error.Message}}</div>{{end}}
      {{range .Assertions}}
      <div class="assert">{{if .Passed}}✓{{else}}✗{{end}} {{if .Description}}{{.Description}}{{else}}{{.Type}}{{end}}{{if not .Passed}} — {{.Message}}{{end}}</div>
      {{end}}
    {{end}}
  {{end}}
</div>
{{end}}
</body>
</html>`

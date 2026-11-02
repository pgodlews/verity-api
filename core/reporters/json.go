package reporters

import (
	"encoding/json"

	"github.com/pgodlews/verity-api/core/spi"
)

// JSONReporter writes the structured run result as indented JSON. This is the
// canonical machine-readable artifact for CI and AI agents. All string fields
// in RunResult are already redacted by the runner.
type JSONReporter struct{}

func (JSONReporter) Type() string { return "json" }

func (r *JSONReporter) Report(result *spi.RunResult, out string) (err error) {
	w, closeFn, err := openOut(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeFn(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/spi"
)

// reportTo runs a reporter to a temp file and copies the result into buf. This
// exercises the real reporter write path used by the CLI.
func reportTo(rep spi.Reporter, res *spi.RunResult, buf *bytes.Buffer) error {
	dir, err := os.MkdirTemp("", "verity-report")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "out")
	if err := rep.Report(res, path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(buf, f)
	return err
}

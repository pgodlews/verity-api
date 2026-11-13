// Package datasource provides Verity's built-in data-driven feed sources: CSV
// and JSON. Each implements spi.DataSource and is registered through the
// registry like any third-party feed.
package datasource

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/internal/scalar"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/verr"
)

// Register installs the built-in data sources.
func Register(r *registry.Registry) {
	r.RegisterDataSource(csvSource{})
	r.RegisterDataSource(jsonSource{})
}

// csvSource reads rows from a CSV file. The first row is treated as the header
// (column names) unless config["header"] == false, in which case columns are
// named col0, col1, ...
type csvSource struct{}

func (csvSource) Type() string { return "csv" }
func (csvSource) Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error) {
	if len(inline) > 0 {
		return inline, nil
	}
	full, err := fsutil.ResolveInside(dir, path, fsutil.AllowExternal(config), "CSV feed")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("open CSV feed %q", path))
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("read CSV feed %q", path))
	}
	if len(records) == 0 {
		return nil, nil
	}

	// Excel and other tools write a UTF-8 byte-order mark; it is not part
	// of the first column's name or value.
	records[0][0] = strings.TrimPrefix(records[0][0], "\ufeff")

	hasHeader := true
	if h, ok := config["header"].(bool); ok {
		hasHeader = h
	}
	var header []string
	var dataRows [][]string
	if hasHeader {
		header = records[0]
		seen := map[string]bool{}
		for _, col := range header {
			if seen[col] {
				return nil, verr.Newf(verr.CodeConfig, "CSV feed %q has duplicate column %q", path, col)
			}
			seen[col] = true
		}
		dataRows = records[1:]
	} else {
		header = make([]string, len(records[0]))
		for i := range header {
			header[i] = fmt.Sprintf("col%d", i)
		}
		dataRows = records
	}

	out := make([]map[string]string, 0, len(dataRows))
	for _, rec := range dataRows {
		row := map[string]string{}
		for i, col := range header {
			if i < len(rec) {
				row[col] = rec[i]
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// jsonSource reads rows from a JSON file containing an array of objects. Values
// are coerced to strings.
type jsonSource struct{}

func (jsonSource) Type() string { return "json" }
func (jsonSource) Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error) {
	if len(inline) > 0 {
		return inline, nil
	}
	full, err := fsutil.ResolveInside(dir, path, fsutil.AllowExternal(config), "JSON feed")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("read JSON feed %q", path))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("parse JSON feed %q (expected array of objects)", path))
	}
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		row := map[string]string{}
		for k, v := range r {
			// null is "", nested objects/arrays are compact JSON text.
			row[k] = scalar.String(v)
		}
		out = append(out, row)
	}
	return out, nil
}

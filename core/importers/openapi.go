package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// openAPIDoc is a minimal OpenAPI 3.x document model — just enough to generate
// request templates. Unsupported fields are ignored.
type openAPIDoc struct {
	OpenAPI string `json:"openapi" yaml:"openapi"`
	Swagger string `json:"swagger" yaml:"swagger"`
	Info    struct {
		Title   string `json:"title" yaml:"title"`
		Version string `json:"version" yaml:"version"`
	} `json:"info" yaml:"info"`
	Servers []struct {
		URL string `json:"url" yaml:"url"`
	} `json:"servers" yaml:"servers"`
	// Swagger 2.0 fields.
	Host     string   `json:"host" yaml:"host"`
	BasePath string   `json:"basePath" yaml:"basePath"`
	Schemes  []string `json:"schemes" yaml:"schemes"`

	Paths map[string]map[string]openAPIOperation `json:"paths" yaml:"paths"`
}

type openAPIOperation struct {
	OperationID string         `json:"operationId" yaml:"operationId"`
	Summary     string         `json:"summary" yaml:"summary"`
	Responses   map[string]any `json:"responses" yaml:"responses"`
}

// httpMethods is the set of keys under a path item that are operations.
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "options": true,
}

// ImportOpenAPI reads an OpenAPI/Swagger spec (JSON or YAML) and generates a
// Verity project with one suite containing a case per operation. Each step is a
// REST request template with a status assertion as a starting point.
func ImportOpenAPI(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "read OpenAPI spec")
	}
	doc, err := decodeOpenAPI(data, path)
	if err != nil {
		return nil, err
	}

	baseURL := openAPIBaseURL(doc)
	res := &Result{}
	if baseURL == "" {
		baseURL = "${BASE_URL}"
		res.Warnings = append(res.Warnings,
			"no server URL found; using ${BASE_URL} variable placeholder")
	}

	title := doc.Info.Title
	if title == "" {
		title = "Imported OpenAPI Project"
	}

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          title,
		Description:   fmt.Sprintf("Generated from OpenAPI spec %s", path),
		Environments: []model.Environment{{
			Name:    "default",
			Default: true,
			Variables: map[string]string{
				"BASE_URL": baseURL,
			},
		}},
	}

	suite := model.Suite{Name: "API"}

	// Deterministic ordering for stable, diffable output.
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		ops := doc.Paths[p]
		methods := make([]string, 0, len(ops))
		for m := range ops {
			if httpMethods[strings.ToLower(m)] {
				methods = append(methods, m)
			}
		}
		sort.Strings(methods)

		for _, m := range methods {
			op := ops[m]
			name := op.OperationID
			if name == "" {
				name = fmt.Sprintf("%s %s", strings.ToUpper(m), p)
			}
			// Build a URL template; OpenAPI path params {id} become ${id}.
			urlPath := convertPathParams(p)
			step := model.Step{
				Name:     name,
				Protocol: "rest",
				Request: map[string]any{
					"method": strings.ToUpper(m),
					"url":    "${BASE_URL}" + urlPath,
				},
				Assertions: []model.Assertion{
					{Type: "status", Op: "lt", Value: 400, Description: "successful response"},
				},
			}
			suite.Cases = append(suite.Cases, model.TestCase{
				Name:  name,
				Steps: []model.Step{step},
			})
		}
	}

	if len(suite.Cases) == 0 {
		return nil, verr.New(verr.CodeImport, "OpenAPI spec contained no operations")
	}
	project.Suites = []model.Suite{suite}
	res.Project = project
	return res, nil
}

func decodeOpenAPI(data []byte, path string) (*openAPIDoc, error) {
	var doc openAPIDoc
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, verr.Wrap(verr.CodeImport, err, "parse OpenAPI JSON")
		}
	} else {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, verr.Wrap(verr.CodeImport, err, "parse OpenAPI YAML")
		}
	}
	if len(doc.Paths) == 0 {
		return nil, verr.New(verr.CodeImport, "OpenAPI spec has no paths")
	}
	return &doc, nil
}

func openAPIBaseURL(doc *openAPIDoc) string {
	if len(doc.Servers) > 0 && doc.Servers[0].URL != "" {
		return strings.TrimRight(doc.Servers[0].URL, "/")
	}
	// Swagger 2.0 fallback.
	if doc.Host != "" {
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		return fmt.Sprintf("%s://%s%s", scheme, doc.Host, strings.TrimRight(doc.BasePath, "/"))
	}
	return ""
}

// convertPathParams rewrites OpenAPI {param} placeholders into Verity ${param}
// variable references so they interpolate at run time.
func convertPathParams(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '{' {
			end := strings.IndexByte(p[i:], '}')
			if end > 0 {
				name := p[i+1 : i+end]
				b.WriteString("${")
				b.WriteString(name)
				b.WriteString("}")
				i += end
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

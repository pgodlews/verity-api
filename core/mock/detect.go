package mock

import (
	"bytes"
	"encoding/xml"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// Detect classifies a mock input as OpenAPI 3.x, WSDL 1.1, or a Verity project.
func Detect(data []byte) (Kind, error) {
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 {
		return "", verr.New(verr.CodeConfig, "empty mock input")
	}
	if trim[0] == '<' {
		if isWSDL(trim) {
			return KindWSDL, nil
		}
		return "", verr.New(verr.CodeConfig, "XML input is not a WSDL 1.1 document")
	}
	var probe map[string]any
	if err := yaml.Unmarshal(trim, &probe); err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "parse mock input")
	}
	if v, ok := probe["openapi"].(string); ok {
		if strings.HasPrefix(v, "3.") {
			return KindOpenAPI, nil
		}
		return "", verr.Newf(verr.CodeConfig, "unsupported OpenAPI version %q (want 3.x)", v)
	}
	if _, ok := probe["swagger"]; ok {
		return "", verr.New(verr.CodeConfig, "Swagger 2.0 is not supported by verity mock; use OpenAPI 3.x")
	}
	if _, ok := probe["schemaVersion"]; ok {
		return KindProject, nil
	}
	if _, ok := probe["suites"]; ok || probe["mocks"] != nil {
		return KindProject, nil
	}
	return "", verr.New(verr.CodeConfig, "unrecognized mock input (want OpenAPI 3.x, WSDL 1.1, or a Verity project)")
}

func isWSDL(data []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		return se.Name.Local == "definitions"
	}
}

func unmarshalProject(data []byte) (*model.Project, error) {
	var p model.Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse project")
	}
	return &p, nil
}

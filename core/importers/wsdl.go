package importers

import (
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// WSDL 1.1 structural model — enough to enumerate operations and build SOAP
// request envelope templates. XSD message part types are not fully expanded;
// generated envelopes include the operation element and a comment placeholder,
// which the user fills in. This keeps the importer pure-Go and lean while still
// producing runnable scaffolding.
type wsdlDefinitions struct {
	XMLName         xml.Name       `xml:"definitions"`
	TargetNamespace string         `xml:"targetNamespace,attr"`
	Services        []wsdlService  `xml:"service"`
	Bindings        []wsdlBinding  `xml:"binding"`
	PortTypes       []wsdlPortType `xml:"portType"`
}

type wsdlService struct {
	Name  string     `xml:"name,attr"`
	Ports []wsdlPort `xml:"port"`
}

type wsdlPort struct {
	Name    string          `xml:"name,attr"`
	Binding string          `xml:"binding,attr"`
	Address wsdlSOAPAddress `xml:"address"`
}

type wsdlSOAPAddress struct {
	Location string `xml:"location,attr"`
}

type wsdlBinding struct {
	Name       string          `xml:"name,attr"`
	Type       string          `xml:"type,attr"`
	Operations []wsdlBindingOp `xml:"operation"`
}

type wsdlBindingOp struct {
	Name      string           `xml:"name,attr"`
	Operation wsdlSOAPOpAction `xml:"operation"`
}

type wsdlSOAPOpAction struct {
	SOAPAction string `xml:"soapAction,attr"`
}

type wsdlPortType struct {
	Name       string           `xml:"name,attr"`
	Operations []wsdlPortTypeOp `xml:"operation"`
}

type wsdlPortTypeOp struct {
	Name string `xml:"name,attr"`
}

// ImportWSDL reads a WSDL 1.1 document and generates a Verity project with a
// SOAP suite. One case per operation is created, each with a SOAP request
// template (auto-generated envelope) and a status assertion.
func ImportWSDL(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "read WSDL")
	}
	var def wsdlDefinitions
	if err := xml.Unmarshal(data, &def); err != nil {
		return nil, verr.Wrap(verr.CodeImport, err, "parse WSDL")
	}

	res := &Result{}
	endpoint := wsdlEndpoint(&def)
	if endpoint == "" {
		endpoint = "${SERVICE_URL}"
		res.Warnings = append(res.Warnings,
			"no soap:address location found; using ${SERVICE_URL} placeholder")
	}

	// Map binding operation name -> SOAPAction.
	actions := map[string]string{}
	for _, b := range def.Bindings {
		for _, op := range b.Operations {
			actions[op.Name] = op.Operation.SOAPAction
		}
	}

	// Collect operations from portTypes (the abstract interface).
	type opInfo struct{ name, action string }
	var ops []opInfo
	seen := map[string]bool{}
	for _, pt := range def.PortTypes {
		for _, op := range pt.Operations {
			if seen[op.Name] {
				continue
			}
			seen[op.Name] = true
			ops = append(ops, opInfo{name: op.Name, action: actions[op.Name]})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].name < ops[j].name })

	if len(ops) == 0 {
		return nil, verr.New(verr.CodeImport, "WSDL contained no operations")
	}

	name := "Imported SOAP Project"
	if len(def.Services) > 0 && def.Services[0].Name != "" {
		name = def.Services[0].Name
	}

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          name,
		Description:   fmt.Sprintf("Generated from WSDL %s", path),
		Environments: []model.Environment{{
			Name:    "default",
			Default: true,
			Variables: map[string]string{
				"SERVICE_URL": endpoint,
			},
		}},
	}

	suite := model.Suite{Name: "SOAP"}
	for _, op := range ops {
		step := model.Step{
			Name:     op.name,
			Protocol: "soap",
			Request: map[string]any{
				"url":        "${SERVICE_URL}",
				"operation":  op.name,
				"namespace":  def.TargetNamespace,
				"soapAction": op.action,
				"parameters": map[string]any{
					"_comment": "fill in request parameters for " + op.name,
				},
			},
			Assertions: []model.Assertion{
				{Type: "status", Value: 200, Description: "HTTP 200 OK"},
			},
		}
		suite.Cases = append(suite.Cases, model.TestCase{
			Name:  op.name,
			Steps: []model.Step{step},
		})
	}
	suite.Cases = uniqueCaseNames(suite.Cases)
	project.Suites = []model.Suite{suite}
	res.Project = project
	return res, nil
}

func wsdlEndpoint(def *wsdlDefinitions) string {
	for _, svc := range def.Services {
		for _, port := range svc.Ports {
			if loc := strings.TrimSpace(port.Address.Location); loc != "" {
				return loc
			}
		}
	}
	return ""
}

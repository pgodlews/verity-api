package importers

import (
	"path/filepath"
	"testing"
)

func TestImportOpenAPI(t *testing.T) {
	res, err := ImportOpenAPI(filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	if p.Name != "Petstore" {
		t.Fatalf("expected project name Petstore, got %q", p.Name)
	}
	if len(p.Suites) != 1 || len(p.Suites[0].Cases) != 3 {
		t.Fatalf("expected 1 suite with 3 cases, got %d suites", len(p.Suites))
	}
	// Path params must be converted to ${var} references.
	found := false
	for _, c := range p.Suites[0].Cases {
		if c.Name == "getPetById" {
			url, _ := c.Steps[0].Request["url"].(string)
			if url != "${BASE_URL}/pets/${petId}" {
				t.Fatalf("path param not converted: %q", url)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("getPetById case not generated")
	}
	// Generated project must validate.
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("generated project invalid: %v", errs)
	}
}

func TestImportWSDL(t *testing.T) {
	res, err := ImportWSDL(filepath.Join("..", "..", "examples", "specs", "calculator.wsdl"))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	if len(p.Suites) != 1 || len(p.Suites[0].Cases) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(p.Suites[0].Cases))
	}
	// The service endpoint should be captured.
	url := p.Environments[0].Variables["SERVICE_URL"]
	if url != "http://example.com/calculator" {
		t.Fatalf("unexpected endpoint: %q", url)
	}
	// SOAPAction should be carried into the Add step.
	for _, c := range p.Suites[0].Cases {
		if c.Name == "Add" {
			action, _ := c.Steps[0].Request["soapAction"].(string)
			if action != "http://example.com/calculator/Add" {
				t.Fatalf("soapAction not captured: %q", action)
			}
		}
	}
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("generated project invalid: %v", errs)
	}
}

package workspace

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// The desktop bridge carries JSON request bodies and GraphQL variables as
// source text so JavaScript never converts their number literals to float64.
func exposeJSONText(p *model.Project) error {
	for _, suite := range p.Suites {
		for _, testCase := range suite.Cases {
			for _, step := range testCase.Steps {
				if step.Request == nil {
					continue
				}
				var field, textField string
				switch step.Protocol {
				case "rest":
					field, textField = "json", "jsonText"
				case "graphql":
					field, textField = "variables", "variablesText"
				default:
					continue
				}
				value, ok := step.Request[field]
				if !ok {
					continue
				}
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				delete(step.Request, field)
				step.Request[textField] = string(raw)
			}
		}
	}
	return nil
}

func normalizedStepJSONText(step model.Step) (model.Step, error) {
	if step.Request == nil {
		return step, nil
	}
	request := make(map[string]any, len(step.Request))
	for key, value := range step.Request {
		request[key] = value
	}
	step.Request = request
	var field, textField string
	switch step.Protocol {
	case "rest":
		field, textField = "json", "jsonText"
	case "graphql":
		field, textField = "variables", "variablesText"
	default:
		return step, nil
	}
	text, ok := request[textField]
	if !ok {
		return step, nil
	}
	raw, ok := text.(string)
	if !ok {
		return model.Step{}, verr.Newf(verr.CodeConfig, "request.%s must be a JSON string", textField)
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return model.Step{}, verr.Wrap(verr.CodeConfig, err, "request."+textField+" is not valid JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return model.Step{}, verr.Newf(verr.CodeConfig, "request.%s must contain one JSON value", textField)
		}
		return model.Step{}, verr.Wrap(verr.CodeConfig, err, "request."+textField+" is not valid JSON")
	}
	delete(request, textField)
	request[field] = model.PreserveJSONNumbers(value)
	return step, nil
}

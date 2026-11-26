package mcpserver

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/wire"
)

// Decides as the SDK's Elicit does.
func canElicit(session *mcp.ServerSession) bool {
	init := session.InitializeParams()
	if init == nil || init.Capabilities == nil || init.Capabilities.Elicitation == nil {
		return false
	}
	caps := init.Capabilities.Elicitation
	return caps.Form != nil || caps.URL == nil
}

// A first call returns the form, which the handler returns instead of running. The client retries with the
// values, and a declined or canceled form fails the retry so that it does not run without them.
func askForParams(req *mcp.CallToolRequest, what string, params []wire.Param, args map[string]wire.RawArg) (*mcp.CallToolResult, error) {
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
	missing := map[string]wire.Param{}
	for _, p := range params {
		_, given := args[p.Name]
		_, required := p.DisplayPresence(os.Getenv).(wire.Required)
		if required && !given {
			schema.Properties[p.Name] = fieldSchema(p)
			schema.Required = append(schema.Required, p.Name)
			missing[p.Name] = p
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	names := strings.Join(schema.Required, ", ")
	response, retried := req.Params.InputResponses[paramsForm].(*mcp.ElicitResult)
	if !retried {
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{paramsForm: &mcp.ElicitParams{
			Message:         fmt.Sprintf("%s needs %s.", what, names),
			RequestedSchema: schema,
		}}}, nil
	}
	switch response.Action {
	case "accept":
	case "decline":
		return nil, fmt.Errorf("the user declined to give %s for %s", names, what)
	default:
		return nil, fmt.Errorf("the user canceled giving %s for %s", names, what)
	}
	for name, value := range response.Content {
		p, ok := missing[name]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if p.Type.IsList() {
				args[name] = append(wire.RawArg{}, wire.SplitList(v)...)
			} else {
				args[name] = wire.RawArg{v}
			}
		case float64:
			if p.Type == wire.ParamTypeLong {
				return nil, fmt.Errorf("param '%s': enter a long as decimal text to preserve its value", name)
			}
			args[name] = wire.RawArg{strconv.FormatFloat(v, 'f', -1, 64)}
		case bool:
			args[name] = wire.RawArg{strconv.FormatBool(v)}
		}
	}
	return nil, nil
}

// Longs use decimal text because the form transport decodes JSON numbers through float64.
// A string_list takes comma-separated values, since a form's arrays only pick from choices.
func fieldSchema(p wire.Param) *jsonschema.Schema {
	field := &jsonschema.Schema{Title: p.Name, Description: p.Description, Type: "string"}
	switch p.Type {
	case wire.ParamTypeLong:
		field.Description = strings.TrimSpace(p.Description + " Enter a whole number as decimal text.")
	case wire.ParamTypeInt:
		field.Type = "integer"
	case wire.ParamTypeDouble:
		field.Type = "number"
	case wire.ParamTypeBoolean:
		field.Type = "boolean"
	case wire.ParamTypeEnum:
		for _, c := range p.Choices {
			field.Enum = append(field.Enum, c)
		}
	case wire.ParamTypeStringList:
		field.Description = strings.TrimSpace(p.Description + " Separate the values with commas.")
	}
	return field
}

const paramsForm = "params"

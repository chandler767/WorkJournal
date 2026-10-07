package skills

import (
	"bytes"
	"embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Names lists the skills in the work-journal plugin.
var Names = []string{"summarize-hour", "categorize-time", "summarize-meeting", "close-day", "weekly-rollup"}

//go:embed schemas/*.json
var schemaFS embed.FS

var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

// schema returns the compiled output schema of a skill.
func schema(skill string) (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		compiled = map[string]*jsonschema.Schema{}
		c := jsonschema.NewCompiler()
		for _, name := range Names {
			b, err := schemaFS.ReadFile("schemas/" + name + ".json")
			if err != nil {
				compileErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				compileErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
			loc := "mem:///" + name + ".json"
			if err := c.AddResource(loc, doc); err != nil {
				compileErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
			if compiled[name], err = c.Compile(loc); err != nil {
				compileErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
		}
	})
	if compileErr != nil {
		return nil, compileErr
	}
	s, ok := compiled[skill]
	if !ok {
		return nil, fmt.Errorf("skills: unknown skill %q", skill)
	}
	return s, nil
}

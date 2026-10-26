package pipeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/amitpatole/renvoy/internal/pipeline"
)

// TestSchemaMatchesModel keeps the published JSON Schema in step with the Go model:
// every object definition must list exactly the fields the decoder accepts.
func TestSchemaMatchesModel(t *testing.T) {
	data, err := os.ReadFile("../../schemas/renvoy.v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	type node struct {
		Properties map[string]json.RawMessage `json:"properties"`
		OneOf      []node                     `json:"oneOf"`
	}
	var schema struct {
		node
		Defs map[string]node `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	keys := func(n node) []string {
		var k []string
		for name := range n.Properties {
			k = append(k, name)
		}
		slices.Sort(k)
		return k
	}
	cases := map[string]struct {
		node node
		typ  reflect.Type
	}{
		"(root)":               {schema.node, reflect.TypeOf(pipeline.Pipeline{})},
		"metadata":             {schema.Defs["metadata"], reflect.TypeOf(pipeline.Metadata{})},
		"triggers":             {schema.Defs["triggers"], reflect.TypeOf(pipeline.Triggers{})},
		"pushTrigger":          {schema.Defs["pushTrigger"], reflect.TypeOf(pipeline.PushTrigger{})},
		"pullRequestTrigger":   {schema.Defs["pullRequestTrigger"], reflect.TypeOf(pipeline.PullRequestTrigger{})},
		"manualTrigger":        {schema.Defs["manualTrigger"], reflect.TypeOf(pipeline.ManualTrigger{})},
		"schedule":             {schema.Defs["schedule"], reflect.TypeOf(pipeline.Schedule{})},
		"condition":            {schema.Defs["condition"], reflect.TypeOf(pipeline.Condition{})},
		"stage":                {schema.Defs["stage"], reflect.TypeOf(pipeline.Stage{})},
		"job":                  {schema.Defs["job"], reflect.TypeOf(pipeline.Job{})},
		"container":            {schema.Defs["container"], reflect.TypeOf(pipeline.Container{})},
		"environment":          {schema.Defs["environment"].OneOf[1], reflect.TypeOf(pipeline.Environment{})},
		"approvals":            {schema.Defs["approvals"], reflect.TypeOf(pipeline.Approvals{})},
		"step":                 {schema.Defs["step"], reflect.TypeOf(pipeline.Step{})},
		"cacheStep":            {schema.Defs["cacheStep"], reflect.TypeOf(pipeline.CacheStep{})},
		"uploadArtifactStep":   {schema.Defs["uploadArtifactStep"], reflect.TypeOf(pipeline.UploadArtifactStep{})},
		"downloadArtifactStep": {schema.Defs["downloadArtifactStep"], reflect.TypeOf(pipeline.DownloadArtifactStep{})},
		"testResultsStep":      {schema.Defs["testResultsStep"], reflect.TypeOf(pipeline.TestResultsStep{})},
		"buildImageStep":       {schema.Defs["buildImageStep"], reflect.TypeOf(pipeline.BuildImageStep{})},
		"pushImageStep":        {schema.Defs["pushImageStep"], reflect.TypeOf(pipeline.PushImageStep{})},
		"deployKubernetesStep": {schema.Defs["deployKubernetesStep"], reflect.TypeOf(pipeline.DeployKubernetesStep{})},
	}
	for name, c := range cases {
		want := pipeline.YAMLFieldNames(c.typ)
		slices.Sort(want)
		if got := keys(c.node); !slices.Equal(got, want) {
			t.Errorf("schema %s properties %v, model fields %v", name, got, want)
		}
	}
}

// TestExamplesAndGoldensAreValid makes sure every checked-in pipeline parses.
func TestExamplesAndGoldensAreValid(t *testing.T) {
	for _, pattern := range []string{"../../examples/*/renvoy.yaml", "../../testdata/golden/*/renvoy.yaml"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if p, _, diags := pipeline.Parse(data, f); p == nil {
				t.Errorf("%s does not parse: %v", f, diags)
			}
		}
	}
}

package bamboo

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/render"
)

// JavaName is the identifier of the Java Specs provider.
const JavaName = "bamboo-java"

const (
	defaultPackage   = "specs"
	defaultServerURL = "http://localhost:8085"
	conditionModule  = "com.atlassian.bamboo.plugins.bamboo-conditional-tasks:variableCondition"
)

// JavaProvider renders Bamboo Java Specs as a Maven project.
type JavaProvider struct{}

// NewJava returns the Bamboo Java Specs provider.
func NewJava() *JavaProvider { return &JavaProvider{} }

// Name implements providers.Provider.
func (*JavaProvider) Name() string { return JavaName }

// DisplayName implements providers.Provider.
func (*JavaProvider) DisplayName() string { return "Bamboo Java Specs" }

// Description implements providers.Provider.
func (*JavaProvider) Description() string {
	return "Atlassian Bamboo Java Specs Maven project (bamboo-specs/pom.xml)"
}

// Capabilities implements providers.Provider.
func (*JavaProvider) Capabilities() providers.CapabilitySet { return capabilities(true) }

// Validate implements providers.Provider.
func (pr *JavaProvider) Validate(p *pipeline.Pipeline) diagnostics.List { return validate(p, pr, true) }

// Render implements providers.Provider.
func (pr *JavaProvider) Render(_ context.Context, p *pipeline.Pipeline, _ providers.RenderOptions) ([]providers.GeneratedFile, error) {
	ext, diags := decode(p, JavaName)
	if diags.HasErrors() {
		return nil, fmt.Errorf("bamboo-java: invalid extensions")
	}
	pkg := ext.JavaPackage
	if pkg == "" {
		pkg = defaultPackage
	}
	version := ext.SpecsVersion
	if version == "" {
		version = DefaultSpecsVersion
	}
	server := ext.ServerURL
	if server == "" {
		server = defaultServerURL
	}
	pl := buildPlan(p, ext)
	dir := "bamboo-specs/src/%s/java/" + strings.ReplaceAll(pkg, ".", "/") + "/"
	return []providers.GeneratedFile{
		{Path: "bamboo-specs/pom.xml", Content: []byte(pom(pkg, p.Metadata.Name, version)),
			Comment: providers.CommentXML, CommentEnd: providers.CommentXMLEnd},
		{Path: fmt.Sprintf(dir, "main") + "PlanSpec.java", Content: []byte(planSpec(pl, pkg, server, ext.LinkedRepository)),
			Comment: providers.CommentSlash},
		{Path: fmt.Sprintf(dir, "test") + "PlanSpecTest.java", Content: []byte(planSpecTest(pkg)),
			Comment: providers.CommentSlash},
	}, nil
}

func pom(pkg, name, version string) string {
	return `<project xmlns="http://maven.apache.org/POM/4.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 http://maven.apache.org/maven-v4_0_0.xsd">
  <modelVersion>4.0.0</modelVersion>

  <parent>
    <groupId>com.atlassian.bamboo</groupId>
    <artifactId>bamboo-specs-parent</artifactId>
    <version>` + version + `</version>
    <relativePath/>
  </parent>

  <groupId>` + pkg + `</groupId>
  <artifactId>` + name + `-bamboo-specs</artifactId>
  <version>1.0.0</version>
  <packaging>jar</packaging>

  <properties>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>

  <dependencies>
    <dependency>
      <groupId>com.atlassian.bamboo</groupId>
      <artifactId>bamboo-specs-api</artifactId>
    </dependency>
    <dependency>
      <groupId>com.atlassian.bamboo</groupId>
      <artifactId>bamboo-specs</artifactId>
    </dependency>
    <dependency>
      <groupId>junit</groupId>
      <artifactId>junit</artifactId>
      <scope>test</scope>
    </dependency>
    <!-- renvoy:begin custom -->
    <!-- Provider-specific content retained during regeneration. -->
    <!-- renvoy:end custom -->
  </dependencies>

  <!-- mvn test validates the plan offline; mvn -Ppublish-specs publishes it. -->
</project>
`
}

func planSpecTest(pkg string) string {
	return `package ` + pkg + `;

import com.atlassian.bamboo.specs.api.util.EntityPropertiesBuilders;
import org.junit.Test;

public class PlanSpecTest {
    @Test
    public void checkYourPlanOffline() {
        EntityPropertiesBuilders.build(new PlanSpec().createPlan());
    }
}
`
}

// code is a block of Java source lines with relative indentation.
type code []string

const step = "        "

func indent(c code) code {
	out := make(code, len(c))
	for i, l := range c {
		out[i] = step + l
	}
	return out
}

// method renders .name(args...), one argument per line when there are several.
func method(name string, items ...code) code {
	if len(items) == 1 && len(items[0]) == 1 {
		return code{"." + name + "(" + items[0][0] + ")"}
	}
	var args code
	for i, it := range items {
		it = append(code{}, it...)
		if i < len(items)-1 {
			it[len(it)-1] += ","
		}
		args = append(args, it...)
	}
	args[len(args)-1] += ")"
	return append(code{"." + name + "("}, indent(args)...)
}

// chain renders a receiver followed by indented method calls.
func chain(head string, methods ...code) code {
	out := code{head}
	for _, m := range methods {
		out = append(out, indent(m)...)
	}
	return out
}

type javaWriter struct{ imports map[string]bool }

func (w *javaWriter) use(classes ...string) {
	for _, c := range classes {
		w.imports[c] = true
	}
}

// jstr renders a Java string literal. Non-ASCII and control characters become
// \u escapes (surrogate pairs above U+FFFF) so the source is plain ASCII.
func jstr(s string) string {
	var chunks []string
	var b strings.Builder
	size := 0
	b.WriteByte('"')
	for _, r := range s {
		// A string constant holds at most 65535 bytes of modified UTF-8, so
		// long strings are split into chunks joined at run time.
		n := modifiedUTF8Len(r)
		if size+n > maxJavaChunk {
			b.WriteByte('"')
			chunks = append(chunks, b.String())
			b.Reset()
			b.WriteByte('"')
			size = 0
		}
		size += n
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10)&0x3ff, 0xdc00+r&0x3ff)
		case r < 0x20 || r >= 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	if chunks == nil {
		return b.String()
	}
	return "String.join(\"\", " + strings.Join(append(chunks, b.String()), ", ") + ")"
}

// maxJavaChunk keeps each string constant well under the 65535-byte limit.
const maxJavaChunk = 60000

// modifiedUTF8Len is the size of r in a Java class file's constant pool.
func modifiedUTF8Len(r rune) int {
	switch {
	case r == 0:
		return 2
	case r < 0x80:
		return 1
	case r < 0x800:
		return 2
	case r <= 0xffff:
		return 3
	}
	return 6
}

func (w *javaWriter) key(k string) string {
	w.use("com.atlassian.bamboo.specs.api.builders.BambooKey")
	return "new BambooKey(" + jstr(k) + ")"
}

func (w *javaWriter) body(s string) code {
	lines := strings.Split(s, "\n")
	if len(lines) == 1 {
		return code{jstr(s)}
	}
	out := code{"String.join(\"\\n\","}
	for i, l := range lines {
		line := step + jstr(l)
		if i < len(lines)-1 {
			line += ","
		} else {
			line += ")"
		}
		out = append(out, line)
	}
	return out
}

func (w *javaWriter) task(t task) code {
	var c code
	switch t.Kind {
	case taskCheckout:
		w.use("com.atlassian.bamboo.specs.builders.task.VcsCheckoutTask")
		return code{"new VcsCheckoutTask().addCheckoutOfDefaultRepository()"}
	case taskJUnit:
		w.use("com.atlassian.bamboo.specs.builders.task.TestParserTask")
		c = chain("TestParserTask.createJUnitParserTask()", method("resultDirectories", code{jstr(t.Results)}))
	default:
		w.use("com.atlassian.bamboo.specs.builders.task.ScriptTask")
		var ms []code
		if t.Description != "" {
			ms = append(ms, method("description", code{jstr(t.Description)}))
		}
		if t.PowerShell {
			ms = append(ms, code{".interpreterWindowsPowerShell()"})
		} else {
			ms = append(ms, code{".interpreterShell()"})
		}
		ms = append(ms, method("inlineBody", w.body(t.Body)))
		if t.Env != "" {
			ms = append(ms, method("environmentVariables", code{jstr(t.Env)}))
		}
		if t.WorkDir != "" {
			ms = append(ms, method("workingSubdirectory", code{jstr(t.WorkDir)}))
		}
		c = chain("new ScriptTask()", ms...)
	}
	if len(t.Branches) > 0 {
		var conds []code
		for _, b := range t.Branches {
			conds = append(conds, code{"branchIs(" + jstr(b) + ")"})
		}
		c = append(c, indent(method("conditions", conds...))...)
	}
	return c
}

func (w *javaWriter) job(j *job) code {
	w.use("com.atlassian.bamboo.specs.api.builders.plan.Job")
	var ms []code
	if j.Image != "" {
		w.use("com.atlassian.bamboo.specs.api.builders.docker.DockerConfiguration")
		docker := "new DockerConfiguration().image(" + jstr(j.Image) + ")"
		if j.RunArgs != "" {
			docker += ".dockerRunArguments(" + jstr(j.RunArgs) + ")"
		}
		ms = append(ms, method("dockerConfiguration", code{docker}))
	}
	if len(j.Requirements) > 0 {
		w.use("com.atlassian.bamboo.specs.api.builders.requirement.Requirement")
		var reqs []code
		for _, r := range j.Requirements {
			if r.Match == "" {
				reqs = append(reqs, code{"Requirement.exists(" + jstr(r.Key) + ")"})
			} else {
				reqs = append(reqs, code{"Requirement.matches(" + jstr(r.Key) + ", " + jstr(r.Match) + ")"})
			}
		}
		ms = append(ms, method("requirements", reqs...))
	}
	var tasks []code
	for _, t := range j.Tasks {
		tasks = append(tasks, w.task(t))
	}
	if len(tasks) > 0 {
		ms = append(ms, method("tasks", tasks...))
	}
	var final []code
	for _, t := range j.FinalTasks {
		final = append(final, w.task(t))
	}
	if len(final) > 0 {
		ms = append(ms, method("finalTasks", final...))
	}
	if len(j.Artifacts) > 0 {
		w.use("com.atlassian.bamboo.specs.api.builders.plan.artifact.Artifact")
		var arts []code
		for _, a := range j.Artifacts {
			arts = append(arts, chain("new Artifact("+jstr(a.Name)+")",
				method("location", code{jstr(a.Location)}),
				method("copyPattern", code{jstr(a.Pattern)}),
				code{".shared(true)"},
				code{".required(true)"}))
		}
		ms = append(ms, method("artifacts", arts...))
	}
	if len(j.Subscriptions) > 0 {
		w.use("com.atlassian.bamboo.specs.api.builders.plan.artifact.ArtifactSubscription")
		var subs []code
		for _, s := range j.Subscriptions {
			subs = append(subs, code{"new ArtifactSubscription().artifact(" + jstr(s.Name) + ").destination(" + jstr(s.Location) + ")"})
		}
		ms = append(ms, method("artifactSubscriptions", subs...))
	}
	return chain("new Job("+jstr(j.Name)+", "+w.key(j.Key)+")", ms...)
}

func planSpec(pl *plan, pkg, server, repo string) string {
	w := &javaWriter{imports: map[string]bool{}}
	w.use("com.atlassian.bamboo.specs.api.BambooSpec",
		"com.atlassian.bamboo.specs.api.builders.plan.Plan",
		"com.atlassian.bamboo.specs.api.builders.project.Project",
		"com.atlassian.bamboo.specs.util.BambooServer")

	var ms []code
	if pl.Description != "" {
		ms = append(ms, method("description", code{jstr(pl.Description)}))
	}
	if repo != "" {
		ms = append(ms, method("linkedRepositories", code{jstr(repo)}))
	}
	if len(pl.Variables) > 0 {
		w.use("com.atlassian.bamboo.specs.api.builders.Variable")
		var vars []code
		for _, v := range pl.Variables {
			vars = append(vars, code{"new Variable(" + jstr(v.Name) + ", " + jstr(v.Value) + ")"})
		}
		ms = append(ms, method("variables", vars...))
	}
	var triggers []code
	if pl.PollRepository {
		w.use("com.atlassian.bamboo.specs.builders.trigger.RepositoryPollingTrigger")
		triggers = append(triggers, code{"new RepositoryPollingTrigger()"})
	}
	for _, c := range pl.Crons {
		w.use("com.atlassian.bamboo.specs.builders.trigger.ScheduledTrigger")
		triggers = append(triggers, code{"new ScheduledTrigger().cronExpression(" + jstr(c) + ")"})
	}
	if len(triggers) > 0 {
		ms = append(ms, method("triggers", triggers...))
	}
	if pl.BranchCreate != "" {
		w.use("com.atlassian.bamboo.specs.api.builders.plan.branches.PlanBranchManagement")
		create := code{".createForPullRequest()"}
		switch {
		case pl.BranchCreate == "for-new-branch" && pl.BranchRegex == allBranches:
			create = code{".createForVcsBranch()"}
		case pl.BranchCreate == "for-new-branch":
			create = code{".createForVcsBranchMatching(" + jstr(pl.BranchRegex) + ")"}
		}
		ms = append(ms, method("planBranchManagement",
			chain("new PlanBranchManagement()", create, code{".triggerBuildsLikeParentPlan()"})))
	}
	var stages []code
	w.use("com.atlassian.bamboo.specs.api.builders.plan.Stage")
	usesConditions := false
	for _, s := range pl.Stages {
		var sm []code
		if s.Manual {
			sm = append(sm, code{".manual(true)"})
		}
		var jobs []code
		for _, j := range s.Jobs {
			jobs = append(jobs, w.job(j))
			for _, t := range append(append([]task{}, j.Tasks...), j.FinalTasks...) {
				usesConditions = usesConditions || len(t.Branches) > 0
			}
		}
		sm = append(sm, method("jobs", jobs...))
		stages = append(stages, chain("new Stage("+jstr(s.Name)+")", sm...))
	}
	ms = append(ms, method("stages", stages...))

	head := "new Plan(new Project().key(" + w.key(pl.ProjectKey) + "), " + jstr(pl.Name) + ", " + w.key(pl.PlanKey) + ")"
	planCode := chain(head, ms...)
	planCode[len(planCode)-1] += ";"

	if usesConditions {
		w.use("java.util.Map",
			"com.atlassian.bamboo.specs.api.builders.AtlassianModule",
			"com.atlassian.bamboo.specs.api.builders.condition.AnyTaskCondition")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s;\n\n", pkg)
	var imports []string
	for imp := range w.imports {
		imports = append(imports, imp)
	}
	slices.Sort(imports)
	for _, imp := range imports {
		fmt.Fprintf(&b, "import %s;\n", imp)
	}
	b.WriteString("\n@BambooSpec\npublic class PlanSpec {\n\n")
	b.WriteString("    public static void main(String[] args) throws Exception {\n")
	fmt.Fprintf(&b, "        BambooServer bambooServer = new BambooServer(%s);\n", jstr(server))
	b.WriteString("        bambooServer.publish(new PlanSpec().createPlan());\n    }\n\n")
	b.WriteString("    Plan createPlan() {\n")
	b.WriteString("        Plan plan = " + planCode[0] + "\n")
	for _, l := range planCode[1:] {
		b.WriteString("        " + l + "\n")
	}
	b.WriteString("        return customize(plan);\n    }\n")
	if usesConditions {
		b.WriteString("\n    private static AnyTaskCondition branchIs(String branch) {\n")
		fmt.Fprintf(&b, "        return new AnyTaskCondition(new AtlassianModule(%s))\n", jstr(conditionModule))
		fmt.Fprintf(&b, "                .configuration(Map.of(\"variable\", %s, \"operation\", \"equals\", \"value\", branch));\n", jstr(branchVariable))
		b.WriteString("    }\n")
	}
	b.WriteString("\n")
	b.WriteString(render.CustomBlockWith(providers.CommentSlash, "    ",
		"    Plan customize(Plan plan) {",
		"        return plan;",
		"    }"))
	b.WriteString("}\n")
	return b.String()
}

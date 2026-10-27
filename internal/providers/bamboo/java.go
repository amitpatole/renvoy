package bamboo

import (
	"context"
	"fmt"
	"regexp"
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
func (pr *JavaProvider) Validate(p *pipeline.Pipeline) diagnostics.List {
	diags := validate(p, pr, true)
	if diags.HasErrors() {
		return diags
	}
	ext, _ := decode(p, JavaName)
	_, sizes := planSpec(buildPlan(p, ext), defaultPackage, defaultServerURL, ext.LinkedRepository)
	for _, c := range sizes {
		if c.constants <= maxJavaConstants && c.jobCode <= maxJavaCode {
			continue
		}
		path := "stages"
		for _, j := range p.AllJobs() {
			if j.Name == c.job {
				path = j.Path()
			}
		}
		diags = append(diags, diagnostics.Errorf(path, "job %q is too large for Bamboo Java Specs, whose class files hold at most 65,535 constants and 64 KB of code per method", c.job).
			WithHint("split the job, or use the bamboo provider (YAML Specs)").WithProvider(JavaName))
	}
	return diags
}

// Limits with headroom below the class file maximums of 65,535.
const (
	maxJavaConstants = 60000
	maxJavaCode      = 50000
)

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
	spec, _ := planSpec(pl, pkg, server, ext.LinkedRepository)
	dir := "bamboo-specs/src/%s/java/" + strings.ReplaceAll(pkg, ".", "/") + "/"
	return []providers.GeneratedFile{
		{Path: "bamboo-specs/pom.xml", Content: []byte(pom(pkg, p.Metadata.Name, version)),
			Comment: providers.CommentXML, CommentEnd: providers.CommentXMLEnd},
		{Path: fmt.Sprintf(dir, "main") + "PlanSpec.java", Content: []byte(spec),
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

type javaWriter struct {
	imports map[string]bool
	classes []classSize
}

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
	// Each line costs bytecode, so long scripts are one (chunked) literal.
	if len(lines) == 1 || len(lines) > maxScriptLines {
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

// maxScriptLines is the longest script rendered one line per argument.
const maxScriptLines = 1000

// task renders a task builder and returns it with its Java type.
func (w *javaWriter) task(t task) (code, string) {
	var c code
	typ := "ScriptTask"
	switch t.Kind {
	case taskCheckout:
		w.use("com.atlassian.bamboo.specs.builders.task.VcsCheckoutTask")
		return code{"new VcsCheckoutTask().addCheckoutOfDefaultRepository()"}, "VcsCheckoutTask"
	case taskJUnit:
		w.use("com.atlassian.bamboo.specs.builders.task.TestParserTask")
		typ = "TestParserTask"
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
	return c, typ
}

// jobClass renders a job as a nested class: job() builds it and each task has
// its own method. A class has its own constant pool and each method its own
// 64 KB of bytecode, so a plan's size is limited per job and per task rather
// than as a whole.
func (w *javaWriter) jobClass(j *job, class string) code {
	var methods []code
	var tasks, final []code
	add := func(t task) code {
		body, typ := w.task(t)
		name := fmt.Sprintf("task%d", len(methods)+1)
		methods = append(methods, javaMethod("static "+typ+" "+name+"()", body))
		return code{name + "()"}
	}
	for _, t := range j.Tasks {
		tasks = append(tasks, add(t))
	}
	for _, t := range j.FinalTasks {
		final = append(final, add(t))
	}
	// A call with thousands of arguments exceeds 64 KB of bytecode, so long
	// task lists are built in groups; Job.tasks and finalTasks append.
	var groups []code
	group := func(prefix string, calls []code) []code {
		if len(calls) <= maxTaskArgs {
			return calls
		}
		w.use("com.atlassian.bamboo.specs.api.builders.task.Task")
		var refs []code
		for start := 0; start < len(calls); start += maxTaskArgs {
			name := fmt.Sprintf("%s%d", prefix, len(refs)+1)
			elems := code{"new Task<?, ?>[] {"}
			for _, c := range calls[start:min(start+maxTaskArgs, len(calls))] {
				elems = append(elems, step+c[0]+",")
			}
			elems = append(elems, "}")
			groups = append(groups, javaMethod("static Task<?, ?>[] "+name+"()", elems))
			refs = append(refs, code{name + "()"})
		}
		return refs
	}
	tasks, final = group("tasks", tasks), group("finalTasks", final)
	out := code{"static final class " + class + " {"}
	out = append(out, javaMethod("static Job job()", w.job(j, tasks, final))...)
	for _, m := range append(groups, methods...) {
		out = append(out, "")
		out = append(out, m...)
	}
	out = append(out, "}")
	w.classes = append(w.classes, classSize{job: j.Name, constants: constantEstimate(out, len(methods)+len(groups)+1),
		jobCode: 200 + 8*(len(tasks)+len(final)) + 12*len(j.Requirements) + 48*len(j.Artifacts) + 24*len(j.Subscriptions)})
	return out
}

// maxTaskArgs is the most tasks passed to one Job.tasks call.
const maxTaskArgs = 1000

// classSize estimates how close a job class is to the class file limits.
type classSize struct {
	job       string
	constants int // constant pool entries; the limit is 65535
	jobCode   int // bytecode bytes of job(); the limit is 65535
}

var javaLiteralRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// constantEstimate over-counts the constant pool entries of a class: two per
// distinct string literal and three per method, plus the classes and methods
// of the Bamboo Specs API it uses.
func constantEstimate(src code, methods int) int {
	literals := map[string]bool{}
	for _, l := range src {
		for _, lit := range javaLiteralRe.FindAllString(l, -1) {
			literals[lit] = true
		}
	}
	return 1000 + 2*len(literals) + 3*methods
}

// javaMethod renders a method returning expr, indented one level.
func javaMethod(signature string, expr code) code {
	out := code{"    " + signature + " {", "        return " + expr[0]}
	for _, l := range expr[1:] {
		out = append(out, "        "+l)
	}
	out[len(out)-1] += ";"
	return append(out, "    }")
}

func (w *javaWriter) job(j *job, tasks, final []code) code {
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
	// Grouped task lists pass one array per call.
	calls := func(name string, args []code, grouped bool) {
		switch {
		case len(args) == 0:
		case grouped:
			for _, a := range args {
				ms = append(ms, method(name, a))
			}
		default:
			ms = append(ms, method(name, args...))
		}
	}
	calls("tasks", tasks, len(j.Tasks) > maxTaskArgs)
	calls("finalTasks", final, len(j.FinalTasks) > maxTaskArgs)
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

func planSpec(pl *plan, pkg, server, repo string) (string, []classSize) {
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
	w.use("com.atlassian.bamboo.specs.api.builders.plan.branches.PlanBranchManagement")
	if pl.BranchCreate == "" {
		ms = append(ms, method("planBranchManagement", code{"new PlanBranchManagement().createManually()"}))
	} else {
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
	var stages, classes []code
	w.use("com.atlassian.bamboo.specs.api.builders.plan.Stage")
	usesConditions := false
	for _, s := range pl.Stages {
		var sm []code
		if s.Manual {
			sm = append(sm, code{".manual(true)"})
		}
		var jobs []code
		for _, j := range s.Jobs {
			class := fmt.Sprintf("J%d", len(classes)+1)
			classes = append(classes, w.jobClass(j, class))
			jobs = append(jobs, code{class + ".job()"})
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
	for _, c := range classes {
		b.WriteString("\n")
		for _, l := range c {
			if l == "" {
				b.WriteString("\n")
			} else {
				b.WriteString("    " + l + "\n")
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(render.CustomBlockWith(providers.CommentSlash, "    ",
		"    Plan customize(Plan plan) {",
		"        return plan;",
		"    }"))
	b.WriteString("}\n")
	return b.String(), w.classes
}

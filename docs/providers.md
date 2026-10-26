# Providers

Renvoy compiles one pipeline into native files for each provider. Every provider
declares how it supports each portable concept, and `renvoy generate` refuses to
write anything when a pipeline uses a concept a selected provider cannot express.

```bash
renvoy providers                                   # list providers
renvoy capabilities --provider gitlab              # one provider's matrix
renvoy capabilities --provider gitlab --format json
```

## Outputs

| Provider | Name | Files |
| --- | --- | --- |
| GitHub Actions | `github-actions` | `.github/workflows/<name>.yml` |
| GitLab CI | `gitlab` | `.gitlab-ci.yml` |
| Azure DevOps Pipelines | `azure-devops` | `azure-pipelines.yml` |
| Jenkins Declarative Pipeline | `jenkins` | `Jenkinsfile` |
| Bamboo YAML Specs | `bamboo` | `bamboo-specs/bamboo.yaml` |
| Bamboo Java Specs | `bamboo-java` | `bamboo-specs/pom.xml`, `bamboo-specs/src/main/java/<package>/PlanSpec.java`, `bamboo-specs/src/test/java/<package>/PlanSpecTest.java` |

`bamboo` and `bamboo-java` write to the same `bamboo-specs/` directory. Bamboo
repository-stored Specs read `bamboo-specs/bamboo.yaml` for YAML and
`bamboo-specs/pom.xml` for Java; generate the format your Bamboo server should
scan, or use `--output` to put the other one somewhere else.

## Support levels

| Level | Meaning |
| --- | --- |
| `native` | The provider has a direct equivalent. |
| `emulated` | Renvoy renders the same behavior with other constructs; the note says how. |
| `provider-managed` | The behavior exists but is configured in the provider, not in the file. Renvoy reports an `info` diagnostic. |
| `unsupported` | The provider cannot express it. Using it is an `error` for that provider. |

## Capability matrix

This section is generated from the provider code by
`go test ./internal/providers -run TestProvidersDocMatrix -update`; CI fails when it is stale.

<!-- capability-matrix:begin -->
| Capability | github-actions | gitlab | azure-devops | jenkins | bamboo | bamboo-java |
| --- | --- | --- | --- | --- | --- | --- |
| `triggers.push` | native | native | native | provider-managed | native | native |
| `triggers.tags` | native | native | native | provider-managed | unsupported | unsupported |
| `triggers.pullRequest` | native | native | native | provider-managed | native | native |
| `triggers.manual` | native | native | native | native | native | native |
| `triggers.schedules` | native | provider-managed | native | native | native | native |
| `triggers.branchFilters` | native | native | native | provider-managed | native | native |
| `stages.dependsOn` | native | native | native | emulated | emulated | emulated |
| `jobs.dependsOn` | native | native | native | emulated | emulated | emulated |
| `jobs.when` | native | native | native | native | emulated | emulated |
| `steps.when` | native | unsupported | native | unsupported | emulated | emulated |
| `runners.linux` | native | native | native | native | native | native |
| `runners.windows` | native | native | native | native | native | native |
| `runners.macos` | native | native | native | native | native | native |
| `jobs.container` | native | native | native | native | native | native |
| `jobs.container.options` | native | unsupported | native | native | native | native |
| `jobs.matrix` | native | native | native | native | unsupported | unsupported |
| `jobs.timeoutMinutes` | native | native | native | native | unsupported | unsupported |
| `jobs.retries` | unsupported | native | unsupported | native | unsupported | unsupported |
| `variables` | native | native | native | native | native | native |
| `secrets` | native | native | native | native | provider-managed | provider-managed |
| `steps.env` | native | emulated | native | native | native | native |
| `steps.workingDirectory` | native | emulated | native | native | native | native |
| `steps.checkout` | native | emulated | native | emulated | emulated | native |
| `steps.uses` | native | unsupported | native | unsupported | unsupported | unsupported |
| `steps.cache` | native | emulated | native | unsupported | unsupported | unsupported |
| `steps.uploadArtifact` | native | emulated | native | emulated | native | native |
| `steps.downloadArtifact` | native | emulated | native | emulated | native | native |
| `steps.publishTestResults` | emulated | native | native | native | native | native |
| `steps.buildImage` | emulated | emulated | emulated | emulated | emulated | emulated |
| `steps.pushImage` | emulated | emulated | emulated | emulated | emulated | emulated |
| `steps.deployKubernetes` | emulated | emulated | emulated | emulated | emulated | emulated |
| `jobs.environment` | native | native | native | emulated | emulated | emulated |
| `jobs.environment.approvals` | provider-managed | provider-managed | provider-managed | emulated | emulated | emulated |

### GitHub Actions notes

- `triggers.schedules` (native): scheduled workflows run on the repository's default branch only
- `jobs.retries` (unsupported): GitHub Actions cannot retry jobs; retry inside the step script or remove retries
- `steps.uses` (native): references must be GitHub actions: owner/repo@ref, owner/repo/path@ref, ./path, or docker://image
- `steps.publishTestResults` (emulated): uploaded as an artifact named test-results-<job>; GitHub has no built-in test report view
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment.approvals` (provider-managed): add required reviewers to the environment in the repository settings

### GitLab CI notes

- `triggers.schedules` (provider-managed): create a pipeline schedule (timezone UTC) for each listed branch under Build > Pipeline schedules; the generated workflow rules accept scheduled pipelines
- `steps.when` (unsupported): GitLab has no step-level conditions; move the step into a job with a when condition
- `runners.windows` (native): defaults to GitLab-hosted runner tags; override with extensions.gitlab.runnerTags
- `runners.macos` (native): defaults to GitLab-hosted runner tags; override with extensions.gitlab.runnerTags
- `jobs.container.options` (unsupported): GitLab cannot pass docker run options from the pipeline; configure them on the runner
- `jobs.retries` (native): GitLab retries a job at most twice
- `steps.env` (emulated): rendered as a subshell that exports the variables (bash and sh only)
- `steps.workingDirectory` (emulated): rendered as a subshell that changes directory (bash and sh only)
- `steps.checkout` (emulated): GitLab always clones before the script; jobs without checkout set GIT_STRATEGY: none
- `steps.uses` (unsupported): GitLab has no reusable step references; use run steps
- `steps.cache` (emulated): job-level cache, restored before the script and saved when the job succeeds
- `steps.uploadArtifact` (emulated): job artifacts, uploaded after the script finishes
- `steps.downloadArtifact` (emulated): downloaded through needs with artifacts: true
- `steps.publishTestResults` (native): artifacts:reports:junit, uploaded even when the job fails
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment.approvals` (provider-managed): protect the environment and require approvals under Operate > Environments

### Azure DevOps Pipelines notes

- `triggers.pullRequest` (native): pr triggers apply to GitHub and Bitbucket repositories; Azure Repos uses branch policies
- `jobs.retries` (unsupported): Azure Pipelines cannot retry jobs; retry inside the step script or remove retries
- `steps.uses` (native): references must be Azure tasks: Name@MajorVersion
- `steps.cache` (native): Cache@2 task; one path per cache step
- `steps.uploadArtifact` (native): publish step; one path per artifact
- `steps.downloadArtifact` (native): DownloadPipelineArtifact@2 into the sources directory
- `steps.publishTestResults` (native): PublishTestResults@2, run even when earlier steps fail
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment` (native): jobs with an environment become deployment jobs
- `jobs.environment.approvals` (provider-managed): add an Approvals check to the environment under Pipelines > Environments

### Jenkins Declarative Pipeline notes

- `triggers.push` (provider-managed): multibranch pipelines build pushed branches and tags through SCM webhooks or indexing
- `triggers.tags` (provider-managed): enable tag discovery on the multibranch pipeline
- `triggers.pullRequest` (provider-managed): enable pull request discovery on the multibranch pipeline
- `triggers.manual` (native): Build Now is always available
- `triggers.schedules` (native): triggers { cron } guarded by branch, in UTC
- `triggers.branchFilters` (provider-managed): restrict branch discovery in the multibranch pipeline's branch sources
- `stages.dependsOn` (emulated): stages run one after another in dependency order
- `jobs.dependsOn` (emulated): jobs with dependencies run one after another; independent jobs run in parallel
- `steps.when` (unsupported): Declarative Pipeline has no step-level conditions; move the step into a job with a when condition
- `runners.windows` (native): agent label 'windows'; scripts use the pwsh step (PowerShell plugin)
- `runners.macos` (native): agent label 'macos'
- `jobs.container` (native): docker agent (Docker Pipeline plugin)
- `secrets` (native): credentials('NAME'); create secret text credentials whose IDs match the secret names
- `steps.checkout` (emulated): default checkout at the start of the stage; jobs without checkout skip it
- `steps.uses` (unsupported): Renvoy does not invent plugin steps; use run steps
- `steps.cache` (unsupported): Jenkins core has no cache step; remove the cache step
- `steps.uploadArtifact` (emulated): archiveArtifacts plus stash for downstream jobs
- `steps.downloadArtifact` (emulated): unstash
- `steps.publishTestResults` (native): post { always { junit } }
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment` (emulated): Jenkins has no deployment environments; the name is exported as RENVOY_ENVIRONMENT
- `jobs.environment.approvals` (emulated): requires extensions.jenkins.input on the job, rendered as a stage input

### Bamboo YAML Specs notes

- `triggers.push` (native): repository trigger; branches other than the plan branch become plan branches
- `triggers.tags` (unsupported): Bamboo plans do not trigger on tags; use a separate plan or deployment project
- `triggers.pullRequest` (native): plan branches created for pull requests
- `triggers.schedules` (native): cron trigger converted to Quartz syntax; runs in the Bamboo server time zone
- `stages.dependsOn` (emulated): Bamboo stages run one after another in dependency order
- `jobs.dependsOn` (emulated): dependent jobs are placed in later Bamboo stages
- `jobs.when` (emulated): task conditions on planRepository.branch; exact branch names only
- `steps.when` (emulated): task conditions on planRepository.branch; exact branch names only
- `runners.windows` (native): requires extensions.bamboo.requirements.windows; scripts use Windows PowerShell
- `runners.macos` (native): requires extensions.bamboo.requirements.macos
- `jobs.container` (native): job Docker configuration
- `jobs.container.options` (native): docker run arguments
- `jobs.matrix` (unsupported): Bamboo has no job matrix; declare one job per combination
- `jobs.timeoutMinutes` (unsupported): Bamboo configures hung build detection per plan, not per job
- `jobs.retries` (unsupported): Bamboo cannot retry jobs from Specs; retry inside the step script
- `secrets` (provider-managed): create Bamboo variables (encrypted) with the secret names; scripts read them as $bamboo_NAME
- `steps.env` (native): script task environment variables
- `steps.workingDirectory` (native): script task working subdirectory
- `steps.checkout` (emulated): YAML Specs check out the repository first in jobs without a checkout task
- `steps.uses` (unsupported): Renvoy does not invent Bamboo plugin task keys; use run steps
- `steps.cache` (unsupported): Bamboo has no cache task; remove the cache step
- `steps.uploadArtifact` (native): shared artifact; one path per artifact
- `steps.downloadArtifact` (native): artifact subscription
- `steps.publishTestResults` (native): JUnit test parser final task
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment` (emulated): plan jobs have no deployment environment; the name is exported as RENVOY_ENVIRONMENT
- `jobs.environment.approvals` (emulated): the job runs in a manual stage that a user must start

### Bamboo Java Specs notes

- `triggers.push` (native): repository trigger; branches other than the plan branch become plan branches
- `triggers.tags` (unsupported): Bamboo plans do not trigger on tags; use a separate plan or deployment project
- `triggers.pullRequest` (native): plan branches created for pull requests
- `triggers.schedules` (native): cron trigger converted to Quartz syntax; runs in the Bamboo server time zone
- `stages.dependsOn` (emulated): Bamboo stages run one after another in dependency order
- `jobs.dependsOn` (emulated): dependent jobs are placed in later Bamboo stages
- `jobs.when` (emulated): task conditions on planRepository.branch; exact branch names only
- `steps.when` (emulated): task conditions on planRepository.branch; exact branch names only
- `runners.windows` (native): requires extensions.bamboo.requirements.windows; scripts use Windows PowerShell
- `runners.macos` (native): requires extensions.bamboo.requirements.macos
- `jobs.container` (native): job Docker configuration
- `jobs.container.options` (native): docker run arguments
- `jobs.matrix` (unsupported): Bamboo has no job matrix; declare one job per combination
- `jobs.timeoutMinutes` (unsupported): Bamboo configures hung build detection per plan, not per job
- `jobs.retries` (unsupported): Bamboo cannot retry jobs from Specs; retry inside the step script
- `secrets` (provider-managed): create Bamboo variables (encrypted) with the secret names; scripts read them as $bamboo_NAME
- `steps.env` (native): script task environment variables
- `steps.workingDirectory` (native): script task working subdirectory
- `steps.checkout` (native): VcsCheckoutTask
- `steps.uses` (unsupported): Renvoy does not invent Bamboo plugin task keys; use run steps
- `steps.cache` (unsupported): Bamboo has no cache task; remove the cache step
- `steps.uploadArtifact` (native): shared artifact; one path per artifact
- `steps.downloadArtifact` (native): artifact subscription
- `steps.publishTestResults` (native): JUnit test parser final task
- `steps.buildImage` (emulated): runs the docker CLI
- `steps.pushImage` (emulated): runs the docker CLI
- `steps.deployKubernetes` (emulated): runs kubectl apply
- `jobs.environment` (emulated): plan jobs have no deployment environment; the name is exported as RENVOY_ENVIRONMENT
- `jobs.environment.approvals` (emulated): the job runs in a manual stage that a user must start
<!-- capability-matrix:end -->

## Semantic differences Renvoy handles

These are the places where providers behave differently by default and the
adapters do extra work so the portable meaning holds.

**Skipped jobs skip their dependents.** GitHub Actions does this natively. GitLab
CI and Jenkins run a job even when an upstream job was skipped by its condition,
so their adapters render each job's condition together with the conditions of all
upstream jobs. Azure DevOps treats a stage as succeeded when one of its jobs was
skipped, so Azure jobs carry the conditions of upstream jobs in other stages.
Bamboo applies inherited conditions to every task.

**Scripts stop at the first failing command.** Bash steps run with `-eo pipefail`
everywhere: GitHub through `defaults.run.shell: bash`, Azure and Jenkins by
prepending `set -eo pipefail`, Bamboo with a shebang plus `set -eo pipefail`.
GitLab's bash runners already behave this way.

**References never become script text.** `${secrets.X}`, `${git.branch}`, and
matrix values are passed to scripts as environment variables, never pasted into
the script. This keeps attacker-controlled values such as pull request branch
names out of GitHub `${{ }}` and Azure `$( )` expansion inside scripts.

**Artifacts are restored at their original paths.** GitHub and Azure root an
artifact at the common parent of its paths, so their download steps target that
directory. Directories in `uploadArtifact.paths` must end with `/`.

**Jobs only receive the artifacts they download.** GitLab `needs` entries set
`artifacts: false` unless the job downloads from that job; Azure deployment jobs
start with `download: none`; Bamboo YAML jobs set `artifact-subscriptions: []`.

**Checkout happens only where you ask.** Azure jobs without a checkout step get
`checkout: none`, GitLab jobs get `GIT_STRATEGY: none`, and Jenkins jobs get
`skipDefaultCheckout()`. GitLab, Jenkins, and Bamboo YAML Specs check out at the
start of the job, so a checkout step that is not first produces a warning.

**Schedules run even without new commits.** Azure schedules set `always: true`.
Jenkins cron triggers use `TZ=UTC` and only fire on the listed branches. Bamboo
cron expressions are converted to Quartz syntax.

**Test results are published when tests fail.** GitHub uses `if: !cancelled()`,
GitLab `artifacts:when: always`, Azure `succeededOrFailed()`, Jenkins
`post { always { junit } }`, and Bamboo a final task.

## Differences Renvoy reports

- **Ordering on Jenkins and Bamboo.** Both run stages one after another. Stages
  that could run at the same time, and jobs that depend on each other inside a
  stage, are serialized in dependency order. Renvoy reports an `info` diagnostic.
- **Environments without deployments.** Jenkins and Bamboo plan jobs have no
  deployment environment; the name is exported as `RENVOY_ENVIRONMENT` and Renvoy
  warns. Azure drops `environment.url` with a warning.
- **Approvals.** GitHub, GitLab, and Azure configure approvals on the environment
  in the provider (`info`). Jenkins needs `extensions.jenkins.input` on the job and
  is an error without it. Bamboo puts the job in a manual stage.
- **Provider-native `${{ }}` and `$( )` text.** GitHub evaluates `${{ }}` and Azure
  evaluates `$( )` macros anywhere in the generated file. Renvoy warns when a run
  script contains `${{`; do not put `$(name)` in values meant for Azure.
- **Shells.** GitLab runs every script in the runner's shell, so `shell: pwsh` on a
  Linux job is an error there. Bamboo on Windows uses Windows PowerShell 5.1.
- **References in Bamboo environment variables.** Bamboo builds a task's
  environment from one `K="v"` string after substituting variables. Branch names,
  plan variables (which can be overridden when a build is run), and secrets could
  contain quotes, so Bamboo env values may only be literals (without `"`, `\`, or
  `${`) and `${git.sha}`. Reference other values in the script instead.
- **GitLab `GIT_STRATEGY`** is set by Renvoy from the job's checkout steps, so a job
  `env` entry with that name is an error.

## Provider extensions

Extensions live under `extensions.<provider>` at the top level, and for some
providers also on a job. They are passed only to their provider; unknown
namespaces produce a warning.

### github-actions

```yaml
extensions:
  github-actions:
    permissions:            # workflow GITHUB_TOKEN permissions (default contents: read)
      contents: read
      packages: write
    runners:                # runs-on labels per portable runner
      linux: [self-hosted, linux, x64]
```

Job level: `permissions`, `runsOn` (labels).

### gitlab

```yaml
extensions:
  gitlab:
    runnerTags:
      linux: [docker]
      windows: [windows, shell]
```

Job level: `tags`.

### azure-devops

```yaml
extensions:
  azure-devops:
    pools:
      linux: {vmImage: ubuntu-24.04}
      windows: {name: Self-Hosted, demands: ["Agent.OS -equals Windows_NT"]}
```

Job level: `pool`.

### jenkins

```yaml
extensions:
  jenkins:
    agentLabel: linux-docker        # label for linux jobs
    agentLabels:
      windows: windows-2022
```

Job level: `agentLabel`, and `input` to gate a stage on approval:

```yaml
- name: production
  environment:
    name: production
    approvals: {required: true}
  extensions:
    jenkins:
      input:
        message: Deploy to production?
        ok: Deploy
        submitter: release-managers
```

### bamboo (used by `bamboo` and `bamboo-java`)

```yaml
extensions:
  bamboo:
    projectKey: ACME              # required; YAML Specs cannot create projects
    planKey: SERVICE              # required
    requirements:                 # agent requirements per runner
      windows: ["operating.system=Windows.*"]
    # Java Specs only:
    linkedRepository: acme-service
    specsVersion: 12.1.12         # must match the Bamboo server version
    javaPackage: com.acme.specs
    serverUrl: https://bamboo.example.com
```

The generated `PlanSpec.java` passes the plan through `customize(Plan plan)`,
which lives in the preserved custom block, so you can adjust the plan with the
Bamboo Specs API without losing changes on regeneration. `mvn test` in
`bamboo-specs/` runs Bamboo's offline validation.

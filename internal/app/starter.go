package app

// Starter is the file written by `renvoy init`. Tests keep it valid for every provider.
const Starter = `# Renvoy pipeline definition: one source of truth, compiled into native CI/CD files.
# Reference: docs/specification.md
apiVersion: renvoy.dev/v1alpha1
kind: Pipeline

metadata:
  # Lowercase letters, digits, and hyphens. Also names generated files.
  name: my-service

# When the pipeline runs.
triggers:
  push:
    branches: [main]
  pullRequest: {}
  manual: {}

# Plain values shared by every job. Reference them as ${variables.NAME}.
variables:
  IMAGE: registry.example.com/my-service

# Names of secrets stored in your CI system. Reference them as ${secrets.NAME}.
# Never put secret values in this file.
secrets:
  - REGISTRY_PASSWORD

stages:
  - name: test
    jobs:
      - name: unit-tests
        runsOn: linux
        container:
          image: python:3.12
        steps:
          - uses: checkout
          - run: pip install -r requirements.txt
          - run: pytest --junitxml=reports/junit.xml
          # Test results are published even when earlier steps fail.
          - publishTestResults:
              path: reports/junit.xml

  # A stage without dependsOn runs after the stage declared before it.
  - name: build
    jobs:
      - name: container-image
        # Only build images on main (pull request builds are skipped).
        when:
          branches: [main]
        steps:
          - uses: checkout
          - buildImage:
              context: .
              image: ${variables.IMAGE}:${git.sha}
          - name: Log in to the registry
            run: echo "${secrets.REGISTRY_PASSWORD}" | docker login registry.example.com --username ci --password-stdin
          - pushImage:
              image: ${variables.IMAGE}:${git.sha}

  - name: deploy
    dependsOn: [build]
    jobs:
      - name: staging
        environment: staging
        steps:
          - uses: checkout
          - deployKubernetes:
              manifests: k8s/
              namespace: staging

# Provider settings. Bamboo needs the keys of an existing project and of the
# plan to create; replace these with your own.
extensions:
  bamboo:
    projectKey: PROJ
    planKey: SERVICE
`

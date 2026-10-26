// Package registry lists the providers compiled into the binary.
package registry

import (
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/azuredevops"
	"github.com/amitpatole/renvoy/internal/providers/bamboo"
	"github.com/amitpatole/renvoy/internal/providers/githubactions"
	"github.com/amitpatole/renvoy/internal/providers/gitlab"
	"github.com/amitpatole/renvoy/internal/providers/jenkins"
)

// All returns every compiled-in provider in display order.
func All() []providers.Provider {
	return []providers.Provider{
		githubactions.New(),
		gitlab.New(),
		azuredevops.New(),
		jenkins.New(),
		bamboo.NewYAML(),
		bamboo.NewJava(),
	}
}

// Get returns the provider with the given name.
func Get(name string) (providers.Provider, bool) {
	for _, p := range All() {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Names returns every provider name in display order.
func Names() []string {
	var names []string
	for _, p := range All() {
		names = append(names, p.Name())
	}
	return names
}

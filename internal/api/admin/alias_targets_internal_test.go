package admin

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/cluster"

	"github.com/stretchr/testify/assert"
)

type deployedOnly []cluster.DeployedEntry

func (d deployedOnly) DefaultDeployedModels() []cluster.DeployedEntry { return d }

func TestAliasTargetIDs_LocalServerMayNameAnyRoutableCatalogModel(t *testing.T) {
	deployed := deployedOnly{{Model: "z-ai/glm-5.2", Provider: providers.ProviderDeepInfra}}

	local := aliasTargetIDs(providers.ProviderLocalOpenAI, deployed)
	gateway := aliasTargetIDs(providers.ProviderOpenAIGateway, deployed)

	assert.Contains(t, local, "z-ai/glm-5.3-flash", "a local server is reached only through its aliases")
	assert.Contains(t, local, "z-ai/glm-5.2")
	assert.NotContains(t, gateway, "z-ai/glm-5.3-flash", "gateway aliases stay limited to deployed models")
}

func TestAliasTargetIDs_NilSourceSkipsValidation(t *testing.T) {
	assert.Nil(t, aliasTargetIDs(providers.ProviderLocalOpenAI, nil))
}

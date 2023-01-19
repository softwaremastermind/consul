package observability

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/hashicorp/consul/api"
	libassert "github.com/hashicorp/consul/test/integration/consul-container/libs/assert"
	libcluster "github.com/hashicorp/consul/test/integration/consul-container/libs/cluster"
	libservice "github.com/hashicorp/consul/test/integration/consul-container/libs/service"
	"github.com/hashicorp/consul/test/integration/consul-container/libs/utils"
)

// TestAccessLogs Summary
// This test ensures that when enabled through `proxy-defaults`, Envoy will emit access logs.
// Philosophically, we are trying to ensure the config options make their way to Envoy more than
// trying to test Envoy's behavior. For this reason and simplicity file operations are not tested.
//
// Steps:
//   - Create a single agent cluster.
//   - Enable default access logs. We do this so Envoy's admin interface inherits the configuration on startup
//   - Create the example static-server and sidecar containers, then register them both with Consul
//   - Create an example static-client sidecar, then register both the service and sidecar with Consul
//   - Make sure a call to the client sidecar emits an access log at the client-sidecar (outbound) and
//     server-sidecar (inbound).
//   - Make sure hitting the Envoy admin interface generates an access log
//   - Change access log configuration to use custom text format and disable Listener logs
//   - Make sure a call to the client sidecar emits an access log at the client-sidecar (outbound) and
//     server-sidecar (inbound).
//   - Clear the access log configuration from `proxy-defaults`
//   - Make sure a call to the client sidecar local bind port succeeds but does not emit a log
//
// Notes:
//   - Does not test disabling listener logs. In practice, it's hard to get them to emit. The best chance would
//     be running a service that throws a 404 on a random path or maybe use some path-based disco chains
//   - JSON keys have no guaranteed ordering, so simple key-value pairs are tested
func TestAccessLogs(t *testing.T) {
	if semver.IsValid(utils.TargetVersion) && semver.Compare(utils.TargetVersion, "v1.15") < 0 {
		t.Skip()
	}

	cluster := createCluster(t)

	// Turn on access logs. Do this before starting the sidecars so that they inherit the configuration
	// for their admin interface
	proxyDefault := &api.ProxyConfigEntry{
		Kind: api.ProxyDefaults,
		Name: api.ProxyConfigGlobal,
		AccessLogs: &api.AccessLogsConfig{
			Enabled:    true,
			JSONFormat: "{\"banana_path\":\"%REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%\"}",
		},
	}
	set, _, err := cluster.Agents[0].GetClient().ConfigEntries().Set(proxyDefault, nil)
	require.NoError(t, err)
	require.True(t, set)

	serverService, clientService := createServices(t, cluster)
	_, port := clientService.GetAddr()

	// Validate Custom JSON
	require.Eventually(t, func() bool {
		libassert.HTTPServiceEchoes(t, "localhost", port, "banana")
		client := libassert.ServiceLogContains(t, clientService, "\"banana_path\":\"/banana\"")
		server := libassert.ServiceLogContains(t, serverService, "\"banana_path\":\"/banana\"")
		return client && server
	}, 20*time.Second, 1*time.Second)

	// Validate Logs on the Admin Interface
	serverSidecar, ok := serverService.(*libservice.ConnectContainer)
	require.True(t, ok)
	ip, port := serverSidecar.GetAdminAddr()

	httpClient := cleanhttp.DefaultClient()
	url := fmt.Sprintf("http://%s:%d/clusters?fruit=bananas", ip, port)
	_, err = httpClient.Get(url)
	require.NoError(t, err, "error making call to Envoy admin interface")

	require.Eventually(t, func() bool {
		return libassert.ServiceLogContains(t, serverService, "\"banana_path\":\"/clusters?fruit=bananas\"")
	}, 15*time.Second, 1*time.Second)

	// TODO: add a test to check that connections without a matching filter chain are logged

	// Validate Listener Logs
	proxyDefault = &api.ProxyConfigEntry{
		Kind: api.ProxyDefaults,
		Name: api.ProxyConfigGlobal,
		AccessLogs: &api.AccessLogsConfig{
			Enabled:             true,
			DisableListenerLogs: true,
			TextFormat:          "Orange you glad I didn't say banana: %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%, %RESPONSE_FLAGS%",
		},
	}

	set, _, err = cluster.Agents[0].GetClient().ConfigEntries().Set(proxyDefault, nil)
	require.NoError(t, err)
	require.True(t, set)
	time.Sleep(5 * time.Second) // time for xDS to propagate

	// Validate Custom Text
	_, port = clientService.GetAddr()
	require.Eventually(t, func() bool {
		libassert.HTTPServiceEchoes(t, "localhost", port, "orange")
		client := libassert.ServiceLogContains(t, clientService, "Orange you glad I didn't say banana: /orange, -")
		server := libassert.ServiceLogContains(t, serverService, "Orange you glad I didn't say banana: /orange, -")
		return client && server
	}, 60*time.Second, 500*time.Millisecond) // For some reason it takes a long time for the server sidecar to update

	// TODO: add a test to check that connections without a matching filter chain are NOT logged

	// Validate access logs can be turned off
	proxyDefault = &api.ProxyConfigEntry{
		Kind: api.ProxyDefaults,
		Name: api.ProxyConfigGlobal,
		AccessLogs: &api.AccessLogsConfig{
			Enabled: false,
		},
	}

	set, _, err = cluster.Agents[0].GetClient().ConfigEntries().Set(proxyDefault, nil)
	require.NoError(t, err)
	require.True(t, set)
	time.Sleep(5 * time.Second) // time for xDS to propagate

	_, port = clientService.GetAddr()
	libassert.HTTPServiceEchoes(t, "localhost", port, "mango")
	time.Sleep(5 * time.Second) // time to flush buffers

	require.False(t, libassert.ServiceLogContains(t, clientService, "mango"))
	require.False(t, libassert.ServiceLogContains(t, serverService, "mango"))
}

func createCluster(t *testing.T) *libcluster.Cluster {
	opts := libcluster.BuildOptions{
		InjectAutoEncryption:   true,
		InjectGossipEncryption: true,
		// TODO: fix the test to not need the service/envoy stack to use :8500
		AllowHTTPAnyway: true,
	}
	ctx := libcluster.NewBuildContext(t, opts)

	conf := libcluster.NewConfigBuilder(ctx).
		ToAgentConfig(t)
	t.Logf("Cluster config:\n%s", conf.JSON)

	configs := []libcluster.Config{*conf}

	cluster, err := libcluster.New(t, configs)
	require.NoError(t, err)

	node := cluster.Agents[0]
	client := node.GetClient()

	libcluster.WaitForLeader(t, cluster, client)
	libcluster.WaitForMembers(t, client, 1)

	// Default Proxy Settings
	ok, err := utils.ApplyDefaultProxySettings(client)
	require.NoError(t, err)
	require.True(t, ok)

	return cluster
}

func createServices(t *testing.T, cluster *libcluster.Cluster) (libservice.Service, libservice.Service) {
	node := cluster.Agents[0]
	client := node.GetClient()

	// Register service as HTTP
	serviceDefault := &api.ServiceConfigEntry{
		Kind:     api.ServiceDefaults,
		Name:     libservice.StaticServerServiceName,
		Protocol: "http",
	}

	ok, _, err := client.ConfigEntries().Set(serviceDefault, nil)
	require.NoError(t, err, "error writing HTTP service-default")
	require.True(t, ok, "did not write HTTP service-default")

	// Create a service and proxy instance
	_, serverConnectProxy, err := libservice.CreateAndRegisterStaticServerAndSidecar(node)
	require.NoError(t, err)

	libassert.CatalogServiceExists(t, client, fmt.Sprintf("%s-sidecar-proxy", libservice.StaticServerServiceName))
	libassert.CatalogServiceExists(t, client, libservice.StaticServerServiceName)

	// Create a client proxy instance with the server as an upstream
	clientConnectProxy, err := libservice.CreateAndRegisterStaticClientSidecar(node, "", false)
	require.NoError(t, err)

	libassert.CatalogServiceExists(t, client, fmt.Sprintf("%s-sidecar-proxy", libservice.StaticClientServiceName))

	return serverConnectProxy, clientConnectProxy
}

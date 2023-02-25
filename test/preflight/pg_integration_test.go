//go:build integration
// +build integration

package preflight

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/superfly/flyctl/test/preflight/testlib"
)

func TestPg_ExternalConnectionDocs(t *testing.T) {
	var (
		result  *testlib.FlyctlResult
		f       = testlib.NewTestEnvFromEnv(t)
		appName = f.CreateRandomAppName()
	)
	f.Fly("pg create --org %s --name %s --region %s --initial-cluster-size 1 --vm-size shared-cpu-1x --volume-size 1", f.OrgSlug(), appName, f.PrimaryRegion())
	// FIXME: snag the database url or at least the username/password out of there
	f.Fly("status -a %s", appName)

	// https://fly.io/docs/postgres/connecting/connecting-external/#allocate-an-ip-address
	result = f.Fly("ips list --app %s", appName)
	require.Contains(f, result.StdOut().String(), "private_v6")
	f.Fly("fly ips allocate-v4 --app %s", appName)
	f.Fly("fly ips allocate-v6 --app %s", appName)
	result = f.Fly("ips list --app %s", appName)
	require.Regexp(f, regexp.MustCompile("^v6\\s+.*\\s+public"), result.StdOut().String())
	require.Regexp(f, regexp.MustCompile("^v4\\s+.*\\s+public"), result.StdOut().String())

	// https://fly.io/docs/postgres/connecting/connecting-external/#configure-an-external-service
	f.Fly("config save --app %s", appName)
	require.Contains(f, f.FlyTomlContent(), appName)
	f.AppendToFlyToml(`
[[services]]
	internal_port = 5432 # Postgres instance
	protocol = "tcp"

[[services.ports]]
	handlers = ["pg_tls"]
	port = 5432`)

	// https://fly.io/docs/postgres/connecting/connecting-external/#deploy-with-the-new-configuration
	result = f.Fly("image show --app %s", appName)
	imagePattern := regexp.MustCompile("\\s+(?P:<image>flyio/\\S+)\\s+(?P:<tag>\\S+)\\s+")
	matches := imagePattern.FindStringSubmatch(result.StdOut().String())

	// FIXME: this up!
	f.Fly("fly deploy . --app %s --image flyio/postgres:<major-version>")
	f.Fly("info")

	// FIXME: try to connect with sql.Open() and run a query

}

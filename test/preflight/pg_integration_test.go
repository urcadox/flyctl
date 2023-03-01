//go:build integration
// +build integration

package preflight

import (
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/google/go-querystring/query"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/superfly/flyctl/test/preflight/testlib"
)

func TestPg_ExternalConnectionDocs(t *testing.T) {
	var (
		result  *testlib.FlyctlResult
		f       = testlib.NewTestEnvFromEnv(t)
		appName = f.CreateRandomAppName()
	)
	result = f.Fly("pg create --org %s --name %s --region %s --initial-cluster-size 1 --vm-size shared-cpu-1x --volume-size 1", f.OrgSlug(), appName, f.PrimaryRegion())

	dbUrlRegex := `(?m)Connection string: (postgres://\S+:\S+@\[.+\]:\d+)$`
	dbUrlPattern := regexp.MustCompile(dbUrlRegex)
	dbUrlMatches := dbUrlPattern.FindStringSubmatch(result.StdOut().String())
	if len(dbUrlMatches) < 2 {
		f.Fatalf("Did not find connection string matches %s in output:\n%s", dbUrlRegex, result.StdOut().String())
	}
	dbUrl := dbUrlMatches[1]

	f.Fly("status -a %s", appName)

	// https://fly.io/docs/postgres/connecting/connecting-external/#allocate-an-ip-address
	result = f.Fly("ips list --app %s", appName)
	require.Contains(f, result.StdOut().String(), "private_v6")
	f.Fly("ips allocate-v4 --app %s", appName)
	f.Fly("ips allocate-v6 --app %s", appName)
	result = f.Fly("ips list --app %s", appName)
	require.Regexp(f, regexp.MustCompile(`(?m)^v6\s+.*\s+public`), result.StdOut().String())
	require.Regexp(f, regexp.MustCompile(`(?m)^v4\s+.*\s+public`), result.StdOut().String())

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
	imageRegex := `\s+(flyio/\S+)\s+(\S+)\s+`
	imagePattern := regexp.MustCompile(imageRegex)
	imageMatches := imagePattern.FindStringSubmatch(result.StdOut().String())
	if len(imageMatches) < 3 {
		f.Fatalf("Could not find %s image in output:\n%s", imageRegex, result.StdOut().String())
	}
	imageOnly := imageMatches[1]
	tag := imageMatches[2]
	image := fmt.Sprintf("%s:%s", imageOnly, tag)

	f.Fly("deploy . --app %s --image %s", appName, image)
	f.Fly("info")

	time.Sleep(5 * time.Second)

	// https://fly.io/docs/postgres/connecting/connecting-external/#adapting-the-connection-string
	dbUrlParsed, err := url.Parse(dbUrl)
	require.Nil(f, err, "error parsing database url '%s'", dbUrl)
	dbUrlParsed.Host = fmt.Sprintf("%s.fly.dev:5432", appName)
	dbQsVals := pgQuerystring{SslMode: "require"}
	dbQs, err := query.Values(dbQsVals)
	require.Nil(f, err, "error making query string from %v", dbQsVals)
	dbUrlParsed.RawQuery = dbQs.Encode()
	publicDbUrl := dbUrlParsed.String()
	pgDb, err := sql.Open("postgres", publicDbUrl)
	require.Nil(f, err, "error opening database url '%s'", publicDbUrl)
	testQuery := "select datname from pg_database;"
	rows, err := pgDb.Query(testQuery)
	f.Cleanup(func() { rows.Close() })
	// FIXME: getting                 Error:          Expected nil, but got: &errors.errorString{s:"EOF"}
	require.Nil(f, err, "error running '%s' query against '%s'", testQuery, publicDbUrl)
	var dbName string
	err = rows.Scan(&dbName)
	require.Nil(f, err, "error scanning row of test query '%s' result", testQuery)
	require.NotEmpty(f, dbName, "expected a database name in result")
}

type pgQuerystring struct {
	SslMode string `url:"sslmode,omitempty"`
}

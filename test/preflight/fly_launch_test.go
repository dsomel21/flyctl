//go:build integration
// +build integration

package preflight

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/superfly/flyctl/test/preflight/testlib"
)

// TODO: list of things to test
// - sourceInfo clone vs launch modes
// - sourceInfo remote dockerfile vs local
// END

// Launch a new app and iterate rerunning `fly launch` to reuse the same app name and config
//
// - Create a V2 app
// - Must contain [http_service] section (no [[services]])
// - primary_region set and updated on subsequent 'fly launch --region other' calls
// - Internal port is set in first call and not replaced unless --internal-port is passed again
// - Primary region found in imported fly.toml must be reused if set and no --region is passed
// - As we are reusing an existing app, the --org param is not needed after the first call
//
func TestFlyLaunch_case01(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()

	f.Fly("launch --no-deploy --org %s --name %s --region %s --image nginx --force-machines", f.OrgSlug(), appName, f.PrimaryRegion())
	toml := f.UnmarshalFlyToml()
	want := map[string]any{
		"app":            appName,
		"primary_region": f.PrimaryRegion(),
		"build":          map[string]any{"image": "nginx"},
		"http_service":   map[string]any{"force_https": true, "internal_port": int64(8080)},
		"checks": map[string]any{
			"alive": map[string]any{
				"type":         "tcp",
				"interval":     "15s",
				"timeout":      "2s",
				"grace_period": "5s",
			},
		},
	}
	require.EqualValues(f, want, toml)

	f.Fly("launch --no-deploy --reuse-app --copy-config --name %s --region %s --image nginx:stable", appName, f.SecondaryRegion())
	toml = f.UnmarshalFlyToml()
	want["primary_region"] = f.SecondaryRegion()
	if build, ok := want["build"].(map[string]any); true {
		require.True(f, ok)
		build["image"] = "nginx:stable"
	}
	require.Equal(f, want, toml)

	f.Fly("launch --no-deploy --reuse-app --copy-config --name %s --image nginx:stable --internal-port 9999", appName)
	toml = f.UnmarshalFlyToml()
	if service, ok := want["http_service"].(map[string]any); true {
		require.True(f, ok)
		service["internal_port"] = int64(9999)
	}
	require.EqualValues(f, want, toml)
}

// Same as case01 but for Nomad apps
func TestFlyLaunch_case02(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()

	f.Fly("launch --no-deploy --org %s --name %s --region %s --image nginx --internal-port 80 --force-nomad", f.OrgSlug(), appName, f.PrimaryRegion())
	toml := f.UnmarshalFlyToml()
	want := map[string]any{
		"app":            appName,
		"build":          map[string]any{"image": "nginx"},
		"env":            map[string]any{},
		"experimental":   map[string]any{"auto_rollback": true},
		"kill_signal":    "SIGINT",
		"kill_timeout":   int64(5),
		"primary_region": f.PrimaryRegion(),
		"processes":      []any{},
		"services": []map[string]any{{
			"concurrency":   map[string]any{"hard_limit": int64(25), "soft_limit": int64(20), "type": "connections"},
			"http_checks":   []any{},
			"internal_port": int64(80),
			"ports": []map[string]any{
				{"force_https": true, "handlers": []any{"http"}, "port": int64(80)},
				{"handlers": []any{"tls", "http"}, "port": int64(443)},
			},
			"processes":     []any{"app"},
			"protocol":      "tcp",
			"script_checks": []any{},
			"tcp_checks": []map[string]any{{
				"grace_period":  "1s",
				"interval":      "15s",
				"timeout":       "2s",
				"restart_limit": int64(0),
			}},
		}},
	}
	require.EqualValues(f, want, toml)

	f.Fly("launch --no-deploy --reuse-app --copy-config --name %s --region %s --image nginx:stable", appName, f.SecondaryRegion())
	toml = f.UnmarshalFlyToml()
	want["primary_region"] = f.SecondaryRegion()
	if build, ok := want["build"].(map[string]any); true {
		require.True(f, ok)
		build["image"] = "nginx:stable"
	}
	require.EqualValues(f, want, toml)

	f.Fly("launch --no-deploy --reuse-app --copy-config --name %s --image nginx:stable --internal-port 9999", appName)
	toml = f.UnmarshalFlyToml()
	if services, ok := want["services"].([]map[string]any); true {
		require.True(f, ok)
		services[0]["internal_port"] = int64(9999)
	}
	require.EqualValues(f, want, toml)
}

// Run fly launch from a template Fly App directory (fly.toml without app name)
func TestFlyLaunch_case03(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()

	f.WriteFlyToml(`
[build]
  image = "superfly/postgres:15"

[checks.status]
	type = "tcp"
	port = 5500
	`)

	f.Fly("launch --no-deploy --org %s --name %s --region %s --force-machines --copy-config", f.OrgSlug(), appName, f.PrimaryRegion())
	toml := f.UnmarshalFlyToml()
	want := map[string]any{
		"app":            appName,
		"primary_region": f.PrimaryRegion(),
		"build":          map[string]any{"image": "superfly/postgres:15"},
		"checks": map[string]any{
			"status": map[string]any{"type": "tcp", "port": int64(5500)},
		},
	}
	require.EqualValues(f, want, toml)

	// reuse the config and app but update the image
	f.Fly("launch --no-deploy --reuse-app --copy-config --name %s --image superfly/postgres:14", appName)
	toml = f.UnmarshalFlyToml()
	if build, ok := want["build"].(map[string]any); true {
		require.True(f, ok)
		build["image"] = "superfly/postgres:14"
	}
	require.EqualValues(f, want, toml)
}

// Trying to import an invalid fly.toml should fail before creating the app
func TestFlyLaunch_case04(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()

	f.WriteFlyToml(`
app = "foo"

[[services]]
	internal_port = "can'tparse as port number"  # invalid
	protocol = "tcp"
	`)

	x := f.FlyAllowExitFailure("launch --no-deploy --org %s --name %s --region %s --force-machines --copy-config", f.OrgSlug(), appName, f.PrimaryRegion())
	require.Contains(f, x.StdErr().String(), `Can not use configuration for Apps V2, check fly.toml`)
}

// Fail if the existing app doesn't match the forced platform version
// V2 app forced as V1
func TestFlyLaunch_case05a(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	appName := f.CreateRandomAppName()
	f.Fly("apps create %s --machines -o %s", appName, f.OrgSlug())
	x := f.FlyAllowExitFailure("launch --no-deploy --reuse-app --name %s --region %s --force-nomad", appName, f.PrimaryRegion())
	require.Contains(f, x.StdErr().String(), `--force-nomad won't work for existing app in machines platform`)
}

// V1 app forced as V2
func TestFlyLaunch_case05b(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	appName := f.CreateRandomAppName()
	// Sadly creating a new app with --nomad doesn't set its platform to Nomad until first deploy
	f.Fly("apps create %s -o %s --nomad", appName, f.OrgSlug())
	f.Fly("launch --now --image nginx --name %s --reuse-app --region=%s --force-nomad", appName, f.PrimaryRegion())

	x := f.FlyAllowExitFailure("launch --no-deploy --copy-config=false --reuse-app --name %s --region %s --force-machines", appName, f.PrimaryRegion())
	require.Contains(f, x.StdErr().String(), `--force-machines won't work for existing app in nomad platform`)
}

// test --generate-name, --name and reuse imported name
func TestFlyLaunch_case06(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	// V2 app forced as V1
	appName1 := f.CreateRandomAppName()
	appName2 := f.CreateRandomAppName()
	appName3 := f.CreateRandomAppName()

	f.WriteFlyToml(`
app = "%s"
primary_region = "%s"
`, appName1, f.PrimaryRegion())

	f.Fly("launch --no-deploy --copy-config --name %s -o %s", appName2, f.OrgSlug())
	toml := f.UnmarshalFlyToml()
	require.EqualValues(f, appName2, toml["app"])

	f.Fly("launch --no-deploy --copy-config=false --name %s --region %s -o %s", appName3, f.PrimaryRegion(), f.OrgSlug())
	toml = f.UnmarshalFlyToml()
	require.EqualValues(f, appName3, toml["app"])

	f.Fly("launch --no-deploy --copy-config --generate-name -o %s", f.OrgSlug())
	toml = f.UnmarshalFlyToml()
	generatedName := toml["app"]
	f.Cleanup(func() {
		f.Fly("apps destroy -y %s", generatedName)
	})
	require.NotEqual(f, appName3, toml["app"])
}
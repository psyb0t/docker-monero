package testinfra

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coverage wiring can rot silently: the gate then reports every service
// package as untested instead of failing. These tests pin the request
// StartApp builds so a regression shows up as a red test.

func TestAppRequestPlainWhenCoverageIsOff(t *testing.T) {
	req := appRequest(AppConfig{}, map[string]string{}, "/repo", "")

	assert.Equal(t, appImageTag, req.Tag)
	assert.Nil(t, req.BuildArgs)
	assert.Nil(t, req.HostConfigModifier)
	assert.Equal(t, []string{appRunCommand}, req.Cmd)
}

func TestAppRequestInstrumentedWhenCoverageIsOn(t *testing.T) {
	const coverDir = "/workspace/.cover/covdata"

	req := appRequest(AppConfig{}, map[string]string{}, "/repo", coverDir)

	assert.Equal(t, appImageTagCover, req.Tag)
	require.Contains(t, req.BuildArgs, "BUILD_COVER")
	assert.Contains(t, *req.BuildArgs["BUILD_COVER"], coveredModule+"/...")

	hostConfig := &container.HostConfig{}
	req.HostConfigModifier(hostConfig)
	assert.Equal(t,
		[]string{coverDir + ":" + containerCoverDir}, hostConfig.Binds)
}

func TestAppEnv(t *testing.T) {
	t.Setenv(coverDirEnvVar, "")

	env := appEnv(AppConfig{Env: map[string]string{
		"DOCKER_MONERO_READ_ONLY": "true",
	}}, "tok")

	assert.Equal(t, "regtest", env["DOCKER_MONERO_NETWORK"])
	assert.Equal(t, "tok", env["DOCKER_MONERO_API_TOKEN"])
	assert.Equal(t, "true", env["DOCKER_MONERO_READ_ONLY"])
	assert.NotContains(t, env, "GOCOVERDIR")

	t.Setenv(coverDirEnvVar, "/x")
	assert.Equal(t, containerCoverDir, appEnv(AppConfig{}, "t")["GOCOVERDIR"])
}

func TestPrepareCoverDir(t *testing.T) {
	t.Setenv(coverDirEnvVar, "")

	dir, err := prepareCoverDir()
	require.NoError(t, err)
	assert.Empty(t, dir)

	t.Setenv(coverDirEnvVar, "relative/path")

	_, err = prepareCoverDir()
	require.Error(t, err)

	target := filepath.Join(t.TempDir(), "covdata")
	t.Setenv(coverDirEnvVar, target)

	dir, err = prepareCoverDir()
	require.NoError(t, err)
	assert.Equal(t, target, dir)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(coverModePerm), info.Mode().Perm())
}

func TestRandomTokenAndEditor(t *testing.T) {
	token, err := randomToken()
	require.NoError(t, err)
	assert.Len(t, token, tokenBytes*2)

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://x", nil,
	)
	require.NoError(t, err)
	require.NoError(t, BearerEditor(token)(context.Background(), request))
	assert.True(t, strings.HasSuffix(
		request.Header.Get("Authorization"), token,
	))
}

func TestRepoRoot(t *testing.T) {
	root, err := repoRoot()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, "go.mod"))
}

func TestTerminateNil(_ *testing.T) {
	var app *App

	app.Terminate(context.Background())
}

// Package testinfra runs the production image for the integration suite.
//
// It builds the repository Dockerfile, starts the container on regtest, and
// waits until monerod answers. A regtest node is offline and mines on
// demand, so the suite exercises the real monerod and monero-wallet-rpc
// binaries, real wallets, and real transactions without a network.
package testinfra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/pkg/http/api/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Ports the image exposes.
const (
	appHTTPPort          = "8080"
	appMetricsPort       = "9091"
	appRestrictedRPCPort = "18089"
)

const (
	appDockerfile  = "Dockerfile"
	appRunCommand  = "run"
	appStartTimout = 10 * time.Minute // covers a cold image build
	readyPath      = "/ready"
	apiBasePath    = "/v1"
	tokenBytes     = 32
	httpTimeout    = 2 * time.Minute

	readyPollInterval = time.Second
)

// The suite builds the image under fixed names so repeated StartApp calls
// reuse one build instead of producing a UUID-tagged image each time. A
// coverage run builds a second, instrumented image under its own tag.
const (
	appImageRepo     = "docker-monero-test"
	appImageTag      = "api-suite"
	appImageTagCover = "api-suite-cover"
)

// Coverage plumbing. When the framework's coverage target sets
// SERVICEPACK_COVDATA_DIR, the image is built with -cover and that directory
// is mounted as GOCOVERDIR, so the service's own lines count toward the gate
// instead of reading as dead code.
const (
	coverDirEnvVar    = "SERVICEPACK_COVDATA_DIR"
	containerCoverDir = "/covdata"
	coveredModule     = "github.com/psyb0t/docker-monero"

	// coverModePerm is wide on purpose: the test process and the
	// container's non-root user are different UIDs and both write here.
	coverModePerm = 0o777
)

var errNoGoMod = errors.New("go.mod not found above the working directory")

// AppConfig is what a test varies about the service under test.
type AppConfig struct {
	// Env adds or overrides DOCKER_MONERO_* variables.
	Env map[string]string
	// ExposeRestrictedRPC publishes the remote-wallet RPC for protocol tests.
	ExposeRestrictedRPC bool
}

// App is a running container plus what a test needs to talk to it.
type App struct {
	Container  testcontainers.Container
	BaseURL    string
	MetricsURL string
	Token      string
	Client     *client.ClientWithResponses
}

// StartApp builds the image if needed and runs it on regtest with a fresh
// random API token. It returns once /ready answers 200.
func StartApp(ctx context.Context, config AppConfig) (*App, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	coverDir, err := prepareCoverDir()
	if err != nil {
		return nil, err
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}

	instance, err := testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: appRequest(
				config, appEnv(config, token), root, coverDir,
			),
			Started: true,
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build and start the app image")
	}

	app, err := describeApp(ctx, instance, token)
	if err != nil {
		_ = instance.Terminate(ctx)

		return nil, err
	}

	return app, nil
}

// appRequest is the container request StartApp sends.
func appRequest(
	config AppConfig,
	env map[string]string,
	root, coverDir string,
) testcontainers.ContainerRequest {
	request := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    root,
			Dockerfile: appDockerfile,
			Repo:       appImageRepo,
			Tag:        imageTag(coverDir),
			BuildArgs:  buildArgs(coverDir),
			KeepImage:  true,
		},
		Cmd: []string{appRunCommand},
		Env: env,
		ExposedPorts: []string{
			appHTTPPort + "/tcp",
			appMetricsPort + "/tcp",
		},
		HostConfigModifier: mountCoverDir(coverDir),
		WaitingFor: wait.ForHTTP(readyPath).
			WithPort(appHTTPPort + "/tcp").
			WithStatusCodeMatcher(func(status int) bool {
				return status == http.StatusOK
			}).
			WithStartupTimeout(appStartTimout),
	}
	if config.ExposeRestrictedRPC {
		request.ExposedPorts = append(
			request.ExposedPorts, appRestrictedRPCPort+"/tcp",
		)
	}

	return request
}

// appEnv is the container environment: regtest, the test's token, and
// whatever the test overrides.
func appEnv(config AppConfig, token string) map[string]string {
	env := map[string]string{
		"LOG_LEVEL":               "info",
		"LOG_FORMAT":              "json",
		"DOCKER_MONERO_NETWORK":   "regtest",
		"DOCKER_MONERO_API_TOKEN": token,
	}

	if os.Getenv(coverDirEnvVar) != "" {
		env["GOCOVERDIR"] = containerCoverDir
	}

	maps.Copy(env, config.Env)

	return env
}

func describeApp(
	ctx context.Context,
	instance testcontainers.Container,
	token string,
) (*App, error) {
	host, err := instance.Host(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve the app host")
	}

	httpPort, err := instance.MappedPort(ctx, appHTTPPort+"/tcp")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve the api port")
	}

	metricsPort, err := instance.MappedPort(ctx, appMetricsPort+"/tcp")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve the metrics port")
	}

	baseURL := "http://" + net.JoinHostPort(host, httpPort.Port())

	api, err := client.NewClientWithResponses(
		baseURL+apiBasePath,
		client.WithHTTPClient(&http.Client{Timeout: httpTimeout}),
		client.WithRequestEditorFn(BearerEditor(token)),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the api client")
	}

	return &App{
		Container: instance,
		BaseURL:   baseURL,
		MetricsURL: "http://" +
			net.JoinHostPort(host, metricsPort.Port()) + "/metrics",
		Token:  token,
		Client: api,
	}, nil
}

// Reconnect re-reads the mapped ports after the container restarted and
// waits until /ready answers again.
func Reconnect(ctx context.Context, app *App) (*App, error) {
	restarted, err := describeApp(ctx, app.Container, app.Token)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(appStartTimout)

	for time.Now().Before(deadline) {
		if ready(ctx, restarted.BaseURL) {
			return restarted, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctxerrors.Wrap(ctx.Err(), "wait for readiness")
		case <-time.After(readyPollInterval):
		}
	}

	return nil, ctxerrors.Wrap(commerr.ErrTimeout, "app never became ready")
}

func ready(ctx context.Context, baseURL string) bool {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, baseURL+readyPath, nil,
	)
	if err != nil {
		return false
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}

	_ = response.Body.Close()

	return response.StatusCode == http.StatusOK
}

// BearerEditor sets the Authorization header on every client request.
func BearerEditor(token string) client.RequestEditorFn {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+token)

		return nil
	}
}

// Terminate stops the container. Best effort, safe on a nil receiver.
func (a *App) Terminate(ctx context.Context) {
	if a == nil || a.Container == nil {
		return
	}

	_ = a.Container.Terminate(ctx)
}

// Logs returns everything the container has written so far.
func (a *App) Logs(ctx context.Context) (string, error) {
	reader, err := a.Container.Logs(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read app logs")
	}

	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(reader)
	if err != nil {
		return "", ctxerrors.Wrap(err, "drain app logs")
	}

	return string(body), nil
}

func randomToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", ctxerrors.Wrap(err, "generate the api token")
	}

	return hex.EncodeToString(buf), nil
}

// prepareCoverDir returns the host covdata directory for a coverage run, or
// "" when coverage is off. The path must be absolute: the Docker daemon
// resolves the bind source on the host, so a relative path would mount
// somewhere else and the counters would vanish.
func prepareCoverDir() (string, error) {
	raw := os.Getenv(coverDirEnvVar)
	if raw == "" {
		return "", nil
	}

	dir := filepath.Clean(raw)
	if !filepath.IsAbs(dir) {
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s must be an absolute path, got %q", coverDirEnvVar, raw,
		)
	}

	if err := os.MkdirAll(dir, coverModePerm); err != nil {
		return "", ctxerrors.Wrap(err, "create the covdata directory")
	}

	// MkdirAll honours umask, so the mode is set again for the container
	// user. A directory owned by another user that cannot be widened would
	// make coverage silently empty, which is worse than a failed run.
	if err := os.Chmod(dir, coverModePerm); err != nil {
		return "", ctxerrors.Wrap(err, "widen the covdata directory")
	}

	return dir, nil
}

func imageTag(coverDir string) string {
	if coverDir == "" {
		return appImageTag
	}

	return appImageTagCover
}

func buildArgs(coverDir string) map[string]*string {
	if coverDir == "" {
		return nil
	}

	cover := "-cover -coverpkg=" + coveredModule + "/..."

	return map[string]*string{"BUILD_COVER": &cover}
}

// mountCoverDir bind-mounts the covdata directory into the container. The
// DIND runner keeps the workspace at its host path, so the path this
// process sees is one the daemon resolves too.
func mountCoverDir(coverDir string) func(*container.HostConfig) {
	if coverDir == "" {
		return nil
	}

	return func(hostConfig *container.HostConfig) {
		hostConfig.Binds = append(
			hostConfig.Binds, coverDir+":"+containerCoverDir,
		)
	}
}

// repoRoot walks up to the directory holding go.mod, the build context.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get the working directory")
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoGoMod
		}

		dir = parent
	}
}

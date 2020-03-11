package disttest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/slon/shad-go/distbuild/pkg/artifact"
	"gitlab.com/slon/shad-go/distbuild/pkg/client"
	"gitlab.com/slon/shad-go/distbuild/pkg/dist"
	"gitlab.com/slon/shad-go/distbuild/pkg/filecache"
	"gitlab.com/slon/shad-go/distbuild/pkg/worker"
	"gitlab.com/slon/shad-go/tools/testtool"

	"go.uber.org/zap"
)

type env struct {
	RootDir string
	Logger  *zap.Logger

	Ctx context.Context

	Client      *client.Client
	Coordinator *dist.Coordinator
	Workers     []*worker.Worker

	HTTP *http.Server
}

const nWorkers = 4

func newEnv(t *testing.T) (e *env, cancel func()) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	absCWD, err := filepath.Abs(cwd)
	require.NoError(t, err)

	env := &env{
		RootDir: filepath.Join(absCWD, "workdir", t.Name()),
	}

	require.NoError(t, os.RemoveAll(env.RootDir))
	require.NoError(t, os.MkdirAll(env.RootDir, 0777))

	cfg := zap.NewDevelopmentConfig()
	cfg.OutputPaths = []string{filepath.Join(env.RootDir, "test.log")}
	env.Logger, err = cfg.Build()
	require.NoError(t, err)

	t.Helper()
	t.Logf("test is running inside %s; see test.log file for more info", filepath.Join("workdir", t.Name()))

	port, err := testtool.GetFreePort()
	require.NoError(t, err)
	addr := "127.0.0.1:" + port
	coordinatorEndpoint := "http://" + addr + "/coordinator"

	var cancelRootContext func()
	env.Ctx, cancelRootContext = context.WithCancel(context.Background())

	env.Client = &client.Client{
		CoordinatorEndpoint: coordinatorEndpoint,
		SourceDir:           filepath.Join(absCWD, "testdata/src"),
		Log:                 env.Logger.Named("client"),
	}

	coordinatorCache, err := filecache.New(filepath.Join(env.RootDir, "coordinator", "filecache"))
	require.NoError(t, err)

	env.Coordinator = dist.NewCoordinator(
		env.Logger.Named("coordinator"),
		coordinatorCache,
	)

	for i := 0; i < nWorkers; i++ {
		workerName := fmt.Sprintf("worker%d", i)
		workerDir := filepath.Join(env.RootDir, workerName)

		fileCache, err := filecache.New(filepath.Join(workerDir, "filecache"))
		require.NoError(t, err)

		artifacts, err := artifact.NewCache(filepath.Join(workerDir, "artifacts"))
		require.NoError(t, err)

		w := worker.New(
			coordinatorEndpoint,
			env.Logger.Named(workerName),
			fileCache,
			artifacts,
		)

		env.Workers = append(env.Workers, w)
	}

	mux := http.NewServeMux()
	mux.Handle("/coordinator/", http.StripPrefix("/coordinator", env.Coordinator))

	for i, w := range env.Workers {
		workerPrefix := fmt.Sprintf("/worker/%d", i)
		mux.Handle(workerPrefix+"/", http.StripPrefix(workerPrefix, w))
	}

	env.HTTP = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	lsn, err := net.Listen("tcp", env.HTTP.Addr)
	require.NoError(t, err)

	go func() {
		env.Logger.Error("http server stopped", zap.Error(env.HTTP.Serve(lsn)))
	}()

	return env, func() {
		cancelRootContext()
		_ = env.HTTP.Shutdown(context.Background())
		_ = env.Logger.Sync()
	}
}

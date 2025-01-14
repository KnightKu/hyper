package daemon

import (
	"io"
	"io/ioutil"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	derr "github.com/docker/docker/errors"
	"github.com/docker/docker/pkg/broadcaster"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/pools"
	"github.com/docker/docker/pkg/promise"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/pkg/stringutils"
	"github.com/docker/docker/runconfig"
)

// ExecConfig holds the configurations for execs. The Daemon keeps
// track of both running and finished execs so that they can be
// examined both during and after completion.
type ExecConfig struct {
	sync.Mutex
	ID            string
	Running       bool
	ExitCode      int
	ProcessConfig *execdriver.ProcessConfig
	streamConfig
	OpenStdin  bool
	OpenStderr bool
	OpenStdout bool
	Container  *Container
	canRemove  bool

	// waitStart will be closed immediately after the exec is really started.
	waitStart chan struct{}
}

type execStore struct {
	s map[string]*ExecConfig
	sync.RWMutex
}

func newExecStore() *execStore {
	return &execStore{s: make(map[string]*ExecConfig, 0)}
}

func (e *execStore) Add(id string, ExecConfig *ExecConfig) {
	e.Lock()
	e.s[id] = ExecConfig
	e.Unlock()
}

func (e *execStore) Get(id string) *ExecConfig {
	e.RLock()
	res := e.s[id]
	e.RUnlock()
	return res
}

func (e *execStore) Delete(id string) {
	e.Lock()
	delete(e.s, id)
	e.Unlock()
}

func (e *execStore) List() []string {
	var IDs []string
	e.RLock()
	for id := range e.s {
		IDs = append(IDs, id)
	}
	e.RUnlock()
	return IDs
}

func (ExecConfig *ExecConfig) resize(h, w int) error {
	select {
	case <-ExecConfig.waitStart:
	case <-time.After(time.Second):
		return derr.ErrorCodeExecResize.WithArgs(ExecConfig.ID)
	}
	return ExecConfig.ProcessConfig.Terminal.Resize(h, w)
}

func (d *Daemon) registerExecCommand(ExecConfig *ExecConfig) {
}

// ExecExists looks up the exec instance and returns a bool if it exists or not.
// It will also return the error produced by `getExecConfig`
func (d *Daemon) ExecExists(name string) (bool, error) {
	if _, err := d.getExecConfig(name); err != nil {
		return false, err
	}
	return true, nil
}

// getExecConfig looks up the exec instance by name. If the container associated
// with the exec instance is stopped or paused, it will return an error.
func (d *Daemon) getExecConfig(name string) (*ExecConfig, error) {
	return nil, nil
}

func (d *Daemon) unregisterExecCommand(ExecConfig *ExecConfig) {
}

func (d *Daemon) getActiveContainer(name string) (*Container, error) {
	container, err := d.Get(name)
	if err != nil {
		return nil, err
	}

	if !container.IsRunning() {
		return nil, derr.ErrorCodeNotRunning.WithArgs(name)
	}
	if container.isPaused() {
		return nil, derr.ErrorCodeExecPaused.WithArgs(name)
	}
	return container, nil
}

// ContainerExecCreate sets up an exec in a running container.
func (d *Daemon) ContainerExecCreate(config *runconfig.ExecConfig) (string, error) {
	container, err := d.getActiveContainer(config.Container)
	if err != nil {
		return "", err
	}

	cmd := stringutils.NewStrSlice(config.Cmd...)
	entrypoint, args := d.getEntrypointAndArgs(stringutils.NewStrSlice(), cmd)

	processConfig := &execdriver.ProcessConfig{
		CommonProcessConfig: execdriver.CommonProcessConfig{
			Tty:        config.Tty,
			Entrypoint: entrypoint,
			Arguments:  args,
		},
	}
	setPlatformSpecificExecProcessConfig(config, container, processConfig)

	ExecConfig := &ExecConfig{
		ID:            stringid.GenerateNonCryptoID(),
		OpenStdin:     config.AttachStdin,
		OpenStdout:    config.AttachStdout,
		OpenStderr:    config.AttachStderr,
		streamConfig:  streamConfig{},
		ProcessConfig: processConfig,
		Container:     container,
		Running:       false,
		waitStart:     make(chan struct{}),
	}

	d.registerExecCommand(ExecConfig)

	return ExecConfig.ID, nil
}

// ContainerExecStart starts a previously set up exec instance. The
// std streams are set up.
func (d *Daemon) ContainerExecStart(name string, stdin io.ReadCloser, stdout io.Writer, stderr io.Writer) error {
	var (
		cStdin           io.ReadCloser
		cStdout, cStderr io.Writer
	)

	ec, err := d.getExecConfig(name)
	if err != nil {
		return derr.ErrorCodeNoExecID.WithArgs(name)
	}

	ec.Lock()
	if ec.Running {
		ec.Unlock()
		return derr.ErrorCodeExecRunning.WithArgs(ec.ID)
	}
	ec.Running = true
	ec.Unlock()

	logrus.Debugf("starting exec command %s in container %s", ec.ID, ec.Container.ID)
	container := ec.Container

	if ec.OpenStdin {
		r, w := io.Pipe()
		go func() {
			defer w.Close()
			defer logrus.Debugf("Closing buffered stdin pipe")
			pools.Copy(w, stdin)
		}()
		cStdin = r
	}
	if ec.OpenStdout {
		cStdout = stdout
	}
	if ec.OpenStderr {
		cStderr = stderr
	}

	ec.streamConfig.stderr = new(broadcaster.Unbuffered)
	ec.streamConfig.stdout = new(broadcaster.Unbuffered)
	// Attach to stdin
	if ec.OpenStdin {
		ec.streamConfig.stdin, ec.streamConfig.stdinPipe = io.Pipe()
	} else {
		ec.streamConfig.stdinPipe = ioutils.NopWriteCloser(ioutil.Discard) // Silently drop stdin
	}

	attachErr := attach(&ec.streamConfig, ec.OpenStdin, true, ec.ProcessConfig.Tty, cStdin, cStdout, cStderr)

	execErr := make(chan error)

	// Note, the ExecConfig data will be removed when the container
	// itself is deleted.  This allows us to query it (for things like
	// the exitStatus) even after the cmd is done running.

	go func() {
		execErr <- d.containerExec(container, ec)
	}()

	select {
	case err := <-attachErr:
		if err != nil {
			return derr.ErrorCodeExecAttach.WithArgs(err)
		}
		return nil
	case err := <-execErr:
		if aErr := <-attachErr; aErr != nil && err == nil {
			return derr.ErrorCodeExecAttach.WithArgs(aErr)
		}
		if err == nil {
			return nil
		}

		// Maybe the container stopped while we were trying to exec
		if !container.IsRunning() {
			return derr.ErrorCodeExecContainerStopped
		}
		return derr.ErrorCodeExecCantRun.WithArgs(ec.ID, container.ID, err)
	}
}

// Exec calls the underlying exec driver to run
func (d *Daemon) Exec(c *Container, ExecConfig *ExecConfig, pipes *execdriver.Pipes, startCallback execdriver.DriverCallback) (int, error) {
	return 0, nil
}

// execCommandGC runs a ticker to clean up the daemon references
// of exec configs that are no longer part of the container.
func (d *Daemon) execCommandGC() {
}

// containerExecIds returns a list of all the current exec ids that are in use
// and running inside a container.
func (d *Daemon) containerExecIds() map[string]struct{} {
	return nil
}

func (d *Daemon) containerExec(container *Container, ec *ExecConfig) error {
	container.Lock()
	defer container.Unlock()

	callback := func(processConfig *execdriver.ProcessConfig, pid int, chOOM <-chan struct{}) error {
		if processConfig.Tty {
			// The callback is called after the process Start()
			// so we are in the parent process. In TTY mode, stdin/out/err is the PtySlave
			// which we close here.
			if c, ok := processConfig.Stdout.(io.Closer); ok {
				c.Close()
			}
		}
		close(ec.waitStart)
		return nil
	}

	// We use a callback here instead of a goroutine and an chan for
	// synchronization purposes
	cErr := promise.Go(func() error { return d.monitorExec(container, ec, callback) })

	// Exec should not return until the process is actually running
	select {
	case <-ec.waitStart:
	case err := <-cErr:
		return err
	}

	return nil
}

func (d *Daemon) monitorExec(container *Container, ExecConfig *ExecConfig, callback execdriver.DriverCallback) error {
	return nil
}

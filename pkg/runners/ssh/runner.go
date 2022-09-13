package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/exograd/eventline/pkg/eventline"
	"github.com/exograd/go-daemon/dlog"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Runner struct {
	runner *eventline.Runner
	log    *dlog.Logger

	rootPath string

	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

func RunnerDef() *eventline.RunnerDef {
	return &eventline.RunnerDef{
		Name: "ssh",
		Cfg: &RunnerCfg{
			RootDirectory: "/tmp/eventline/execution",
		},
		InstantiateParameters: NewRunnerParameters,
		InstantiateBehaviour:  NewRunner,
	}
}

func NewRunner(r *eventline.Runner) eventline.RunnerBehaviour {
	cfg := r.Cfg.(*RunnerCfg)

	je := r.JobExecution

	rootDirPath := cfg.RootDirectory
	rootPath := path.Join(rootDirPath, je.Id.String())

	return &Runner{
		runner: r,
		log:    r.Log,

		rootPath: rootPath,
	}
}

func (r *Runner) DirPath() string {
	return r.rootPath
}

func (r *Runner) Init(ctx context.Context) error {
	sshClient, err := r.connect(ctx)
	if err != nil {
		return err
	}
	r.sshClient = sshClient

	r.sftpClient, err = sftp.NewClient(r.sshClient)
	if err != nil {
		return fmt.Errorf("cannot create sftp client: %w", err)
	}

	if err := r.uploadFileSet(ctx); err != nil {
		return err
	}

	return nil
}

func (r *Runner) Terminate() {
	cfg := r.runner.Cfg.(*RunnerCfg)

	if r.sftpClient != nil {
		// Note that we delete all files *in* the root directory, but not the
		// root directory itself; it is provided by the user, and could for
		// example have specific permissions.
		if err := r.deleteDirectoryContent(cfg.RootDirectory); err != nil {
			r.log.Error("cannot delete directory %q: %v", r.rootPath, err)
		}

		r.sftpClient.Close()
	}

	if r.sshClient != nil {
		r.sshClient.Close()
	}
}

func (r *Runner) ExecuteStep(ctx context.Context, se *eventline.StepExecution, step *eventline.Step, stdout, stderr io.WriteCloser) error {
	// Create and initialize a new session
	session, err := r.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("cannot open session: %w", err)
	}

	session.Stdout = stdout
	session.Stderr = stderr

	for k, v := range r.runner.Environment {
		if err := session.Setenv(k, v); err != nil {
			return fmt.Errorf("cannot set environment variable %q: %w", k, err)
		}
	}

	// Run the command and wait for completion
	cmd := r.runner.StepCommandString(se, step, r.rootPath)

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("cannot start command: %w", err)
	}

	errChan := make(chan error)

	go func() {
		errChan <- session.Wait()
		close(errChan)
	}()

	select {
	case err = <-errChan:
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			err = eventline.NewStepFailureError(r.translateExitError(exitErr))
		}

	case <-ctx.Done():
		if err := session.Signal(ssh.SIGKILL); err != nil {
			r.log.Error("cannot kill program: %v", err)
		}

		err = context.Canceled
	}

	// Cleanup
	session.Close()

	return err
}

func (r *Runner) translateExitError(err *ssh.ExitError) error {
	if code := err.ExitStatus(); code != 0 {
		return fmt.Errorf("program exited with status %d", code)
	} else if sigName := err.Signal(); sigName != "" {
		return fmt.Errorf("program killed by signal %s", sigName)
	}

	return err
}

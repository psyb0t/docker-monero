package commander

import (
	"io"
)

type Option func(*Options)

type Options struct {
	Stdin io.Reader
	Env   []string
	Dir   string

	StreamStdout chan<- string
	StreamStderr chan<- string
}

func WithStdin(stdin io.Reader) Option {
	return func(o *Options) {
		o.Stdin = stdin
	}
}

func WithEnv(env []string) Option {
	return func(o *Options) {
		o.Env = env
	}
}

func WithDir(dir string) Option {
	return func(o *Options) {
		o.Dir = dir
	}
}

// WithStream subscribes stdout and stderr channels to Start before the process
// launches, so no line can be read before the subscriber exists. Process.Stream
// called after Start can miss lines a fast command already wrote. Pass nil for
// a stream you do not want. The channels are closed when the process is done,
// as with Process.Stream. Run, Output and CombinedOutput ignore this option.
func WithStream(stdout, stderr chan<- string) Option {
	return func(o *Options) {
		o.StreamStdout = stdout
		o.StreamStderr = stderr
	}
}

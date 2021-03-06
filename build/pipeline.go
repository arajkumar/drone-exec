package build

import (
	"bufio"
	"time"

	"github.com/drone/drone-exec/yaml"
)

// element represents a link in the linked list.
type element struct {
	*yaml.Container
	next *element
}

// Pipeline represents a build pipeline.
type Pipeline struct {
	conf *yaml.Config
	head *element
	tail *element
	pipe chan (*Line)
	next chan (error)
	done chan (error)
	err  error

	containers []string
	volumes    []string
	networks   []string

	engine Engine
}

// Done returns when the process is done executing.
func (p *Pipeline) Done() <-chan error {
	return p.done
}

// Err returns the error for the current process.
func (p *Pipeline) Err() error {
	return p.err
}

// Next returns the next step in the process.
func (p *Pipeline) Next() <-chan error {
	return p.next
}

// Exec executes the current step.
func (p *Pipeline) Exec() {
	go func() {
		err := p.exec(p.head.Container)
		if err != nil {
			p.err = err
		}
		p.step()
	}()
}

// Skip skips the current step.
func (p *Pipeline) Skip() {
	p.step()
}

// Pipe returns the build output pipe.
func (p *Pipeline) Pipe() <-chan *Line {
	return p.pipe
}

// Head returns the head item in the list.
func (p *Pipeline) Head() *yaml.Container {
	return p.head.Container
}

// Tail returns the tail item in the list.
func (p *Pipeline) Tail() *yaml.Container {
	return p.tail.Container
}

// Stop stops the pipeline.
func (p *Pipeline) Stop() {
	go func() {
		p.done <- ErrTerm
	}()
}

// Setup prepares the build pipeline environment.
func (p *Pipeline) Setup() error {
	return nil
}

// Teardown removes the pipeline environment.
func (p *Pipeline) Teardown() {
	for _, id := range p.containers {
		p.engine.ContainerRemove(id)
	}
	close(p.next)
	close(p.done)

	// TODO we have a race condition here where the program can try to async
	// write to a closed pipe channel. This package, in general, needs to be
	// tested for race conditions.
	// close(p.pipe)
}

// step steps through the pipeline to head.next
func (p *Pipeline) step() {
	if p.head == p.tail {
		go func() {
			p.done <- nil
		}()
	} else {
		go func() {
			p.head = p.head.next
			p.next <- nil
		}()
	}
}

// close closes open channels and signals the pipeline is done.
func (p *Pipeline) close(err error) {
	go func() {
		p.done <- err
	}()
}

func (p *Pipeline) exec(c *yaml.Container) error {
	name, err := p.engine.ContainerStart(c)
	if err != nil {
		return err
	}
	p.containers = append(p.containers, name)

	go func() {
		rc, rerr := p.engine.ContainerLogs(name)
		if rerr != nil {
			return
		}
		defer rc.Close()

		num := 0
		now := time.Now().UTC()
		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			p.pipe <- &Line{
				Proc: c.Name,
				Time: int64(time.Since(now).Seconds()),
				Pos:  num,
				Out:  scanner.Text(),
			}
			num++
		}
	}()

	// exit when running container in detached mode in background
	if c.Detached {
		return nil
	}

	state, err := p.engine.ContainerWait(name)
	if err != nil {
		return err
	}
	if state.OOMKilled {
		return &OomError{c.Name}
	} else if state.ExitCode != 0 {
		return &ExitError{c.Name, state.ExitCode}
	}
	return nil
}

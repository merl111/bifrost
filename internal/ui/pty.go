package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

const (
	defaultPTYWidth  = 80
	defaultPTYHeight = 24
)

var ptyWakeMarker = []byte{0x00, 0xff, 0x00, 0xff}

type ptyOutputMsg struct {
	session *ptySession
	data    []byte
	err     error
}

type ptyWrite struct {
	data []byte
	done chan error
}

// ptySession owns one interactive child and its emulator. All writes and
// resizes are serialized because an os.File is shared by the read command and
// Bubble Tea's update loop.
type ptySession struct {
	mu         sync.Mutex
	writeMu    sync.Mutex
	ops        sync.WaitGroup
	file       *os.File
	cmd        *exec.Cmd
	term       *vt.SafeEmulator
	inputErr   error
	closed     bool
	queueDone  bool
	writeQ     chan ptyWrite
	writerDone chan struct{}
	inputDone  chan struct{}
}

func startPTY(root string, width, height int, shell string) (*ptySession, error) {
	if width < 1 {
		width = defaultPTYWidth
	}
	if height < 1 {
		height = defaultPTYHeight
	}
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd.exe"
		} else {
			shell = "/bin/sh"
		}
	}
	return startPTYArgv(root, width, height, []string{shell})
}

func startPTYArgv(root string, width, height int, argv []string) (*ptySession, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("terminal command is empty")
	}
	for _, arg := range argv {
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, fmt.Errorf("terminal command contains NUL")
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return nil, fmt.Errorf("start terminal: %w", err)
	}
	file, err = preparePTY(file)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p := &ptySession{
		file:       file,
		cmd:        cmd,
		term:       vt.NewSafeEmulator(width, height),
		writeQ:     make(chan ptyWrite, 32),
		writerDone: make(chan struct{}),
		inputDone:  make(chan struct{}),
	}
	go p.forwardWrites()
	go p.forwardInput()
	return p, nil
}

func (p *ptySession) forwardWrites() {
	defer close(p.writerDone)
	for request := range p.writeQ {
		_, err := p.file.Write(request.data)
		request.done <- err
		close(request.done)
	}
}

func (p *ptySession) forwardInput() {
	defer close(p.inputDone)
	buf := make([]byte, 4096)
	for {
		n, err := p.term.Read(buf)
		if n > 0 {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				// Shutdown writes a marker through the same pipe. Discard stale
				// input until the marker is consumed, then let Close safely close
				// the emulator after this goroutine exits.
				if bytes.Contains(buf[:n], ptyWakeMarker) {
					return
				}
				continue
			}
			_, _ = p.enqueueWrite(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *ptySession) read(ctx context.Context) ptyOutputMsg {
	if p == nil {
		return ptyOutputMsg{err: io.EOF}
	}
	buf := make([]byte, 32*1024)
	// A PTY read is intentionally one bounded read. Bubble Tea calls this in a
	// command, so output is coalesced naturally without holding Update hostage.
	n, err := p.file.Read(buf)
	if n > 0 {
		data := append([]byte(nil), buf[:n]...)
		if p.beginOp() {
			// Emulator parsing can synchronously emit a device response into
			// the input pipe. Do not hold p.mu while it may block on the
			// forwarding goroutine.
			_, _ = p.term.Write(data)
			p.ops.Done()
		}
		select {
		case <-ctx.Done():
			return ptyOutputMsg{err: ctx.Err()}
		default:
		}
		return ptyOutputMsg{data: data, err: err}
	}
	return ptyOutputMsg{err: err}
}

func (p *ptySession) write(data []byte) error {
	if p == nil || len(data) == 0 {
		return nil
	}
	return p.writeRaw(data)
}

func (p *ptySession) sendKey(key uv.KeyEvent) error {
	if p == nil {
		return os.ErrClosed
	}
	if !p.beginOp() {
		return os.ErrClosed
	}
	defer p.ops.Done()
	p.term.SendKey(key)
	return nil
}

func (p *ptySession) beginOp() bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.ops.Add(1)
	p.mu.Unlock()
	return true
}

func (p *ptySession) paste(text string) error {
	if p == nil {
		return os.ErrClosed
	}
	if !p.beginOp() {
		return os.ErrClosed
	}
	defer p.ops.Done()
	// Do not hold p.mu while writing. Paste uses an io.Pipe and may block until
	// the forwarding goroutine drains several chunks into the child.
	p.term.Paste(text)
	return nil
}

func (p *ptySession) writeRaw(data []byte) error {
	done, err := p.enqueueWrite(data)
	if err != nil {
		return err
	}
	return <-done
}

// The forwarding reader must drain the emulator independently of PTY writes:
// a child may stop reading while the UI still needs to render or close it.
func (p *ptySession) enqueueWrite(data []byte) (<-chan error, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, os.ErrClosed
	}
	request := ptyWrite{data: append([]byte(nil), data...), done: make(chan error, 1)}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.queueDone {
		return nil, os.ErrClosed
	}
	select {
	case p.writeQ <- request:
		return request.done, nil
	default:
		err := fmt.Errorf("terminal input buffer full; some input was not delivered")
		p.mu.Lock()
		p.inputErr = err
		p.mu.Unlock()
		return nil, err
	}
}

func (p *ptySession) resize(width, height int) error {
	if p == nil {
		return nil
	}
	if width < 1 || height < 1 {
		return nil
	}
	if !p.beginOp() {
		return os.ErrClosed
	}
	defer p.ops.Done()
	if err := resizePTY(p.file, width, height); err != nil {
		return err
	}
	p.term.Resize(width, height)
	return nil
}

func (p *ptySession) snapshot() string {
	if p == nil || !p.beginOp() {
		return ""
	}
	defer p.ops.Done()
	return p.term.Render()
}

func (p *ptySession) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	file := p.file
	cmd := p.cmd
	p.mu.Unlock()
	foregroundPGID := foregroundProcessGroup(file)
	// Stop the child before waiting on any I/O: a blocking PTY Write may not
	// be interrupted by Close alone on every supported Unix implementation.
	processErr := terminatePTYProcess(cmd, foregroundPGID)
	if file != nil {
		// Interrupt a forwarding write first. This guarantees the goroutine can
		// return to term.Read before the wake byte below is written.
		_ = file.Close()
	}
	// Closing the PTY must precede waiting: an in-flight paste can be blocked
	// behind a child that has stopped reading its input.
	p.ops.Wait()
	p.writeMu.Lock()
	p.queueDone = true
	close(p.writeQ)
	p.writeMu.Unlock()
	// SafeEmulator.Read is not cancellation-aware. Wake its io.Pipe reader
	// with a marker that forwardInput recognizes, then wait for that goroutine.
	_, _ = p.term.InputPipe().Write(ptyWakeMarker)
	<-p.inputDone
	<-p.writerDone
	_ = p.term.Close()
	return processErr
}

func (p *ptySession) inputError() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inputErr
}

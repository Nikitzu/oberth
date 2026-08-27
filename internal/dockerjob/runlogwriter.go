package dockerjob

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// maxStepLogBytes and DefaultMaxRunLogBytes are the Argo engine's budgets,
	// repeated here so a run costs the same disk under either engine.
	maxStepLogBytes = 32 << 20

	DefaultMaxRunLogBytes = 64 << 20
)

// errRunLogBudget is returned once a run's aggregate log ceiling is reached.
var errRunLogBudget = errors.New("dockerjob: aggregate run log budget exceeded")

// runBudgetWriter caps a run's total log bytes.
type runBudgetWriter struct {
	mu          sync.Mutex
	destination io.Writer
	remaining   int64
}

func newRunBudgetWriter(destination io.Writer, budget int64) *runBudgetWriter {
	if budget <= 0 {
		budget = DefaultMaxRunLogBytes
	}
	return &runBudgetWriter{destination: destination, remaining: budget}
}

func (writer *runBudgetWriter) Write(body []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.remaining <= 0 {
		return 0, errRunLogBudget
	}
	allowed := len(body)
	limited := int64(allowed) > writer.remaining
	if limited {
		allowed = int(writer.remaining)
	}
	written, err := writer.destination.Write(body[:allowed])
	writer.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	if written != allowed {
		return written, io.ErrShortWrite
	}
	if limited {
		return written, errRunLogBudget
	}
	return written, nil
}

// stepLogWriter prefixes every line with "[burn/step] ", which is the marker
// internal/runlog indexes on, and enforces the per-step ceiling.
//
// Unlike the Argo engine, which replays a finished pod's log in one piece on
// terminal transition, this writes lines through as the container emits them.
// A container's log is available live from the daemon, so there is no reason
// to hold it, and one container runs at a time here, so interleaving cannot
// produce output no reader can attribute. The practical difference is that a
// running step's output appears in the dashboard while it runs.
type stepLogWriter struct {
	destination io.Writer
	prefix      []byte
	buffered    []byte
	written     int64
	truncated   bool
}

func newStepLogWriter(destination io.Writer, burn, step string) *stepLogWriter {
	return &stepLogWriter{destination: destination, prefix: []byte("[" + burn + "/" + step + "] ")}
}

func (writer *stepLogWriter) Write(body []byte) (int, error) {
	consumed := len(body)
	writer.buffered = append(writer.buffered, body...)
	for {
		index := indexNewline(writer.buffered)
		if index < 0 {
			break
		}
		line := trimCarriageReturn(writer.buffered[:index])
		writer.buffered = writer.buffered[index+1:]
		if err := writer.emit(line); err != nil {
			return consumed, err
		}
	}
	// A pathological producer emitting no newline at all must not accumulate
	// without bound; flush what is held once it passes a line's worth.
	if len(writer.buffered) > 1<<20 {
		held := writer.buffered
		writer.buffered = nil
		if err := writer.emit(held); err != nil {
			return consumed, err
		}
	}
	return consumed, nil
}

// Close flushes a trailing line that never carried a newline.
func (writer *stepLogWriter) Close() error {
	if len(writer.buffered) == 0 {
		return nil
	}
	held := trimCarriageReturn(writer.buffered)
	writer.buffered = nil
	return writer.emit(held)
}

func (writer *stepLogWriter) emit(line []byte) error {
	if writer.truncated {
		return nil
	}
	assembled := make([]byte, 0, len(writer.prefix)+len(line)+1)
	assembled = append(assembled, writer.prefix...)
	assembled = append(assembled, line...)
	assembled = append(assembled, '\n')
	if writer.written+int64(len(assembled)) > maxStepLogBytes {
		writer.truncated = true
		writer.note(fmt.Sprintf("oberth: step log truncated: exceeds the %d byte budget", maxStepLogBytes))
		return nil
	}
	writer.written += int64(len(assembled))
	_, err := writer.destination.Write(assembled)
	if errors.Is(err, errRunLogBudget) {
		writer.truncated = true
		return err
	}
	return err
}

// note writes one engine-authored line attributed to this step, bypassing the
// per-step budget so a truncation banner is never itself truncated.
func (writer *stepLogWriter) note(text string) {
	assembled := make([]byte, 0, len(writer.prefix)+len(text)+1)
	assembled = append(assembled, writer.prefix...)
	assembled = append(assembled, text...)
	assembled = append(assembled, '\n')
	_, _ = writer.destination.Write(assembled)
}

func indexNewline(body []byte) int {
	for index, value := range body {
		if value == '\n' {
			return index
		}
	}
	return -1
}

func trimCarriageReturn(line []byte) []byte {
	if len(line) != 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

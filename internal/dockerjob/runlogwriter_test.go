package dockerjob

import (
	"bytes"
	"strings"
	"testing"
)

// Every line a step emits must carry its own [burn/step] prefix, because that
// prefix is what internal/runlog indexes on: a line without one cannot be
// attributed to a step in the dashboard.
func TestStepLogWriterPrefixesEveryLine(t *testing.T) {
	var sink bytes.Buffer
	writer := newStepLogWriter(&sink, "build", "compile")
	if _, err := writer.Write([]byte("first\r\nsecond\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := writer.Write([]byte("trailing without a newline")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := "[build/compile] first\n[build/compile] second\n[build/compile] trailing without a newline\n"
	if sink.String() != want {
		t.Fatalf("got %q, want %q", sink.String(), want)
	}
}

// A step that never emits a newline must not be buffered without bound.
func TestStepLogWriterFlushesAProducerThatNeverEndsALine(t *testing.T) {
	var sink bytes.Buffer
	writer := newStepLogWriter(&sink, "build", "compile")
	if _, err := writer.Write(bytes.Repeat([]byte("x"), (1<<20)+1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sink.Len() == 0 {
		t.Fatal("a newline-free producer was buffered without bound")
	}
}

// The per-step ceiling stops a runaway step, and says so in the step's own
// log rather than silently dropping output.
func TestStepLogWriterStopsAtThePerStepCeiling(t *testing.T) {
	var sink bytes.Buffer
	writer := newStepLogWriter(&sink, "build", "compile")
	line := append(bytes.Repeat([]byte("y"), 4095), '\n')
	for written := 0; written < maxStepLogBytes+(1<<20); written += len(line) {
		if _, err := writer.Write(line); err != nil {
			break
		}
	}
	_ = writer.Close()
	if sink.Len() > maxStepLogBytes+(1<<12) {
		t.Fatalf("the per-step ceiling did not hold: %d bytes", sink.Len())
	}
	if !strings.Contains(sink.String(), "step log truncated") {
		t.Fatal("truncation happened without saying so")
	}
}

// The aggregate run ceiling holds across steps, and reports itself so the
// caller can stop rather than spin.
func TestRunBudgetWriterStopsAtTheRunCeiling(t *testing.T) {
	var sink bytes.Buffer
	budget := newRunBudgetWriter(&sink, 32)
	written, err := budget.Write(bytes.Repeat([]byte("a"), 20))
	if written != 20 || err != nil {
		t.Fatalf("first write: %d %v", written, err)
	}
	written, err = budget.Write(bytes.Repeat([]byte("b"), 20))
	if written != 12 {
		t.Fatalf("the run ceiling let %d bytes through, budget had 12 left", written)
	}
	if err == nil {
		t.Fatal("exceeding the run budget was not reported")
	}
	if sink.Len() != 32 {
		t.Fatalf("sink holds %d bytes, ceiling is 32", sink.Len())
	}
	if _, err := budget.Write([]byte("more")); err == nil {
		t.Fatal("a write past an exhausted budget succeeded")
	}
}

// A step that hits the run ceiling stops writing rather than looping on a
// writer that refuses every byte.
func TestStepLogWriterStopsWhenTheRunBudgetIsGone(t *testing.T) {
	var sink bytes.Buffer
	budget := newRunBudgetWriter(&sink, 40)
	writer := newStepLogWriter(budget, "build", "compile")
	for index := 0; index < 100; index++ {
		if _, err := writer.Write([]byte("a line of output\n")); err != nil {
			break
		}
	}
	if sink.Len() > 40 {
		t.Fatalf("the run ceiling did not hold through the step writer: %d bytes", sink.Len())
	}
	if !writer.truncated {
		t.Fatal("the step writer did not stop after the run budget was exhausted")
	}
}

package dbmigrate

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

type fakeExecutor struct {
	upErr, stepsErr error
	upCalls         int
	steps           []int
}

type fakeLifecycle struct {
	fakeExecutor
	sourceCloseErr, databaseCloseErr error
	closeCalls                       int
}

func (f *fakeLifecycle) Close() (error, error) {
	f.closeCalls++
	return f.sourceCloseErr, f.databaseCloseErr
}

func (f *fakeExecutor) Up() error         { f.upCalls++; return f.upErr }
func (f *fakeExecutor) Steps(n int) error { f.steps = append(f.steps, n); return f.stepsErr }

func TestParseDirection(t *testing.T) {
	tests := []struct {
		args    []string
		want    Direction
		wantErr string
	}{
		{[]string{"up"}, Up, ""},
		{[]string{"down"}, Down, ""},
		{nil, "", "usage: migrate up|down"},
		{[]string{"sideways"}, "", `unknown migration direction "sideways" (want up|down)`},
		{[]string{"up", "extra"}, "", "usage: migrate up|down"},
	}
	for _, tt := range tests {
		got, err := ParseDirection(tt.args)
		if got != tt.want || (err != nil && err.Error() != tt.wantErr) || (err == nil && tt.wantErr != "") {
			t.Fatalf("ParseDirection(%v) = (%q, %v), want (%q, %q)", tt.args, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRunConstructedLifecycle(t *testing.T) {
	factoryErr := errors.New("source factory failed")
	if err := runConstructed(Up, func() (lifecycle, error) { return nil, factoryErr }); !errors.Is(err, factoryErr) {
		t.Fatalf("factory error = %v", err)
	}

	success := &fakeLifecycle{}
	if err := runConstructed(Up, func() (lifecycle, error) { return success, nil }); err != nil || success.closeCalls != 1 {
		t.Fatalf("success = err:%v closes:%d", err, success.closeCalls)
	}

	applyErr := errors.New("apply failed")
	sourceErr := errors.New("source close failed")
	databaseErr := errors.New("database close failed")
	failed := &fakeLifecycle{fakeExecutor: fakeExecutor{upErr: applyErr}, sourceCloseErr: sourceErr, databaseCloseErr: databaseErr}
	err := runConstructed(Up, func() (lifecycle, error) { return failed, nil })
	for _, want := range []error{applyErr, sourceErr, databaseErr} {
		if !errors.Is(err, want) {
			t.Fatalf("joined error %v does not preserve %v", err, want)
		}
	}
	if failed.closeCalls != 1 {
		t.Fatalf("close calls = %d", failed.closeCalls)
	}
}

func TestApplyDirectionAndNoChange(t *testing.T) {
	up := &fakeExecutor{upErr: migrate.ErrNoChange}
	if err := Apply(Up, up); err != nil || up.upCalls != 1 {
		t.Fatalf("up = calls:%d err:%v", up.upCalls, err)
	}
	down := &fakeExecutor{stepsErr: migrate.ErrNoChange}
	if err := Apply(Down, down); err != nil || len(down.steps) != 1 || down.steps[0] != -1 {
		t.Fatalf("down = steps:%v err:%v", down.steps, err)
	}
	want := errors.New("boom")
	failed := &fakeExecutor{upErr: want}
	if err := Apply(Up, failed); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

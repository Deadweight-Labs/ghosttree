package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRuntimeWriterFIFOAndCompletion(t *testing.T) {
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	entered, release := make(chan struct{}), make(chan struct{})
	var order []int
	first, err := w.admit(context.Background(), 0, func() error {
		close(entered)
		<-release
		order = append(order, 0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	requests := []*writerRequest{first}
	for i := 1; i <= 8; i++ {
		r, err := w.admit(context.Background(), 0, func() error {
			order = append(order, i)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	for _, r := range requests {
		select {
		case <-r.done:
			t.Error("acknowledged before execution completed")
		default:
		}
	}
	close(release)
	for _, r := range requests {
		if err := <-r.done; err != nil {
			t.Fatal(err)
		}
	}
	if want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}; !reflect.DeepEqual(order, want) {
		t.Fatalf("execution order = %v, want %v", order, want)
	}
}

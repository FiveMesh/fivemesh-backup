//go:build windows

package securepipe

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPasswordPipeRoundTrip(t *testing.T) {
	name := fmt.Sprintf("fivemesh-test-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	secretBytes := []byte{'p', 0, 'a', 0, 's', 0, 's', 0}
	server := make(chan error, 1)
	go func() {
		server <- ServePasswordPipe(ctx, name, secretBytes)
	}()
	password, err := ReadPassword(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if password != "pass" {
		t.Fatalf("password = %q", password)
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func TestPasswordPipeCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := ServePasswordPipe(ctx, fmt.Sprintf("fivemesh-test-%d", time.Now().UnixNano()), []byte{'x', 0})
	if err == nil {
		t.Fatal("unconnected password pipe did not honor cancellation")
	}
}

func TestPasswordPipeRejectsBadData(t *testing.T) {
	if err := ServePasswordPipe(context.Background(), "../bad", []byte{'x', 0}); err == nil {
		t.Fatal("invalid pipe name was accepted")
	}
	if err := ServePasswordPipe(context.Background(), "fivemesh-empty", nil); err == nil {
		t.Fatal("empty password was accepted")
	}
}

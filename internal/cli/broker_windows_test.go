//go:build windows

package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPasswordBrokerRoundTrip(t *testing.T) {
	name := fmt.Sprintf("fivemesh-broker-test-%d", time.Now().UnixNano())
	secretBytes := []byte{'s', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0}
	var stderr bytes.Buffer
	broker := make(chan error, 1)
	go func() {
		broker <- runPasswordBroker(
			context.Background(),
			[]string{"--pipe", name, "--timeout", "5s"},
			bytes.NewReader(secretBytes),
			&stderr,
		)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	password, err := readWindowsPasswordPipe(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if password != "secret" {
		t.Fatalf("password = %q", password)
	}
	if err := <-broker; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "secret") {
		t.Fatal("password broker wrote secret material to stderr")
	}
}

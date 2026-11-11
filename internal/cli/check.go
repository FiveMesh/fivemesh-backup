package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
)

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("check", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile to test")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("check does not accept positional arguments"))
	}
	_, name, profile, password, err := loadSelectedProfile(paths, *profileName)
	if err != nil {
		return err
	}
	client, err := mysql.DiscoverClient(ctx, profile.Database)
	if err != nil {
		return err
	}
	if err := mysql.TestConnection(ctx, client, profile.Database, password); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Connection OK for profile %q.\nClient: %s\nVersion: %s\n",
		name,
		client.Path,
		client.Version,
	)
	fmt.Fprintln(stdout, "This verifies authentication and basic connectivity; dump privileges are validated by a real backup.")
	return nil
}

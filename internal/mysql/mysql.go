package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/winsec"
)

const (
	maxDiagnosticBytes = 16 * 1024
	versionTimeout     = 5 * time.Second
	connectionTimeout  = 10 * time.Second
	processWaitDelay   = 5 * time.Second
)

type ToolInfo struct {
	Path    string
	Version string
}

type Tools struct {
	Dump   ToolInfo
	Client ToolInfo
}

type CommandError struct {
	Operation string
	ExitCode  int
	Err       error
	Detail    string
}

func (e *CommandError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s failed (exit code %d): %v", e.Operation, e.ExitCode, e.Err)
	}
	return fmt.Sprintf("%s failed (exit code %d): %v: %s", e.Operation, e.ExitCode, e.Err, e.Detail)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

type DumpResult struct {
	ExitCode int
	Bytes    int64
}

func Discover(ctx context.Context, database config.Database) (Tools, error) {
	tools, err := discoverConfigured(ctx, database)
	if err == nil {
		return tools, nil
	}
	// Never silently replace explicit tool paths chosen by the user.
	if !isAutomaticTool(database.DumpTool) || !isAutomaticTool(database.ClientTool) {
		return Tools{}, err
	}
	// PATH is frequently incomplete on Windows, even for an installed server.
	// Probe only conventional installation locations, without recursive scans.
	for _, directory := range installedClientDirectories() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Tools{}, ctxErr
		}
		tools, candidateErr := DiscoverInDirectory(ctx, database, directory)
		if candidateErr == nil {
			return tools, nil
		}
	}
	return Tools{}, fmt.Errorf("%w; also searched common installation directories", err)
}

func isAutomaticTool(tool string) bool {
	return tool == "" || strings.EqualFold(tool, "auto")
}

func toolCandidates(flavor string) ([]string, []string) {
	if flavor == "mysql" {
		return []string{"mysqldump", "mariadb-dump"}, []string{"mysql", "mariadb"}
	}
	return []string{"mariadb-dump", "mysqldump"}, []string{"mariadb", "mysql"}
}

func discoverConfigured(ctx context.Context, database config.Database) (Tools, error) {
	dumpCandidates, clientCandidates := toolCandidates(database.Flavor)
	dump, err := discoverOne(ctx, database.DumpTool, dumpCandidates, "database dump client")
	if err != nil {
		return Tools{}, err
	}
	client, err := discoverClient(ctx, database, clientCandidates)
	if err != nil {
		return Tools{}, err
	}
	return Tools{Dump: dump, Client: client}, nil
}

// DiscoverInDirectory validates a complete dump/client pair in a user-specified
// installation root or bin directory and returns absolute executable paths.
// Nothing is installed or added to the process/user PATH.
func DiscoverInDirectory(ctx context.Context, database config.Database, installationPath string) (Tools, error) {
	directory := strings.Trim(strings.TrimSpace(installationPath), "\"")
	if !filepath.IsAbs(directory) {
		return Tools{}, fmt.Errorf("installation directory must be an absolute path")
	}
	dumpNames, clientNames := toolCandidates(database.Flavor)
	for _, candidateDir := range []string{directory, filepath.Join(directory, "bin")} {
		info, err := os.Stat(candidateDir)
		if err != nil || !info.IsDir() {
			continue
		}
		dump, dumpErr := discoverOne(ctx, "auto", candidateExecutables(candidateDir, dumpNames), "database dump client")
		if dumpErr != nil {
			continue
		}
		client, clientErr := discoverOne(ctx, "auto", candidateExecutables(candidateDir, clientNames), "database client")
		if clientErr != nil {
			continue
		}
		return Tools{Dump: dump, Client: client}, nil
	}
	return Tools{}, fmt.Errorf("no compatible dump and client executables found under %q (check the installation root or bin folder)", directory)
}

func candidateExecutables(directory string, names []string) []string {
	result := make([]string, 0, len(names)*2)
	for _, name := range names {
		if runtime.GOOS == "windows" {
			result = append(result, filepath.Join(directory, name+".exe"))
		} else {
			result = append(result, filepath.Join(directory, name))
		}
	}
	return result
}

func DiscoverClient(ctx context.Context, database config.Database) (ToolInfo, error) {
	clientCandidates := []string{"mariadb", "mysql"}
	if database.Flavor == "mysql" {
		clientCandidates = []string{"mysql", "mariadb"}
	}
	tool, err := discoverClient(ctx, database, clientCandidates)
	if err == nil || !isAutomaticTool(database.ClientTool) {
		return tool, err
	}
	// Match Discover: PATH is often incomplete on Windows, so also look next
	// to a configured dump tool and in conventional installation folders.
	var directories []string
	if filepath.IsAbs(database.DumpTool) {
		directories = append(directories, filepath.Dir(database.DumpTool))
	}
	directories = append(directories, installedClientDirectories()...)
	for _, directory := range directories {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ToolInfo{}, ctxErr
		}
		candidate, candidateErr := discoverOne(ctx, "auto", candidateExecutables(directory, clientCandidates), "database client")
		if candidateErr == nil {
			return candidate, nil
		}
	}
	return ToolInfo{}, fmt.Errorf("%w; also searched common installation directories", err)
}

func discoverClient(ctx context.Context, database config.Database, candidates []string) (ToolInfo, error) {
	return discoverOne(
		ctx,
		database.ClientTool,
		candidates,
		"database client",
	)
}

func Dump(ctx context.Context, tool ToolInfo, database config.Database, password string, destination io.Writer) (DumpResult, error) {
	optionsPath, err := createOptionFile(database, password)
	if err != nil {
		return DumpResult{}, err
	}
	defer os.Remove(optionsPath)

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stderr := newLimitedBuffer(maxDiagnosticBytes)
	output := &cancelWriter{destination: destination, cancel: cancel}
	cmd := exec.CommandContext(runContext, tool.Path, buildDumpArgs(optionsPath, database)...)
	configureProcess(cmd)
	cmd.Stdout = output
	cmd.Stderr = stderr
	cmd.WaitDelay = processWaitDelay

	if err := cmd.Start(); err != nil {
		return DumpResult{}, fmt.Errorf("start %s: %w", tool.Path, err)
	}
	cleanup, err := attachProcess(cmd)
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	cleanup()
	detail := sanitizeDiagnostic(stderr.String(), password, optionsPath)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return DumpResult{ExitCode: exitCode(waitErr), Bytes: output.bytes},
			fmt.Errorf("dump process stopped: %w%s", ctxErr, detailSuffix(detail))
	}
	if output.err != nil {
		return DumpResult{ExitCode: exitCode(waitErr), Bytes: output.bytes},
			fmt.Errorf("stream dump output: %w%s", output.err, detailSuffix(detail))
	}
	if waitErr != nil {
		return DumpResult{ExitCode: exitCode(waitErr), Bytes: output.bytes}, &CommandError{
			Operation: "database dump",
			ExitCode:  exitCode(waitErr),
			Err:       waitErr,
			Detail:    detail,
		}
	}
	return DumpResult{ExitCode: 0, Bytes: output.bytes}, nil
}

func TestConnection(ctx context.Context, tool ToolInfo, database config.Database, password string) error {
	optionsPath, err := createOptionFile(database, password)
	if err != nil {
		return err
	}
	defer os.Remove(optionsPath)

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connectionTimeout)
		defer cancel()
	}

	stderr := newLimitedBuffer(maxDiagnosticBytes)
	cmd := exec.CommandContext(
		ctx,
		tool.Path,
		buildConnectionTestArgs(optionsPath, database.Database)...,
	)
	configureProcess(cmd)
	cmd.Stderr = stderr
	cmd.WaitDelay = processWaitDelay
	output, err := cmd.Output()
	detail := sanitizeDiagnostic(stderr.String(), password, optionsPath)
	if ctx.Err() != nil {
		return fmt.Errorf("connection test timed out or was cancelled: %w%s", ctx.Err(), detailSuffix(detail))
	}
	if err != nil {
		return &CommandError{
			Operation: "connection test",
			ExitCode:  exitCode(err),
			Err:       err,
			Detail:    detail,
		}
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("connection test returned an unexpected result%s", detailSuffix(detail))
	}
	return nil
}

// systemSchemas are server-internal databases that are never offered as
// backup targets in setup.
var systemSchemas = map[string]bool{
	"information_schema": true,
	"performance_schema": true,
	"mysql":              true,
	"sys":                true,
}

// ListDatabases returns the user databases visible to the configured account,
// sorted by name. It is used by setup to offer a database picker; the
// database field of the connection settings is ignored.
func ListDatabases(ctx context.Context, tool ToolInfo, database config.Database, password string) ([]string, error) {
	optionsPath, err := createOptionFile(database, password)
	if err != nil {
		return nil, err
	}
	defer os.Remove(optionsPath)

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connectionTimeout)
		defer cancel()
	}
	stderr := newLimitedBuffer(maxDiagnosticBytes)
	cmd := exec.CommandContext(ctx, tool.Path, buildListDatabasesArgs(optionsPath)...)
	configureProcess(cmd)
	cmd.Stderr = stderr
	cmd.WaitDelay = processWaitDelay
	output, err := cmd.Output()
	detail := sanitizeDiagnostic(stderr.String(), password, optionsPath)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("listing databases timed out or was cancelled: %w%s", ctx.Err(), detailSuffix(detail))
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &CommandError{
				Operation: "connection test",
				ExitCode:  exitCode(err),
				Err:       err,
				Detail:    detail,
			}
		}
		return nil, fmt.Errorf("start database client: %w", err)
	}
	return parseDatabaseList(string(output)), nil
}

func buildListDatabasesArgs(optionsPath string) []string {
	return []string{
		"--defaults-extra-file=" + optionsPath,
		"--batch",
		"--skip-column-names",
		"--connect-timeout=10",
		"--execute=SHOW DATABASES",
	}
}

func parseDatabaseList(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || systemSchemas[strings.ToLower(name)] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildConnectionTestArgs(optionsPath, database string) []string {
	return []string{
		"--defaults-extra-file=" + optionsPath,
		"--database=" + database,
		"--batch",
		"--skip-column-names",
		"--connect-timeout=10",
		"--execute=SELECT 1",
	}
}

var connectionErrorCode = regexp.MustCompile(`\b(1040|1053|2002|2003|2005|2006|2013)\b`)

// IsTransientConnectionError recognizes only availability-related native client
// failures. Authentication, authorization, and database-selection errors fail
// immediately.
func IsTransientConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	message := err.Error()
	return connectionErrorCode.MatchString(message)
}

func Restore(
	ctx context.Context,
	tool ToolInfo,
	database config.Database,
	password string,
	targetDatabase string,
	source io.Reader,
) error {
	if strings.TrimSpace(targetDatabase) == "" || strings.HasPrefix(targetDatabase, "-") {
		return errors.New("restore target database must be non-empty and must not start with '-'")
	}
	optionsPath, err := createOptionFile(database, password)
	if err != nil {
		return err
	}
	defer os.Remove(optionsPath)

	stderr := newLimitedBuffer(maxDiagnosticBytes)
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(
		runContext,
		tool.Path,
		"--defaults-extra-file="+optionsPath,
		"--database="+targetDatabase,
		"--binary-mode",
	)
	configureProcess(cmd)
	input := &cancelReader{source: source, cancel: cancel}
	cmd.Stdin = input
	cmd.Stderr = stderr
	cmd.WaitDelay = processWaitDelay
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start restore client: %w", err)
	}
	cleanup, err := attachProcess(cmd)
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	cleanup()
	detail := sanitizeDiagnostic(stderr.String(), password, optionsPath)
	if ctx.Err() != nil {
		return fmt.Errorf("restore stopped: %w%s", ctx.Err(), detailSuffix(detail))
	}
	if input.err != nil {
		return fmt.Errorf("stream archive into restore client: %w%s", input.err, detailSuffix(detail))
	}
	if waitErr != nil {
		return &CommandError{
			Operation: "database restore",
			ExitCode:  exitCode(waitErr),
			Err:       waitErr,
			Detail:    detail,
		}
	}
	return nil
}

func buildDumpArgs(optionsPath string, database config.Database) []string {
	args := []string{
		"--defaults-extra-file=" + optionsPath,
		"--quick",
		"--triggers",
		"--hex-blob",
	}
	if database.SingleTransaction {
		args = append(args, "--single-transaction", "--skip-lock-tables")
	}
	if database.IncludeRoutines {
		args = append(args, "--routines")
	}
	if database.IncludeEvents {
		args = append(args, "--events")
	}
	args = append(args, database.ExtraDumpArgs...)
	args = append(args, "--", database.Database)
	return args
}

func createOptionFile(database config.Database, password string) (string, error) {
	return createOptionFileInTempDir("", database, password)
}

func createOptionFileInTempDir(directory string, database config.Database, password string) (string, error) {
	file, err := os.CreateTemp(directory, "fivemesh-backup-client-*.cnf")
	if err != nil {
		return "", fmt.Errorf("create temporary client option file: %w", err)
	}
	path := file.Name()
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("restrict temporary client option file: %w", err)
	}
	if err := winsec.ProtectFile(path); err != nil {
		return "", fmt.Errorf("protect temporary client option file: %w", err)
	}

	var content strings.Builder
	content.WriteString("[client]\n")
	fmt.Fprintf(&content, "user=%s\n", quoteOption(database.Username))
	fmt.Fprintf(&content, "password=%s\n", quoteOption(password))
	if database.Host != "" {
		fmt.Fprintf(&content, "host=%s\n", quoteOption(database.Host))
		fmt.Fprintf(&content, "port=%d\n", database.Port)
		content.WriteString("protocol=tcp\n")
	} else {
		fmt.Fprintf(&content, "socket=%s\n", quoteOption(database.Socket))
		content.WriteString("protocol=socket\n")
	}
	if _, err := io.WriteString(file, content.String()); err != nil {
		return "", fmt.Errorf("write temporary client option file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("synchronize temporary client option file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary client option file: %w", err)
	}
	if err := winsec.VerifyFile(path); err != nil {
		return "", fmt.Errorf("verify temporary client option file ACL: %w", err)
	}
	complete = true
	return path, nil
}

func quoteOption(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\t", `\t`,
		"\r", `\r`,
		"\n", `\n`,
	)
	return `"` + replacer.Replace(value) + `"`
}

func discoverOne(ctx context.Context, preferred string, candidates []string, label string) (ToolInfo, error) {
	var names []string
	if preferred != "" && !strings.EqualFold(preferred, "auto") {
		names = []string{preferred}
	} else {
		names = candidates
	}

	var attempts []string
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			attempts = append(attempts, name)
			continue
		}
		version, err := toolVersion(ctx, path)
		if err != nil {
			return ToolInfo{}, fmt.Errorf("validate %s %q: %w", label, path, err)
		}
		return ToolInfo{Path: path, Version: version}, nil
	}
	return ToolInfo{}, fmt.Errorf("%s not found; checked %s", label, strings.Join(attempts, ", "))
}

func toolVersion(parent context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	configureProcess(cmd)
	cmd.WaitDelay = processWaitDelay
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("version check timed out: %w", ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("version check failed: %w", err)
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if line == "" {
		return "", errors.New("version check returned no output")
	}
	if len(line) > 256 {
		line = line[:256]
	}
	return line, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func sanitizeDiagnostic(detail, password, optionsPath string) string {
	detail = strings.TrimSpace(detail)
	if password != "" {
		detail = strings.ReplaceAll(detail, password, "[REDACTED]")
	}
	if optionsPath != "" {
		detail = strings.ReplaceAll(detail, optionsPath, "[CLIENT_OPTIONS_FILE]")
		detail = strings.ReplaceAll(detail, filepath.Base(optionsPath), "[CLIENT_OPTIONS_FILE]")
	}
	if len(detail) > maxDiagnosticBytes {
		detail = detail[len(detail)-maxDiagnosticBytes:]
	}
	return detail
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

type limitedBuffer struct {
	limit int
	data  []byte
}

type cancelWriter struct {
	destination io.Writer
	cancel      context.CancelFunc
	bytes       int64
	err         error
}

func (w *cancelWriter) Write(data []byte) (int, error) {
	n, err := w.destination.Write(data)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
		w.cancel()
	}
	return n, err
}

type cancelReader struct {
	source io.Reader
	cancel context.CancelFunc
	err    error
}

func (r *cancelReader) Read(data []byte) (int, error) {
	n, err := r.source.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
		r.cancel()
	}
	return n, err
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLen := len(data)
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return originalLen, nil
	}
	overflow := len(b.data) + len(data) - b.limit
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return originalLen, nil
}

func (b *limitedBuffer) String() string {
	return string(bytes.Clone(b.data))
}

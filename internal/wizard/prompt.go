package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"
)

var ErrCancelled = errors.New("setup cancelled")

// byteOrderMark is U+FEFF, which some shells prepend to piped UTF-8 input.
const byteOrderMark = string(rune(0xFEFF))

// PlainEnv disables the full-screen prompt widgets and falls back to simple
// line-based questions, for example in screen readers or limited terminals.
const PlainEnv = "FIVEMESH_BACKUP_PLAIN"

type Choice struct {
	Label       string
	Description string
}

// Prompter asks setup questions. On an interactive terminal it renders
// arrow-key selections, inline validation, masked input, and spinners. When
// input is redirected it uses plain numbered prompts, which keeps setup
// scriptable and testable.
type Prompter struct {
	input       io.Reader
	output      io.Writer
	reader      *bufio.Reader
	inputFile   *os.File
	terminal    *os.File
	interactive bool
	// sawFirstLine records whether plain input has been read yet.
	sawFirstLine bool
	rich         bool
	style        uiStyle
}

func NewPrompter(input io.Reader, output io.Writer) *Prompter {
	file, _ := input.(*os.File)
	interactive := file != nil && term.IsTerminal(int(file.Fd()))
	outputFile, _ := output.(*os.File)
	rich := interactive &&
		outputFile != nil && term.IsTerminal(int(outputFile.Fd())) &&
		os.Getenv(PlainEnv) == "" &&
		os.Getenv("TERM") != "dumb"
	prompter := &Prompter{
		input:       input,
		output:      output,
		reader:      bufio.NewReader(input),
		inputFile:   file,
		interactive: interactive,
		rich:        rich,
		style:       newUIStyle(rich),
	}
	if rich {
		enableVirtualTerminal(outputFile)
		prompter.terminal = outputFile
		// Downsample or strip styling for the detected terminal and NO_COLOR.
		prompter.output = colorprofile.NewWriter(outputFile, os.Environ())
	}
	return prompter
}

func (p *Prompter) Interactive() bool {
	return p.interactive
}

// Rich reports whether the prompter renders interactive terminal widgets.
func (p *Prompter) Rich() bool {
	return p.rich
}

func (p *Prompter) Intro() {
	if !p.rich {
		fmt.Fprintln(p.output, "FiveMesh Backup")
		fmt.Fprintln(p.output, "Local, compressed database backups with no FiveMesh account required.")
		fmt.Fprintln(p.output)
		return
	}
	fmt.Fprintln(p.output)
	fmt.Fprintln(p.output, "  "+p.style.badge.Render(" FiveMesh Backup "))
	fmt.Fprintln(p.output, "  "+p.style.faint.Render("Local, compressed MariaDB & MySQL backups. No account required."))
	fmt.Fprintln(p.output, "  "+p.style.faint.Render("Use ↑/↓ and Enter to choose, Esc or Ctrl+C to cancel. Nothing is saved until you confirm."))
	fmt.Fprintln(p.output)
}

func (p *Prompter) Step(number int, title string) {
	if !p.rich {
		fmt.Fprintf(p.output, "\n%d. %s\n", number, title)
		return
	}
	fmt.Fprintln(p.output)
	fmt.Fprintf(p.output, "  %s %s\n", p.style.stepNumber.Render(fmt.Sprintf("%d/%d", number, totalSteps)), p.style.title.Render(title))
	fmt.Fprintln(p.output, "  "+p.style.faint.Render(strings.Repeat(p.style.glyphs.rule, 44)))
}

// Info prints a neutral message.
func (p *Prompter) Info(message string) {
	if !p.rich {
		fmt.Fprintln(p.output, message)
		return
	}
	fmt.Fprintln(p.output, "  "+message)
}

// Note prints secondary guidance.
func (p *Prompter) Note(message string) {
	if !p.rich {
		fmt.Fprintln(p.output, message)
		return
	}
	fmt.Fprintln(p.output, "  "+p.style.faint.Render(message))
}

// Success prints a completed check.
func (p *Prompter) Success(message string) {
	if !p.rich {
		fmt.Fprintln(p.output, message)
		return
	}
	fmt.Fprintln(p.output, "  "+p.style.success.Render(p.style.glyphs.ok)+" "+message)
}

// Warn prints something that needs attention but does not stop setup.
func (p *Prompter) Warn(message string) {
	if !p.rich {
		fmt.Fprintln(p.output, message)
		return
	}
	fmt.Fprintln(p.output, "  "+p.style.warn.Render(p.style.glyphs.warn)+" "+message)
}

// Fail prints a failed check.
func (p *Prompter) Fail(message string) {
	if !p.rich {
		fmt.Fprintln(p.output, message)
		return
	}
	fmt.Fprintln(p.output, "  "+p.style.fail.Render(p.style.glyphs.fail)+" "+message)
}

// Summary prints aligned label/value rows.
func (p *Prompter) Summary(rows [][2]string) {
	if !p.rich {
		for _, row := range rows {
			fmt.Fprintf(p.output, "%s: %s\n", row[0], row[1])
		}
		return
	}
	width := 0
	for _, row := range rows {
		if len(row[0]) > width {
			width = len(row[0])
		}
	}
	var body strings.Builder
	for index, row := range rows {
		if index > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(p.style.faint.Render(fmt.Sprintf("%-*s", width, row[0])))
		body.WriteString("  ")
		body.WriteString(row[1])
	}
	for _, line := range strings.Split(p.style.box.Render(body.String()), "\n") {
		fmt.Fprintln(p.output, "  "+line)
	}
}

// Task runs action while showing a spinner on interactive terminals.
func (p *Prompter) Task(ctx context.Context, title string, action func(context.Context) error) error {
	if !p.rich {
		fmt.Fprintln(p.output, title)
		return action(ctx)
	}
	var actionErr error
	err := spinner.New().
		Title(" " + title).
		WithTheme(p.style.spinnerTheme()).
		WithInput(p.inputFile).
		WithOutput(p.terminal).
		Context(ctx).
		ActionWithErr(func(runContext context.Context) error {
			actionErr = action(runContext)
			return nil
		}).
		Run()
	if err != nil {
		return mapPromptError(err)
	}
	return actionErr
}

func (p *Prompter) Text(label, defaultValue string, validate func(string) error) (string, error) {
	if p.rich {
		return p.richText(label, defaultValue, validate)
	}
	for {
		if defaultValue == "" {
			fmt.Fprintf(p.output, "%s: ", label)
		} else {
			fmt.Fprintf(p.output, "%s [%s]: ", label, defaultValue)
		}
		line, err := p.readLine()
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = defaultValue
		}
		if validate != nil {
			if err := validate(value); err != nil {
				fmt.Fprintf(p.output, "  %v\n", err)
				continue
			}
		}
		return value, nil
	}
}

func (p *Prompter) Integer(label string, defaultValue, minimum, maximum int) (int, error) {
	value, err := p.Text(label, strconv.Itoa(defaultValue), func(value string) error {
		number, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("enter a whole number")
		}
		if number < minimum || number > maximum {
			return fmt.Errorf("enter a value from %d to %d", minimum, maximum)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}

func (p *Prompter) Int64(label string, defaultValue int64, minimum int64) (int64, error) {
	value, err := p.Text(label, strconv.FormatInt(defaultValue, 10), func(value string) error {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("enter a whole number")
		}
		if number < minimum {
			return fmt.Errorf("value must be at least %d", minimum)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

func (p *Prompter) Select(label string, choices []Choice, defaultIndex int) (int, error) {
	if len(choices) == 0 {
		return 0, errors.New("no choices available")
	}
	if defaultIndex < 0 || defaultIndex >= len(choices) {
		defaultIndex = 0
	}
	if p.rich {
		return p.richSelect(label, choices, defaultIndex)
	}
	fmt.Fprintln(p.output, label)
	for index, choice := range choices {
		marker := " "
		if index == defaultIndex {
			marker = "*"
		}
		fmt.Fprintf(p.output, "  %s %d) %s\n", marker, index+1, choice.Label)
		if choice.Description != "" {
			fmt.Fprintf(p.output, "       %s\n", choice.Description)
		}
	}
	for {
		fmt.Fprintf(p.output, "Choose [%d]: ", defaultIndex+1)
		line, err := p.readLine()
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultIndex, nil
		}
		selected, err := strconv.Atoi(line)
		if err == nil && selected >= 1 && selected <= len(choices) {
			return selected - 1, nil
		}
		for index, choice := range choices {
			if strings.EqualFold(line, choice.Label) {
				return index, nil
			}
		}
		fmt.Fprintf(p.output, "  Enter a number from 1 to %d.\n", len(choices))
	}
}

func (p *Prompter) Confirm(label string, defaultValue bool) (bool, error) {
	if p.rich {
		return p.richConfirm(label, defaultValue)
	}
	suffix := "[y/N]"
	if defaultValue {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.output, "%s %s: ", label, suffix)
		line, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.output, "  Enter yes or no.")
		}
	}
}

func (p *Prompter) Secret(label string) (string, error) {
	if p.rich {
		return p.richSecret(label)
	}
	if p.interactive && p.inputFile != nil {
		fmt.Fprintf(p.output, "%s: ", label)
		value, err := term.ReadPassword(int(p.inputFile.Fd()))
		fmt.Fprintln(p.output)
		if err != nil {
			return "", fmt.Errorf("read hidden input: %w", err)
		}
		return string(value), nil
	}
	fmt.Fprintf(p.output, "%s: ", label)
	return p.readLine()
}

func (p *Prompter) SecureSecret(label string) (string, error) {
	if !p.interactive || p.inputFile == nil {
		return "", errors.New("secure terminal password input is unavailable; use the documented secure password-pipe workflow")
	}
	return p.Secret(label)
}

func (p *Prompter) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			if line == "" {
				return "", ErrCancelled
			}
		} else {
			return "", fmt.Errorf("read input: %w", err)
		}
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	// PowerShell and some editors prefix piped UTF-8 input with a byte order
	// mark; it must not become part of the first answer.
	if !p.sawFirstLine {
		p.sawFirstLine = true
		line = strings.TrimPrefix(line, byteOrderMark)
	}
	return line, nil
}

func (p *Prompter) runField(field huh.Field) error {
	form := huh.NewForm(huh.NewGroup(field)).
		WithTheme(p.style.formTheme()).
		WithShowHelp(true).
		WithInput(p.inputFile).
		WithOutput(p.terminal)
	return mapPromptError(form.Run())
}

func (p *Prompter) recap(label, value string) {
	fmt.Fprintf(
		p.output,
		"  %s %s  %s\n",
		p.style.success.Render(p.style.glyphs.ok),
		p.style.faint.Render(strings.TrimSuffix(label, "?")),
		value,
	)
}

func (p *Prompter) richText(label, defaultValue string, validate func(string) error) (string, error) {
	value := defaultValue
	input := huh.NewInput().
		Title(label).
		Prompt(p.style.glyphs.pointer).
		Value(&value).
		Validate(func(current string) error {
			current = strings.TrimSpace(current)
			if current == "" {
				current = defaultValue
			}
			if validate != nil {
				return validate(current)
			}
			return nil
		})
	if defaultValue != "" {
		input = input.Placeholder(defaultValue)
	}
	if err := p.runField(input); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	shown := value
	if shown == "" {
		shown = p.style.faint.Render("(empty)")
	}
	p.recap(label, shown)
	return value, nil
}

func (p *Prompter) richSelect(label string, choices []Choice, defaultIndex int) (int, error) {
	selected := defaultIndex
	options := make([]huh.Option[int], len(choices))
	for index, choice := range choices {
		text := choice.Label
		if choice.Description != "" {
			text += "  " + p.style.faint.Render(choice.Description)
		}
		options[index] = huh.NewOption(text, index)
	}
	field := huh.NewSelect[int]().Title(label).Options(options...).Value(&selected)
	if len(choices) > 8 {
		field = field.Height(10).Filtering(false)
	}
	if err := p.runField(field); err != nil {
		return 0, err
	}
	if selected < 0 || selected >= len(choices) {
		selected = defaultIndex
	}
	p.recap(label, choices[selected].Label)
	return selected, nil
}

func (p *Prompter) richConfirm(label string, defaultValue bool) (bool, error) {
	value := defaultValue
	field := huh.NewConfirm().Title(label).Affirmative("Yes").Negative("No").Value(&value)
	if err := p.runField(field); err != nil {
		return false, err
	}
	answer := "No"
	if value {
		answer = "Yes"
	}
	p.recap(label, answer)
	return value, nil
}

func (p *Prompter) richSecret(label string) (string, error) {
	var value string
	field := huh.NewInput().Title(label).Prompt(p.style.glyphs.pointer).EchoMode(huh.EchoModePassword).Value(&value)
	if err := p.runField(field); err != nil {
		return "", err
	}
	shown := p.style.faint.Render("(hidden)")
	if value == "" {
		shown = p.style.faint.Render("(none entered)")
	}
	p.recap(strings.SplitN(label, " (", 2)[0], shown)
	return value, nil
}

func mapPromptError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, huh.ErrTimeout) {
		return ErrCancelled
	}
	return err
}

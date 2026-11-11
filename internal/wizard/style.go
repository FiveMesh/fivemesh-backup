package wizard

import (
	"os"
	"runtime"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
)

// totalSteps is the number of numbered sections in the setup wizard.
const totalSteps = 4

type glyphSet struct {
	ok, fail, warn, pointer, rule string
}

// Legacy Windows console fonts (Consolas, Lucida Console) lack several common
// symbols; Windows Terminal, VS Code, and Unix terminals render them fine.
var (
	unicodeGlyphs = glyphSet{ok: "✓", fail: "✗", warn: "!", pointer: "❯ ", rule: "─"}
	safeGlyphs    = glyphSet{ok: "√", fail: "×", warn: "!", pointer: "> ", rule: "─"}
)

type uiStyle struct {
	glyphs     glyphSet
	badge      lipgloss.Style
	title      lipgloss.Style
	stepNumber lipgloss.Style
	faint      lipgloss.Style
	success    lipgloss.Style
	warn       lipgloss.Style
	fail       lipgloss.Style
	box        lipgloss.Style
}

func newUIStyle(rich bool) uiStyle {
	glyphs := unicodeGlyphs
	legacyConsole := runtime.GOOS == "windows" &&
		os.Getenv("WT_SESSION") == "" &&
		os.Getenv("TERM_PROGRAM") == ""
	if legacyConsole {
		glyphs = safeGlyphs
	}
	border := lipgloss.RoundedBorder()
	if legacyConsole {
		border = lipgloss.NormalBorder()
	}
	return uiStyle{
		glyphs:     glyphs,
		badge:      lipgloss.NewStyle().Bold(true).Reverse(true),
		title:      lipgloss.NewStyle().Bold(true),
		stepNumber: lipgloss.NewStyle().Faint(true),
		faint:      lipgloss.NewStyle().Faint(true),
		success:    lipgloss.NewStyle().Foreground(lipgloss.Green).Bold(true),
		warn:       lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
		fail:       lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true),
		box:        lipgloss.NewStyle().Border(border).BorderForeground(lipgloss.BrightBlack).Padding(0, 1),
	}
}

// formTheme is a restrained monochrome theme: bold for focus, faint for
// secondary text, and color only for validation errors.
func (s uiStyle) formTheme() huh.Theme {
	glyphs := s.glyphs
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		theme := huh.ThemeBase(isDark)
		lightDark := lipgloss.LightDark(isDark)
		strong := lightDark(lipgloss.Black, lipgloss.BrightWhite)
		muted := lipgloss.BrightBlack
		errorColor := lipgloss.Red

		theme.Focused.Base = theme.Focused.Base.BorderForeground(muted).MarginLeft(2)
		theme.Focused.Title = lipgloss.NewStyle().Foreground(strong).Bold(true)
		theme.Focused.Description = lipgloss.NewStyle().Foreground(muted)
		theme.Focused.ErrorIndicator = lipgloss.NewStyle().Foreground(errorColor).SetString(" *")
		theme.Focused.ErrorMessage = lipgloss.NewStyle().Foreground(errorColor)
		theme.Focused.SelectSelector = lipgloss.NewStyle().Foreground(strong).Bold(true).SetString(glyphs.pointer)
		theme.Focused.Option = lipgloss.NewStyle().Foreground(muted)
		theme.Focused.SelectedOption = lipgloss.NewStyle().Foreground(strong).Bold(true)
		theme.Focused.FocusedButton = lipgloss.NewStyle().Padding(0, 2).MarginRight(1).Bold(true).Reverse(true)
		theme.Focused.BlurredButton = lipgloss.NewStyle().Padding(0, 2).MarginRight(1).Foreground(muted)
		theme.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(strong)
		theme.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(muted)
		theme.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(strong)
		theme.Help.ShortKey = lipgloss.NewStyle().Foreground(muted)
		theme.Help.ShortDesc = lipgloss.NewStyle().Foreground(muted).Faint(true)
		theme.Help.ShortSeparator = lipgloss.NewStyle().Foreground(muted).Faint(true)
		theme.Blurred = theme.Focused
		return theme
	})
}

func (s uiStyle) spinnerTheme() spinner.Theme {
	return spinner.ThemeFunc(func(isDark bool) *spinner.Styles {
		styles := spinner.ThemeDefault(isDark)
		styles.Spinner = lipgloss.NewStyle().Bold(true).PaddingLeft(2)
		styles.Title = lipgloss.NewStyle()
		return styles
	})
}

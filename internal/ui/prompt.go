package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	accentColor  = lipgloss.Color("62")
	mutedColor   = lipgloss.Color("241")
	frameStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(mutedColor).Padding(1, 2)
	titleStyle   = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	labelStyle   = lipgloss.NewStyle().Bold(true)
	hintStyle    = lipgloss.NewStyle().Foreground(mutedColor)
	defaultStyle = lipgloss.NewStyle().Faint(true)

	selectedButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(accentColor).Bold(true).Border(lipgloss.NormalBorder()).BorderForeground(accentColor).Padding(0, 1)
	buttonStyle         = lipgloss.NewStyle().Foreground(accentColor).Border(lipgloss.NormalBorder()).BorderForeground(mutedColor).Padding(0, 1)
)

func clampInt(min, max, value int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

type promptModel struct {
	prompt     string
	allowBlank bool
	input      textinput.Model
	result     string
	answered   bool
	cancelled  bool
	width      int
}

func newPromptModel(prompt, defaultValue string, allowBlank bool) promptModel {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = defaultValue
	ti.SetValue(defaultValue)
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 56
	return promptModel{prompt: prompt, allowBlank: allowBlank, input: ti, width: 64}
}

func newSecretPromptModel(prompt string) promptModel {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = ""
	ti.SetValue("")
	ti.EchoMode = textinput.EchoPassword
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 56
	return promptModel{prompt: prompt, allowBlank: false, input: ti, width: 64}
}

func (m promptModel) Init() tea.Cmd { return textinput.Blink }

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = clampInt(32, 80, msg.Width-8)
		m.input.Width = clampInt(24, 72, msg.Width-12)
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			value := strings.TrimSpace(m.input.Value())
			if value == "" && !m.allowBlank {
				value = strings.TrimSpace(m.input.Placeholder)
			}
			m.result = value
			m.answered = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m promptModel) View() string {
	promptLine := labelStyle.Render(m.prompt)
	if defaultValue := strings.TrimSpace(m.input.Placeholder); defaultValue != "" {
		promptLine += " " + defaultStyle.Render("("+defaultValue+")")
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Input"),
		promptLine,
		"",
		m.input.View(),
		hintStyle.Render("Enter to accept • Esc/Ctrl+C to abort"),
	)
	return frameStyle.Render(body)
}

type confirmModel struct {
	prompt     string
	defaultYes bool
	selected   int
	result     bool
	answered   bool
	cancelled  bool
	width      int
}

type SelectOption struct {
	Label   string
	Value   string
	Default bool
}

type selectModel struct {
	title     string
	options   []SelectOption
	selected  int
	result    string
	answered  bool
	cancelled bool
	width     int
	viewport  int
}

type multiSelectModel struct {
	title     string
	options   []SelectOption
	selected  int
	checked   []bool
	result    []string
	answered  bool
	cancelled bool
	width     int
	viewport  int
}

func newSelectModel(title string, options []SelectOption) selectModel {
	selected := 0
	for i, option := range options {
		if option.Default {
			selected = i
			break
		}
	}
	return selectModel{title: title, options: options, selected: selected, width: 64, viewport: 8}
}

func newMultiSelectModel(title string, options []SelectOption) multiSelectModel {
	checked := make([]bool, len(options))
	for i := range checked {
		checked[i] = true
	}
	return multiSelectModel{title: title, options: options, checked: checked, width: 64, viewport: 8}
}

func newConfirmModel(prompt string, defaultYes bool) confirmModel {
	selected := 1
	if defaultYes {
		selected = 0
	}
	return confirmModel{prompt: prompt, defaultYes: defaultYes, selected: selected, width: 64}
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) selectYes() confirmModel {
	m.selected = 0
	return m
}

func (m confirmModel) selectNo() confirmModel {
	m.selected = 1
	return m
}

func (m confirmModel) toggle() confirmModel {
	if m.selected == 0 {
		return m.selectNo()
	}
	return m.selectYes()
}

func (m confirmModel) previous() confirmModel {
	if m.selected <= 0 {
		m.selected = 1
		return m
	}
	m.selected--
	return m
}

func (m confirmModel) next() confirmModel {
	if m.selected >= 1 {
		m.selected = 0
		return m
	}
	m.selected++
	return m
}

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = clampInt(32, 80, msg.Width-8)
		return m, nil
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "y":
			m.result = true
			m.answered = true
			return m, tea.Quit
		case "n":
			m.result = false
			m.answered = true
			return m, tea.Quit
		case "left", "h", "shift+tab":
			m = m.previous()
			return m, nil
		case "right", "l", "tab":
			m = m.next()
			return m, nil
		}
		switch msg.Type {
		case tea.KeyEnter:
			m.result = m.selected == 0
			m.answered = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) button(label string, selected bool) string {
	if selected {
		return selectedButtonStyle.Render(label)
	}
	return buttonStyle.Render(label)
}

func (m confirmModel) View() string {
	yes := m.button("Yes", m.selected == 0)
	no := m.button("No", m.selected == 1)
	body := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Confirm"),
		labelStyle.Render(m.prompt),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center, yes, "  ", no),
		hintStyle.Render("Use arrows, h/l, tab, y/n, Enter, or Esc/Ctrl+C"),
	)
	return frameStyle.Render(body)
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) move(delta int) selectModel {
	if len(m.options) == 0 {
		return m
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = len(m.options) - 1
	}
	if m.selected >= len(m.options) {
		m.selected = 0
	}
	return m
}

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = clampInt(32, 96, msg.Width-8)
		m.viewport = clampInt(4, 12, msg.Height-10)
		return m, nil
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k", "shift+tab":
			m = m.move(-1)
			return m, nil
		case "down", "j", "tab":
			m = m.move(1)
			return m, nil
		}
		switch msg.Type {
		case tea.KeyEnter:
			if len(m.options) == 0 {
				m.cancelled = true
				return m, tea.Quit
			}
			m.result = m.options[m.selected].Value
			m.answered = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	start := 0
	if m.viewport > 0 && m.selected >= m.viewport {
		start = m.selected - m.viewport + 1
	}
	end := len(m.options)
	if m.viewport > 0 && end > start+m.viewport {
		end = start + m.viewport
	}
	lines := []string{titleStyle.Render("Select"), labelStyle.Render(m.title), ""}
	if len(m.options) == 0 {
		lines = append(lines, hintStyle.Render("No options available"))
	} else {
		for i := start; i < end; i++ {
			prefix := "  "
			style := lipgloss.NewStyle()
			if i == m.selected {
				prefix = "▸ "
				style = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
			}
			lines = append(lines, style.Render(prefix+m.options[i].Label))
		}
	}
	lines = append(lines, hintStyle.Render("Use ↑/↓, j/k, Enter, or Esc/Ctrl+C"))
	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func (m multiSelectModel) Init() tea.Cmd { return nil }

func (m multiSelectModel) move(delta int) multiSelectModel {
	if len(m.options) == 0 {
		return m
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = len(m.options) - 1
	}
	if m.selected >= len(m.options) {
		m.selected = 0
	}
	return m
}

func (m multiSelectModel) toggleSelected() multiSelectModel {
	if len(m.checked) == 0 || m.selected < 0 || m.selected >= len(m.checked) {
		return m
	}
	m.checked[m.selected] = !m.checked[m.selected]
	return m
}

func (m multiSelectModel) selectedValues() []string {
	values := make([]string, 0, len(m.options))
	for i, option := range m.options {
		if i < len(m.checked) && m.checked[i] {
			values = append(values, option.Value)
		}
	}
	return values
}

func (m multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = clampInt(32, 96, msg.Width-8)
		m.viewport = clampInt(4, 12, msg.Height-10)
		return m, nil
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k", "shift+tab":
			m = m.move(-1)
			return m, nil
		case "down", "j", "tab":
			m = m.move(1)
			return m, nil
		case "space":
			m = m.toggleSelected()
			return m, nil
		}
		switch msg.Type {
		case tea.KeySpace:
			m = m.toggleSelected()
			return m, nil
		case tea.KeyEnter:
			m.result = m.selectedValues()
			m.answered = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m multiSelectModel) View() string {
	start := 0
	if m.viewport > 0 && m.selected >= m.viewport {
		start = m.selected - m.viewport + 1
	}
	end := len(m.options)
	if m.viewport > 0 && end > start+m.viewport {
		end = start + m.viewport
	}
	lines := []string{titleStyle.Render("Multi-select"), labelStyle.Render(m.title), ""}
	if len(m.options) == 0 {
		lines = append(lines, hintStyle.Render("No options available"))
	} else {
		for i := start; i < end; i++ {
			prefix := "[ ] "
			style := lipgloss.NewStyle()
			if i < len(m.checked) && m.checked[i] {
				prefix = "[x] "
			}
			if i == m.selected {
				style = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
			}
			lines = append(lines, style.Render(prefix+m.options[i].Label))
		}
	}
	lines = append(lines, hintStyle.Render("Use ↑/↓, j/k, space, Enter, or Esc/Ctrl+C"))
	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func PromptString(prompt, defaultValue string, allowBlank bool) (string, error) {
	program := tea.NewProgram(newPromptModel(prompt, defaultValue, allowBlank))
	model, err := program.Run()
	if err != nil {
		return "", err
	}
	final, ok := model.(promptModel)
	if !ok {
		return "", fmt.Errorf("prompt failed")
	}
	if final.cancelled {
		return "", fmt.Errorf("prompt cancelled")
	}
	if !final.answered {
		return "", fmt.Errorf("prompt failed")
	}
	return final.result, nil
}

func PromptSecret(prompt string) (string, error) {
	program := tea.NewProgram(newSecretPromptModel(prompt))
	model, err := program.Run()
	if err != nil {
		return "", err
	}
	final, ok := model.(promptModel)
	if !ok {
		return "", fmt.Errorf("prompt failed")
	}
	if final.cancelled {
		return "", fmt.Errorf("prompt cancelled")
	}
	if !final.answered {
		return "", fmt.Errorf("prompt failed")
	}
	return final.result, nil
}

func Confirm(prompt string, defaultYes bool) (bool, error) {
	program := tea.NewProgram(newConfirmModel(prompt, defaultYes))
	model, err := program.Run()
	if err != nil {
		return false, err
	}
	final, ok := model.(confirmModel)
	if !ok {
		return false, fmt.Errorf("confirm failed")
	}
	if final.cancelled {
		return false, fmt.Errorf("confirm cancelled")
	}
	if !final.answered {
		return false, fmt.Errorf("confirm failed")
	}
	return final.result, nil
}

func Select(title string, options []SelectOption) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options available")
	}
	program := tea.NewProgram(newSelectModel(title, options))
	model, err := program.Run()
	if err != nil {
		return "", err
	}
	final, ok := model.(selectModel)
	if !ok {
		return "", fmt.Errorf("select failed")
	}
	if final.cancelled {
		return "", fmt.Errorf("select cancelled")
	}
	if !final.answered {
		return "", fmt.Errorf("select failed")
	}
	return final.result, nil
}

func MultiSelect(title string, options []SelectOption) ([]string, error) {
	if len(options) == 0 {
		return nil, fmt.Errorf("no options available")
	}
	program := tea.NewProgram(newMultiSelectModel(title, options))
	model, err := program.Run()
	if err != nil {
		return nil, err
	}
	final, ok := model.(multiSelectModel)
	if !ok {
		return nil, fmt.Errorf("multi-select failed")
	}
	if final.cancelled {
		return nil, fmt.Errorf("multi-select cancelled")
	}
	if !final.answered {
		return nil, fmt.Errorf("multi-select failed")
	}
	return final.result, nil
}

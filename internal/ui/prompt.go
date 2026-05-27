package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type promptModel struct {
	prompt     string
	allowBlank bool
	input      textinput.Model
	result     string
	answered   bool
	cancelled  bool
}

func newPromptModel(prompt, defaultValue string, allowBlank bool) promptModel {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = defaultValue
	ti.SetValue(defaultValue)
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 80
	return promptModel{prompt: prompt, allowBlank: allowBlank, input: ti}
}

func (m promptModel) Init() tea.Cmd { return textinput.Blink }

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
	promptStyle := lipgloss.NewStyle().Bold(true)
	helpStyle := lipgloss.NewStyle().Faint(true)
	return fmt.Sprintf("%s\n\n%s\n\n%s\n", promptStyle.Render(m.prompt), m.input.View(), helpStyle.Render("Enter to accept, Ctrl+C to abort"))
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

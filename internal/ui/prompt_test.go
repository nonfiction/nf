package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConfirmModelDefaultsAndKeys(t *testing.T) {
	m := newConfirmModel("Proceed?", false)
	if got, want := m.selected, 1; got != want {
		t.Fatalf("default selected = %d, want %d", got, want)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(confirmModel)
	if got, want := m.selected, 0; got != want {
		t.Fatalf("left selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(confirmModel)
	if got, want := m.selected, 1; got != want {
		t.Fatalf("down selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(confirmModel)
	if got, want := m.selected, 0; got != want {
		t.Fatalf("up selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(confirmModel)
	if got, want := m.selected, 1; got != want {
		t.Fatalf("right selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(confirmModel)
	if got, want := m.selected, 0; got != want {
		t.Fatalf("shift+tab selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(confirmModel)
	if !m.answered || !m.result {
		t.Fatalf("enter result = %#v, want answered yes", m)
	}

	m = newConfirmModel("Proceed?", true)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(confirmModel)
	if !m.answered || m.result {
		t.Fatalf("n result = %#v, want answered no", m)
	}
}

func TestConfirmModelViewIsCompactAndBorderless(t *testing.T) {
	m := newConfirmModel("Proceed?", false)
	view := m.View()
	for _, border := range []string{"╭", "╮", "╰", "╯", "│", "─", "[ Yes ]"} {
		if strings.Contains(view, border) {
			t.Fatalf("confirm view contains modal border/button %q:\n%s", border, view)
		}
	}
	if !strings.Contains(view, "> No") {
		t.Fatalf("confirm default should visibly select No:\n%s", view)
	}
}

func TestPromptModelAllowBlankUsesPlaceholder(t *testing.T) {
	m := newPromptModel("Name", "demo", false)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if !m.answered {
		t.Fatal("prompt not answered")
	}
	if got, want := m.result, "demo"; got != want {
		t.Fatalf("prompt result = %q, want %q", got, want)
	}
}

func TestSecretPromptModelPrepopulatesWithoutExposingDefault(t *testing.T) {
	m := newSecretPromptModel("Secret", "sensitive-value")
	if got, want := m.input.Value(), "sensitive-value"; got != want {
		t.Fatalf("secret input value = %q, want %q", got, want)
	}
	view := m.View()
	if strings.Contains(view, "sensitive-value") {
		t.Fatalf("secret prompt exposed initial value:\n%s", view)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if got, want := m.result, "sensitive-value"; got != want {
		t.Fatalf("secret result = %q, want %q", got, want)
	}
}

func TestSelectModelNavigationAndSubmit(t *testing.T) {
	m := newSelectModel("Choose", []SelectOption{{Label: "One", Value: "1"}, {Label: "Two", Value: "2"}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(selectModel)
	if got, want := m.selected, 1; got != want {
		t.Fatalf("down selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(selectModel)
	if got, want := m.selected, 0; got != want {
		t.Fatalf("tab selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(selectModel)
	if !m.answered || m.result != "1" {
		t.Fatalf("enter result = %#v, want answered value 1", m)
	}
}

func TestSelectModelSupportsFirstAndLastKeys(t *testing.T) {
	m := newSelectModel("Choose", []SelectOption{{Label: "One", Value: "1"}, {Label: "Two", Value: "2"}, {Label: "Three", Value: "3"}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(selectModel)
	if got, want := m.selected, 2; got != want {
		t.Fatalf("G selected = %d, want %d", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(selectModel)
	if got, want := m.selected, 0; got != want {
		t.Fatalf("g selected = %d, want %d", got, want)
	}
}

func TestSelectModelViewIsCompactAndBorderless(t *testing.T) {
	m := newSelectModel("Choose", []SelectOption{{Label: "One / target app1", Value: "1"}, {Label: "Two / target app2", Value: "2"}})
	view := m.View()
	for _, border := range []string{"╭", "╮", "╰", "╯", "│", "─", "Select\n"} {
		if strings.Contains(view, border) {
			t.Fatalf("select view contains modal border/title %q:\n%s", border, view)
		}
	}
	if !strings.Contains(view, "> One  target app1") {
		t.Fatalf("select view should use compact cursor and label spacing:\n%s", view)
	}
}

func TestSelectModelHonorsDefaultOption(t *testing.T) {
	m := newSelectModel("Choose", []SelectOption{{Label: "One", Value: "1"}, {Label: "Two", Value: "2", Default: true}, {Label: "Three", Value: "3"}})
	if got, want := m.selected, 1; got != want {
		t.Fatalf("default selected = %d, want %d", got, want)
	}
}

func TestMultiSelectModelDefaultsToAllSelected(t *testing.T) {
	m := newMultiSelectModel("Choose", []SelectOption{{Label: "One", Value: "1"}, {Label: "Two", Value: "2"}})
	if got, want := len(m.selectedValues()), 2; got != want {
		t.Fatalf("selectedValues count = %d, want %d", got, want)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(multiSelectModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(multiSelectModel)
	if !m.answered || len(m.result) != 1 || m.result[0] != "2" {
		t.Fatalf("multi-select result = %#v, want only second option selected", m)
	}
}

func TestMultiSelectModelCanStartWithNoneSelected(t *testing.T) {
	m := newMultiSelectModelWithInitial("Choose", []SelectOption{{Label: "One", Value: "1"}, {Label: "Two", Value: "2"}}, false)
	if got := len(m.selectedValues()); got != 0 {
		t.Fatalf("selectedValues count = %d, want 0", got)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(multiSelectModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(multiSelectModel)
	if !m.answered || len(m.result) != 1 || m.result[0] != "1" {
		t.Fatalf("multi-select result = %#v, want only first option selected", m)
	}
}

package ui

import (
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

package bot

// /tasks — lists the tasks rafiña has sent me, straight from ~/.zoro/tasks.json.
// The agent (Claude) owns that file: it adds tasks and updates status/completada
// (stamping completada_el); this command only reads and renders title + check.
// A completed task shows only on the day it was completed; after that it counts
// as archived and is hidden (it stays in the file as history).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tasksPath = ".zoro/tasks.json"

type taskItem struct {
	Titulo       string `json:"titulo"`
	Status       string `json:"status"`
	Completada   bool   `json:"completada"`
	CompletadaEl string `json:"completada_el,omitempty"` // YYYY-MM-DD, stamped when done
	Creada       string `json:"creada,omitempty"`
}

type tasksFile struct {
	Tasks []taskItem `json:"tasks"`
}

// tasksText renders the task list for Telegram: pending first, then today's
// done ones; older done tasks are archived (hidden, only counted in the footer).
func tasksText() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/rafael"
	}
	raw, err := os.ReadFile(filepath.Join(home, tasksPath))
	if err != nil {
		return "⚪ No hay tasks todavía (~/.zoro/tasks.json no existe)."
	}
	var f tasksFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "⚠️ No pude leer tasks.json: " + err.Error()
	}
	return renderTasks(f.Tasks, time.Now().Format("2006-01-02"))
}

func renderTasks(tasks []taskItem, today string) string {
	var open, doneToday []string
	archived := 0
	for _, t := range tasks {
		switch {
		case !t.Completada:
			open = append(open, "⬜ "+t.Titulo)
		case t.CompletadaEl == today:
			doneToday = append(doneToday, "✅ "+t.Titulo)
		default: // done on a previous day (or undated) → archived
			archived++
		}
	}
	if len(open) == 0 && len(doneToday) == 0 {
		if archived > 0 {
			return fmt.Sprintf("✨ Cero tasks pendientes · %d archivadas", archived)
		}
		return "✨ Cero tasks."
	}
	lines := []string{"⚔️ Tasks", ""}
	lines = append(lines, open...)
	if len(doneToday) > 0 {
		if len(open) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, doneToday...)
	}
	foot := fmt.Sprintf("%d pendientes · %d hechas hoy", len(open), len(doneToday))
	if archived > 0 {
		foot += fmt.Sprintf(" · %d archivadas", archived)
	}
	lines = append(lines, "", foot)
	return strings.Join(lines, "\n")
}
